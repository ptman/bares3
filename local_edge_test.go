package bares3

import (
	"bytes"
	"context"
	"errors"
	"io"
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

func TestDecodeEscapedPathInvalidEscape(t *testing.T) {
	t.Parallel()
	if got := decodeEscapedPath("%zz"); got != "%zz" {
		t.Fatalf("decodeEscapedPath = %q", got)
	}
}
