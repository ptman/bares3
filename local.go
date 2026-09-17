package bares3

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
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
	bucket := input.Bucket
	if err := validateLocalBucket(bucket); err != nil {
		if bucket == "" {
			return nil, ErrNoBucket
		}
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
	if err := validateLocalBucket(input.Bucket); err != nil {
		if input.Bucket == "" {
			return nil, ErrNoBucket
		}
		return nil, &S3Error{Code: "InvalidBucketName", Message: err.Error(), StatusCode: 400}
	}
	dir := input.Bucket
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
	if err := validateLocalKey(input.Key); err != nil {
		return nil, &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, input.Key)
	if err := c.osRoot.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}

	f, err := c.osRoot.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}
	defer func() { _ = f.Close() }()

	h := md5.New()
	payloadHash, hashErr := parseContentSHA256(input.ContentSHA256)
	if hashErr != nil {
		return nil, hashErr
	}
	payload := sha256.New()
	body := input.Body
	if body == nil {
		body = strings.NewReader("")
	}
	writers := []io.Writer{f, h}
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
			return nil, err
		}
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}
	if input.ContentLength > 0 && written != input.ContentLength {
		return nil, &S3Error{Code: "InvalidRequest", Message: "content length does not match body", StatusCode: 400}
	}
	if input.ContentLength > 0 {
		var extra [1]byte
		if n, readErr := body.Read(extra[:]); readErr != io.EOF || n != 0 {
			return nil, &S3Error{Code: "InvalidRequest", Message: "content length does not match body", StatusCode: 400}
		}
	}
	if len(payloadHash) != 0 && !bytes.Equal(payload.Sum(nil), payloadHash) {
		return nil, &S3Error{Code: "InvalidRequest", Message: "content SHA-256 does not match ContentSHA256", StatusCode: 400}
	}

	etag := `"` + hex.EncodeToString(h.Sum(nil)) + `"`

	return &PutObjectOutput{
		ETag: etag,
	}, nil
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
	if err := validateLocalKey(input.Key); err != nil {
		return nil, &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, input.Key)
	f, err := c.osRoot.Open(path)
	if err != nil {
		return nil, &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist."}
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, &S3Error{Code: "InternalError", Message: err.Error()}
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist."}
	}

	contentType := mime.TypeByExtension(filepath.Ext(input.Key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	etag := computeLocalETag(c.osRoot, path)

	return &GetObjectOutput{
		ContentLength: info.Size(),
		ContentType:   contentType,
		LastModified:  info.ModTime(),
		ETag:          etag,
		Body:          f,
	}, nil
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
	if err := validateLocalKey(input.Key); err != nil {
		return nil, &S3Error{Code: "InvalidObjectName", Message: err.Error(), StatusCode: 400}
	}
	path := c.localPath(bucket, input.Key)
	info, err := c.osRoot.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist."}
	}

	contentType := mime.TypeByExtension(filepath.Ext(input.Key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	etag := computeLocalETag(c.osRoot, path)

	return &HeadObjectOutput{
		ContentLength: info.Size(),
		ContentType:   contentType,
		LastModified:  info.ModTime(),
		ETag:          etag,
	}, nil
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

	root := bucket
	prefix := input.Prefix
	delimiter := input.Delimiter

	var contents []Object
	var commonPrefixes []CommonPrefix
	prefixSet := make(map[string]bool)

	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

			if delimiter != "" {
				afterPrefix := rel[len(prefix):]
				if idx := strings.Index(afterPrefix, delimiter); idx >= 0 {
					common := prefix + afterPrefix[:idx+len(delimiter)]
					if !prefixSet[common] {
						prefixSet[common] = true
						commonPrefixes = append(commonPrefixes, CommonPrefix{Prefix: common})
					}
					// A key represented by a common prefix is not separately listed.
					return nil
				}
			}

			etag := computeLocalETag(c.osRoot, path)
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
	truncated := len(entries) > maxKeys
	if delimiter != "" && len(entries) > maxKeys {
		// The API exposes object and prefix collections separately. Keep direct
		// objects ahead of derived prefixes at a page boundary.
		for i := range entries {
			for j := i + 1; j < len(entries); j++ {
				if entries[i].object == nil && entries[j].object != nil {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}
	}
	if truncated {
		entries = entries[:maxKeys]
	}
	if delimiter != "" {
		// The public output separates objects and prefixes. For compatibility,
		// direct objects are returned before prefixes when both share a page.
		for i := range entries {
			for j := i + 1; j < len(entries); j++ {
				if entries[i].object == nil && entries[j].object != nil {
					entries[i], entries[j] = entries[j], entries[i]
				}
			}
		}
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
	return &ListObjectsV2Output{Name: bucket, Prefix: input.Prefix, Delimiter: input.Delimiter, MaxKeys: maxKeys, Contents: contents, CommonPrefixes: commonPrefixes, KeyCount: len(entries), IsTruncated: truncated}, nil
}

func computeLocalETag(r *os.Root, path string) string {
	f, err := r.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return `"` + hex.EncodeToString(h.Sum(nil)) + `"`
}
