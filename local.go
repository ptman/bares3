package bares3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

func validateLocalPath(name string, allowEmpty bool) error {
	if name == "" {
		if allowEmpty {
			return nil
		}
		return errors.New("path must not be empty")
	}
	if filepath.IsAbs(name) || strings.ContainsRune(name, '\x00') {
		return errors.New("path must be relative and contain no NUL bytes")
	}
	if slices.Contains(strings.FieldsFunc(filepath.ToSlash(name), func(r rune) bool { return r == '/' }), "..") {
		return errors.New("path traversal is not allowed")
	}
	return nil
}

func (c *Client) localPath(bucket, key string) string {
	return filepath.Join(bucket, key)
}

var localTmpSeq atomic.Uint64

// localTempFile opens a fresh, same-directory file for an in-progress write
// and reports the root-relative name that must be used with the root's
// methods; (*os.File).Name() returns an absolute path, which os.Root
// rejects. The O_EXCL creation means a stale name (from a crashed run) is a
// retryable collision, never a truncation of existing data.
func (c *Client) localTempFile(dir string) (*os.File, string, error) {
	var err error
	for i := 0; i < 8; i++ {
		name := filepath.Join(dir, fmt.Sprintf(".put-%d-%d", os.Getpid(), localTmpSeq.Add(1)))
		var f *os.File
		f, err = c.osRoot.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil || !os.IsExist(err) {
			return f, name, err
		}
	}
	return nil, "", &S3Error{Code: "InternalError", Message: "could not allocate a temporary write file"}
}

func validateLocalBucket(bucket string) error {
	return validateLocalPath(bucket, false)
}

func validateLocalKey(key string) error {
	return validateLocalPath(key, false)
}

func (c *Client) localHeadBucket(ctx context.Context, input HeadBucketInput) (*HeadBucketOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}
	if err := validateLocalBucket(bucket); err != nil {
		return nil, &S3Error{Code: "InvalidBucketName", Message: err.Error(), StatusCode: 400}
	}

	info, err := c.osRoot.Stat(bucket)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSuchBucket
		}
		return nil, &S3Error{Code: "InternalError", Message: err.Error(), StatusCode: 500}
	}

	if !info.IsDir() {
		return nil, &S3Error{Code: "InternalError", Message: "Bucket name is not a directory", StatusCode: 500}
	}

	return &HeadBucketOutput{
		LastModified: info.ModTime(),
	}, nil
}

func (c *Client) localCreateBucket(ctx context.Context, input CreateBucketInput) (*CreateBucketOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}
	if err := validateLocalBucket(bucket); err != nil {
		return nil, &S3Error{Code: "InvalidBucketName", Message: err.Error(), StatusCode: 400}
	}
	dir := bucket
	if err := c.osRoot.MkdirAll(dir, 0o755); err != nil {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}
	return &CreateBucketOutput{}, nil
}

func (c *Client) localPutObject(ctx context.Context, input PutObjectInput) (*PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	if _, err := c.localHeadBucket(ctx, HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, err
	}
	if input.Key == "" {
		return nil, emptyObjectKeyError()
	}
	if err := validateLocalKey(input.Key); err != nil {
		return nil, &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, input.Key)
	dir := filepath.Dir(path)
	if err := c.osRoot.MkdirAll(dir, 0o755); err != nil {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}

	// The upload goes to a temporary file in the same directory and is
	// renamed over the key only at the end. A S3 put is all-or-nothing, so a
	// failed write must not leave a truncated object behind.
	tmp, tmpName, err := c.localTempFile(dir)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*PutObjectOutput, error) {
		_ = tmp.Close()
		_ = c.osRoot.Remove(tmpName)
		return nil, err
	}

	h := md5.New()
	payloadHash, hashErr := parseContentSHA256(input.ContentSHA256)
	if hashErr != nil {
		return fail(hashErr)
	}
	payload := sha256.New()
	body := input.Body
	if body == nil {
		body = strings.NewReader("")
	}
	// Once the upload starts the client owns the body, matching the remote
	// backend, whose transport closes it. Bodies are closed here only after
	// validation has passed; an earlier rejection leaves them untouched.
	if closer, ok := body.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	writers := []io.Writer{tmp, h}
	if len(payloadHash) != 0 {
		writers = append(writers, payload)
	}
	mw := io.MultiWriter(writers...)
	reader := contextReader{ctx: ctx, reader: body}
	if input.ContentLength > 0 {
		reader.reader = io.LimitReader(reader.reader, input.ContentLength)
	}
	written, err := io.Copy(mw, reader)
	if err != nil {
		if errors.Is(err, ctx.Err()) {
			return fail(err)
		}
		return fail(&S3Error{Code: "InternalError", Message: err.Error()})
	}
	if input.ContentLength > 0 && written != input.ContentLength {
		return fail(&S3Error{Code: "InvalidRequest", Message: "content length does not match body", StatusCode: 400})
	}
	if input.ContentLength > 0 {
		var extra [1]byte
		if n, readErr := body.Read(extra[:]); readErr != io.EOF || n != 0 {
			return fail(&S3Error{Code: "InvalidRequest", Message: "content length does not match body", StatusCode: 400})
		}
	}
	if len(payloadHash) != 0 && !bytes.Equal(payload.Sum(nil), payloadHash) {
		return fail(&S3Error{Code: "InvalidRequest", Message: "content SHA-256 does not match ContentSHA256", StatusCode: 400})
	}

	if err := tmp.Close(); err != nil {
		return fail(&S3Error{Code: "InternalError", Message: err.Error()})
	}
	if err := c.osRoot.Rename(tmpName, path); err != nil {
		return fail(&S3Error{Code: "InternalError", Message: err.Error()})
	}

	etag := `"` + hex.EncodeToString(h.Sum(nil)) + `"`

	return &PutObjectOutput{
		ETag: etag,
	}, nil
}

// localReadObject resolves a key for a read: it validates the key, confirms
// the bucket and a regular file exist, computes the ETag, and evaluates the
// conditional-read inputs. Get and Head share it so the two read paths cannot
// drift. The returned file is open and belongs to the caller.
func (c *Client) localReadObject(bucket, key, ifMatch, ifNoneMatch string, ifModifiedSince, ifUnmodifiedSince *time.Time) (*os.File, fs.FileInfo, string, string, error) {
	if key == "" {
		return nil, nil, "", "", emptyObjectKeyError()
	}
	if err := validateLocalKey(key); err != nil {
		return nil, nil, "", "", &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, key)
	f, err := c.osRoot.Open(path)
	if err != nil {
		return nil, nil, "", "", ErrNoSuchKey
	}
	fail := func(err error) (*os.File, fs.FileInfo, string, string, error) {
		_ = f.Close()
		return nil, nil, "", "", err
	}

	info, err := f.Stat()
	if err != nil {
		return fail(&S3Error{Code: "InternalError", Message: err.Error()})
	}
	if !info.Mode().IsRegular() {
		return fail(ErrNoSuchKey)
	}

	contentType := mime.TypeByExtension(filepath.Ext(key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	etag, err := computeLocalETag(c.osRoot, path)
	if err != nil {
		return fail(err)
	}

	if err := checkLocalPreconditions(etag, info.ModTime(), ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince); err != nil {
		return fail(err)
	}

	return f, info, contentType, etag, nil
}

func (c *Client) localGetObject(ctx context.Context, input GetObjectInput) (*GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	if _, err := c.localHeadBucket(ctx, HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, err
	}
	f, info, contentType, etag, err := c.localReadObject(bucket, input.Key, input.IfMatch, input.IfNoneMatch, input.IfModifiedSince, input.IfUnmodifiedSince)
	if err != nil {
		return nil, err
	}

	out := &GetObjectOutput{
		ContentLength: info.Size(),
		ContentType:   contentType,
		LastModified:  info.ModTime(),
		ETag:          etag,
		AcceptRanges:  "bytes",
		Body:          f,
	}

	start, end, ok, err := resolveByteRange(input.Range, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if ok {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			_ = f.Close()
			return nil, &S3Error{Code: "InternalError", Message: err.Error()}
		}
		length := end - start + 1
		out.ContentLength = length
		out.ContentRange = formatContentRange(start, end, info.Size())
		out.Body = &limitedBody{Reader: io.LimitReader(f, length), Closer: f}
	}

	return out, nil
}

func (c *Client) localHeadObject(ctx context.Context, input HeadObjectInput) (*HeadObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	if _, err := c.localHeadBucket(ctx, HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, err
	}
	f, info, contentType, etag, err := c.localReadObject(bucket, input.Key, input.IfMatch, input.IfNoneMatch, input.IfModifiedSince, input.IfUnmodifiedSince)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	out := &HeadObjectOutput{
		ContentLength: info.Size(),
		ContentType:   contentType,
		LastModified:  info.ModTime(),
		ETag:          etag,
		AcceptRanges:  "bytes",
	}

	if start, end, ok, err := resolveByteRange(input.Range, info.Size()); err != nil {
		return nil, err
	} else if ok {
		out.ContentLength = end - start + 1
		out.ContentRange = formatContentRange(start, end, info.Size())
	}

	return out, nil
}

func (c *Client) localDeleteObject(ctx context.Context, input DeleteObjectInput) (*DeleteObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	if _, err := c.localHeadBucket(ctx, HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, err
	}
	if input.Key == "" {
		return nil, emptyObjectKeyError()
	}
	if err := validateLocalKey(input.Key); err != nil {
		return nil, &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, input.Key)
	if err := c.osRoot.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}
	return &DeleteObjectOutput{}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (c *Client) localListObjectsV2(ctx context.Context, input ListObjectsV2Input) (*ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	// A missing bucket is an error, not an empty listing. Walking an absent
	// directory yields nothing, which would report success for a bucket that
	// does not exist.
	if _, err := c.localHeadBucket(ctx, HeadBucketInput{Bucket: bucket}); err != nil {
		return nil, err
	}

	root := bucket
	prefix := input.Prefix
	delimiter := input.Delimiter

	var contents []Object
	var commonPrefixes []CommonPrefix
	prefixSet := make(map[string]bool)

	walkDir := func(dirPath string) error {
		return fs.WalkDir(c.osRoot.FS(), dirPath, func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return nil
			}

			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			if rel == "." {
				return nil
			}
			rel = filepath.ToSlash(rel)

			info, err := d.Info()
			if err != nil {
				return nil
			}

			if d.IsDir() {
				dirSlash := rel + "/"
				if prefix != "" && !strings.HasPrefix(dirSlash, prefix) && !strings.HasPrefix(prefix, dirSlash) {
					return filepath.SkipDir
				}
				if delimiter != "" && strings.HasPrefix(dirSlash, prefix) {
					afterPrefix := dirSlash[len(prefix):]
					if idx := strings.Index(afterPrefix, delimiter); idx >= 0 {
						common := prefix + afterPrefix[:idx+len(delimiter)]
						if !prefixSet[common] {
							prefixSet[common] = true
							commonPrefixes = append(commonPrefixes, CommonPrefix{Prefix: common})
						}
						return filepath.SkipDir
					}
				}
				return nil
			}

			if prefix != "" && !strings.HasPrefix(rel, prefix) {
				return nil
			}

			// No file-level delimiter handling is needed: a file whose key
			// contains the delimiter sits under a directory that the check
			// above already reported as a common prefix and skipped.

			etag, err := computeLocalETag(c.osRoot, path)
			if err != nil {
				// One unreadable object must not fail the whole listing.
				return nil
			}
			contents = append(contents, Object{
				Key:          rel,
				LastModified: info.ModTime(),
				ETag:         etag,
				Size:         info.Size(),
				StorageClass: "STANDARD",
			})

			return nil
		})
	}

	if err := walkDir(root); err != nil {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}

	type entry struct {
		key    string
		object *Object
		prefix *CommonPrefix
	}
	entries := make([]entry, 0, len(contents)+len(commonPrefixes))
	for i := range contents {
		entries = append(entries, entry{key: contents[i].Key, object: &contents[i]})
	}
	for i := range commonPrefixes {
		entries = append(entries, entry{key: commonPrefixes[i].Prefix, prefix: &commonPrefixes[i]})
	}

	// A common prefix occupies the same ordered position as the first key
	// beneath it. Keep object entries that compare before that prefix, but do
	// not let an implementation-specific prefix tie change page contents.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key != entries[j].key {
			return entries[i].key < entries[j].key
		}
		return entries[i].object != nil
	})
	start := input.StartAfter
	if input.ContinuationToken > start {
		start = input.ContinuationToken
	}
	if start != "" {
		entries = slices.DeleteFunc(entries, func(e entry) bool { return e.key <= start })
	}
	maxKeys := input.MaxKeys
	if maxKeys == 0 {
		maxKeys = 1000
	}
	// A page is the first MaxKeys entries in key order. Objects and common
	// prefixes share that single ordering: reordering them before truncation
	// would drop entries that belong on the page and admit later ones instead.
	truncated := len(entries) > maxKeys
	if truncated {
		entries = entries[:maxKeys]
	}
	contents = contents[:0]
	commonPrefixes = commonPrefixes[:0]
	for _, e := range entries {
		if e.object != nil {
			contents = append(contents, *e.object)
		} else {
			commonPrefixes = append(commonPrefixes, *e.prefix)
		}
	}

	// The token is the last key on the page. Both ContinuationToken and
	// StartAfter already resume after that key, so the documented pagination
	// loop works without the caller synthesizing a value.
	out := &ListObjectsV2Output{
		Name:              bucket,
		Prefix:            input.Prefix,
		Delimiter:         input.Delimiter,
		MaxKeys:           maxKeys,
		Contents:          contents,
		CommonPrefixes:    commonPrefixes,
		KeyCount:          len(entries),
		IsTruncated:       truncated,
		ContinuationToken: input.ContinuationToken,
	}
	if truncated && len(entries) > 0 {
		out.NextContinuationToken = entries[len(entries)-1].key
	}
	return out, nil
}

// etagFromFile returns the MD5 of an open file's contents in S3's quoted form,
// leaving the read position where it found it so a ranged read can continue
// from the same handle.
func etagFromFile(f *os.File) (string, error) {
	position, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return "", err
	}
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if _, err := f.Seek(position, io.SeekStart); err != nil {
		return "", err
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`, nil
}

// computeLocalETag returns the MD5 of the file's contents in S3's quoted form.
// A read failure is reported rather than reduced to an empty string: an empty
// ETag would silently fail later conditional reads and would be reported to the
// caller as a valid object.
func computeLocalETag(r *os.Root, path string) (string, error) {
	f, err := r.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return etagFromFile(f)
}
