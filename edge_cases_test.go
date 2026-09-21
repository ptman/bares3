package bares3

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestUploadReadCloserAdditionalErrorPaths(t *testing.T) {
	t.Parallel()
	readErr := errors.New("read error")
	r := &uploadReadCloser{body: &errorCloseReader{Reader: readErrorCloser{err: readErr}}, hash: sha256.New(), limit: 1}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, readErr) {
		t.Fatalf("read error = %v", err)
	}

	closeErr := errors.New("close error")
	r = &uploadReadCloser{body: &errorCloseReader{Reader: strings.NewReader("x"), err: closeErr}, hash: sha256.New(), limit: 1}
	if n, err := r.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("initial read = %d, %v", n, err)
	}
	if _, err := r.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("final read = %v", err)
	}
}

func TestLocalListMaxKeysAndContinuationBoundaries(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/x", "a/y", "b", "c/z"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader(key), ContentLength: int64(len(key))}); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []ListObjectsV2Input{
		{Bucket: "b", Prefix: "a/", Delimiter: "/", MaxKeys: 1},
		{Bucket: "b", Prefix: "a/", Delimiter: "/", MaxKeys: 2},
		{Bucket: "b", StartAfter: "a/x"},
		{Bucket: "b", ContinuationToken: "b"},
	} {
		out, err := c.ListObjectsV2(ctx, input)
		if err != nil {
			t.Fatalf("input %+v: %v", input, err)
		}
		if out.KeyCount > out.MaxKeys {
			t.Fatalf("input %+v returned %d > %d", input, out.KeyCount, out.MaxKeys)
		}
	}
}

func TestRemoteMalformedAndEmptyErrors(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 403, 404, 409, 429, 500, 503} {
		c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
			return response(status, http.Header{}, ""), nil
		})
		_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
		var s3Err *S3Error
		if !errors.As(err, &s3Err) || s3Err.StatusCode != status || s3Err.Message == "" {
			t.Fatalf("status %d error = %+v", status, err)
		}
	}
}

func TestResponseMetadataOptionalAndNegativeValues(t *testing.T) {
	t.Parallel()
	m := responseMetadata(http.Header{
		"Content-Length":       {"-1"},
		"X-Amz-Mp-Parts-Count": {"0"},
	})
	if m.ContentLength != -1 || m.PartsCount == nil || *m.PartsCount != 0 {
		t.Fatalf("metadata = %+v", m)
	}
	if deleteMarker(http.Header{"X-Amz-Delete-Marker": {"false"}}) != nil {
		t.Fatal("false delete marker should be nil")
	}
}
