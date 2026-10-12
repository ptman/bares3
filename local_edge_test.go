package bares3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalContextCancellationAcrossOperations(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateBucket error = %v", err)
	}
	if _, err := c.HeadBucket(ctx, HeadBucketInput{Bucket: "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("HeadBucket error = %v", err)
	}
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetObject error = %v", err)
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("HeadObject error = %v", err)
	}
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteObject error = %v", err)
	}
	if _, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListObjectsV2 error = %v", err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutObject error = %v", err)
	}
}

// A context cancelled while the body is streaming must surface the context
// error, not a generic I/O error. The reader signals that the copy has
// started, then blocks until the goroutine cancels the context.
type gatedReader struct {
	started chan struct{}
	ctx     context.Context
}

func (g *gatedReader) Read(p []byte) (int, error) {
	select {
	case g.started <- struct{}{}:
	default:
	}
	<-g.ctx.Done()
	return 0, g.ctx.Err()
}

func TestLocalPutObjectContextCancelledDuringCopy(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	body := &gatedReader{started: started, ctx: ctx}
	go func() {
		<-started
		cancel()
	}()
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: body}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutObject error = %v, want context.Canceled", err)
	}
}

// These failures come from the filesystem itself, so they are forced with the
// least surprising filesystem facts: a name that exists as the wrong kind of
// object, and a directory that cannot be removed while non-empty.
func TestLocalFileSystemFailurePaths(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	// A regular file squatting on the bucket name makes MkdirAll fail.
	if err := c.osRoot.WriteFile("b", []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err == nil {
		t.Fatal("CreateBucket over an existing file should fail")
	}
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "ok"}); err != nil {
		t.Fatal(err)
	}
	// A file squatting on a key path component makes the parent MkdirAll fail.
	if err := c.osRoot.WriteFile(filepath.Join("ok", "sub"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "ok", Key: "sub/x", Body: strings.NewReader("x")}); err == nil {
		t.Fatal("PutObject under an existing file should fail")
	}
	// A key that exists as a directory makes OpenFile fail.
	if err := c.osRoot.MkdirAll(filepath.Join("ok", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "ok", Key: "dir", Body: strings.NewReader("x")}); err == nil {
		t.Fatal("PutObject onto a directory should fail")
	}
	// A malformed hash is rejected before any upload.
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "ok", Key: "x", Body: strings.NewReader("x"), ContentSHA256: "nothex"}); err == nil {
		t.Fatal("PutObject with a malformed ContentSHA256 should fail")
	}
	// Remove fails on a non-empty directory; IsNotExist does not mask it.
	if err := c.osRoot.WriteFile(filepath.Join("ok", "dir", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "ok", Key: "dir"}); err == nil {
		t.Fatal("DeleteObject of a non-empty directory should fail")
	}
}

// failAfterOneRead delivers a single byte, then fails, so a put dies mid-copy.
type failAfterOneRead struct{ once bool }

func (f *failAfterOneRead) Read(p []byte) (int, error) {
	if f.once {
		return 0, errors.New("write interrupted")
	}
	f.once = true
	p[0] = 'x'
	return 1, nil
}

// A S3 put is all-or-nothing. A write that dies mid-copy must leave the
// previous object intact, must not create a partial one, and must not leave
// its temporary file behind.
func TestLocalPutObjectAtomicOnFailure(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("original")}); err != nil {
		t.Fatal(err)
	}

	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: &failAfterOneRead{}}); err == nil {
		t.Fatal("interrupted overwrite should fail")
	}
	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x"})
	if err != nil {
		t.Fatalf("GetObject after failed overwrite = %v", err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil || string(data) != "original" {
		t.Fatalf("content after failed overwrite = %q, %v", data, err)
	}

	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "new", Body: &failAfterOneRead{}}); err == nil {
		t.Fatal("interrupted first write should fail")
	}
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "new"}); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("GetObject = %v, want ErrNoSuchKey", err)
	}

	entries, err := fs.ReadDir(c.osRoot.FS(), "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".put-") {
			t.Fatalf("leftover temp file %q after failed writes", e.Name())
		}
	}
}

// If every temporary name localTempFile can try is already taken (for
// example by leftovers of several crashed runs), the write must fail with a
// clean error instead of retrying forever. The test reserves the next eight
// sequence values explicitly and runs un-parallel so no other test advances
// the counter in between.
func TestLocalPutObjectTempNameExhaustion(t *testing.T) {
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	base := localTmpSeq.Load()
	pid := os.Getpid()
	for i := uint64(1); i <= 8; i++ {
		name := filepath.Join("b", fmt.Sprintf(".put-%d-%d", pid, base+i))
		if err := c.osRoot.WriteFile(name, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x")})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "InternalError" || s3Err.Message != "could not allocate a temporary write file" {
		t.Fatalf("err = %+v, want InternalError temp name exhaustion", s3Err)
	}
}

// Renaming over a path that exists as a directory must fail the write, leave
// the directory in place, and clean up the temporary file.
func TestLocalPutObjectRenameOverDirectory(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.MkdirAll(filepath.Join("b", "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "d", Body: strings.NewReader("x")})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "InternalError" {
		t.Fatalf("err = %+v, want InternalError rename failure", s3Err)
	}
	if _, err := c.osRoot.Stat(filepath.Join("b", "d")); err != nil {
		t.Fatalf("target directory disappeared: %v", err)
	}
	entries, err := fs.ReadDir(c.osRoot.FS(), "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".put-") {
			t.Fatalf("leftover temp file %q after failed rename", e.Name())
		}
	}
}

// cancelOnFirstRead serves one byte, cancels the context, and then relies on
// the contextReader's own check to fail the next read: the cancellation lands
// between reads, not inside the body.
type cancelOnFirstRead struct {
	cancel context.CancelFunc
	once   bool
}

func (r *cancelOnFirstRead) Read(p []byte) (int, error) {
	if !r.once {
		r.once = true
		r.cancel()
		p[0] = 'x'
		return 1, nil
	}
	return 0, errors.New("unreachable")
}

func TestLocalPutObjectContextCheckedBetweenReads(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: &cancelOnFirstRead{cancel: cancel}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PutObject error = %v, want context.Canceled", err)
	}
}

// countCtx passes through Err for the first n calls, then reports the context
// as canceled, so a test can decide exactly which check sees the cancellation.
type countCtx struct {
	context.Context
	left int
}

func (c *countCtx) Err() error {
	if c.left > 0 {
		c.left--
		return nil
	}
	return context.Canceled
}

// The walk of a large listing re-checks the context on every entry. The
// listing's own check and the bucket's HEAD check each consume one pass, so a
// counter of two lands the cancellation inside the walk.
func TestLocalListObjectsV2ContextCancelledInWalk(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	ctx := &countCtx{Context: context.Background(), left: 2}
	_, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "InternalError" {
		t.Fatalf("err = %v, want InternalError from the canceled walk", err)
	}
}

// A dangling symlink inside a bucket is unreadable, so the listing must skip
// it instead of failing the whole page.
func TestLocalListingSkipsUnreadableObject(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.WriteFile("b/real.txt", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.Symlink("missing-target", "b/link"); err != nil {
		t.Fatal(err)
	}
	out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b"})
	if err != nil {
		t.Fatalf("ListObjectsV2 = %v", err)
	}
	if len(out.Contents) != 1 || out.Contents[0].Key != "real.txt" {
		t.Fatalf("contents = %+v, want only real.txt", out.Contents)
	}
}

func TestLocalPutObjectLengthAndHashValidation(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}

	for _, input := range []PutObjectInput{
		{Bucket: "b", Key: "short", Body: strings.NewReader("x"), ContentLength: 2},
		{Bucket: "b", Key: "extra", Body: strings.NewReader("xy"), ContentLength: 1},
		{Bucket: "b", Key: "hash", Body: strings.NewReader("x"), ContentSHA256: HashSHA256([]byte("y"))},
	} {
		if _, err := c.PutObject(ctx, input); err == nil {
			t.Fatalf("PutObject(%q) unexpectedly succeeded", input.Key)
		}
	}

	body := []byte("valid")
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "valid", Body: bytes.NewReader(body), ContentLength: int64(len(body)), ContentSHA256: HashSHA256(body)}); err != nil {
		t.Fatal(err)
	}
}

func TestUploadReadCloserZeroLengthBuffer(t *testing.T) {
	t.Parallel()
	r := &uploadReadCloser{body: io.NopCloser(strings.NewReader("")), limit: 0}
	if n, err := r.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("Read = %d, %v", n, err)
	}
}

func TestURIEncodeEncodesEveryReservedByte(t *testing.T) {
	t.Parallel()
	// A percent sign is data, not an escape, so it must itself be encoded.
	// Callers decode the result, never re-decode it.
	for input, want := range map[string]string{
		"%": "%25", "%zz": "%25zz", "!": "%21", "'": "%27", "(": "%28", ")": "%29", "*": "%2A",
		"+": "%2B", "=": "%3D", "&": "%26", "/": "%2F", " ": "%20",
	} {
		if got := uriEncode(input); got != want {
			t.Errorf("uriEncode(%q) = %q, want %q", input, got, want)
		}
	}
}
