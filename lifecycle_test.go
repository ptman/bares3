package bares3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error { b.closed = true; return nil }

func TestRemoteGetErrorClosesBody(t *testing.T) {
	t.Parallel()
	body := &trackingBody{Reader: strings.NewReader(`<Error><Code>Denied</Code><Message>no</Message></Error>`)}
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Status: "403 Forbidden", Header: http.Header{}, Body: body}, nil
	})
	_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"})
	if err == nil || !body.closed {
		t.Fatalf("error = %v, closed = %v", err, body.closed)
	}
}

func TestRemoteSuccessBodyOwnership(t *testing.T) {
	t.Parallel()
	body := &trackingBody{Reader: strings.NewReader("data")}
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
	})
	out, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if body.closed {
		t.Fatal("GetObject closed caller-owned body")
	}
	if err := out.Body.Close(); err != nil || !body.closed {
		t.Fatalf("caller close: %v, closed=%v", err, body.closed)
	}
}

func TestLocalDirectoryObjectErrors(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.Mkdir("b/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "dir"}); err == nil {
		t.Error("HeadObject accepted directory")
	}
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "b", Key: "dir"}); err != nil {
		t.Fatalf("DeleteObject directory: %v", err)
	}
	if _, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
}

func TestUploadReadCloserCloseAndEOFVerification(t *testing.T) {
	t.Parallel()
	body := &trackingBody{Reader: strings.NewReader("x")}
	r := &uploadReadCloser{body: body, hash: sha256.New(), expected: mustHash("x"), limit: -1}
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "x" {
		t.Fatalf("read = %q, %v", data, err)
	}
	if err := r.Close(); err != nil || !body.closed {
		t.Fatalf("close = %v, closed=%v", err, body.closed)
	}
}

func TestUploadReadCloserFinalReadAndCloseError(t *testing.T) {
	t.Parallel()
	body := &trackingBody{Reader: strings.NewReader("x")}
	r := &uploadReadCloser{body: body, hash: sha256.New(), expected: mustHash("x"), limit: 1}
	p := make([]byte, 2)
	if n, err := r.Read(p); n != 1 || err != nil {
		t.Fatalf("first read = %d, %v", n, err)
	}
	if n, err := r.Read(p); n != 0 || err != io.EOF {
		t.Fatalf("final read = %d, %v", n, err)
	}

	closeErr := errors.New("close failed")
	closer := &errorCloseReader{Reader: strings.NewReader("x"), err: closeErr}
	r = &uploadReadCloser{body: closer, hash: sha256.New(), limit: -1}
	if err := r.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v", err)
	}
}

func TestUploadReadCloserLengthAndReadErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		body  io.Reader
		limit int64
	}{
		{"short", strings.NewReader("x"), 2},
		{"extra", strings.NewReader("xy"), 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &uploadReadCloser{body: asReadCloser(test.body), hash: sha256.New(), limit: test.limit}
			if _, err := io.ReadAll(r); err == nil {
				t.Fatal("expected length error")
			}
		})
	}
	readErr := errors.New("read failed")
	r := &uploadReadCloser{body: asReadCloser(readErrorCloser{err: readErr}), hash: sha256.New(), limit: -1}
	if _, err := io.ReadAll(r); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v", err)
	}
}

func mustHash(value string) []byte {
	decoded, err := hex.DecodeString(HashSHA256([]byte(value)))
	if err != nil {
		panic(err)
	}
	return decoded
}

type errorCloseReader struct {
	io.Reader
	err error
}

func (r *errorCloseReader) Close() error { return r.err }

func TestS3ErrorNilTarget(t *testing.T) {
	t.Parallel()
	if (&S3Error{Code: "x"}).Is((*S3Error)(nil)) {
		t.Error("non-nil error matched nil target")
	}
	if !errors.Is(&S3Error{Code: "x"}, &S3Error{Code: "x"}) {
		t.Error("matching errors did not match")
	}
}
