package bares3

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestNewClientVirtualHostAndEndpointEdges(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientParams{Endpoint: "https://example.com", UseVirtualHostStyle: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(ClientParams{Endpoint: "https://example.com/", UseVirtualHostStyle: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(ClientParams{Endpoint: "https://example.com/path", UseVirtualHostStyle: true}); err == nil {
		t.Fatal("path endpoint should fail")
	}
}

func TestRemoteMissingBucketDoesNotCallTransport(t *testing.T) {
	t.Parallel()
	called := false
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected") })
	_, err := c.GetObject(context.Background(), GetObjectInput{Key: "x"})
	if !errors.Is(err, ErrNoBucket) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestEnsureBucketRemoteCreateFailure(t *testing.T) {
	t.Parallel()
	calls := 0
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(404, http.Header{}, `<Error><Code>NotFound</Code><Message>missing</Message></Error>`), nil
		}
		return response(409, http.Header{}, `<Error><Code>BucketAlreadyOwnedByYou</Code><Message>exists</Message></Error>`), nil
	})
	_, err := c.EnsureBucket(context.Background(), EnsureBucketInput{Bucket: "b"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "BucketAlreadyOwnedByYou" || calls != 2 {
		t.Fatalf("error = %+v, calls=%d", err, calls)
	}
}

func TestS3ErrorMatchingNilAndDifferentTypes(t *testing.T) {
	t.Parallel()
	var nilErr *S3Error
	if nilErr.Is(ErrNoSuchKey) {
		t.Error("nil receiver should not match")
	}
	err := &S3Error{Code: "X"}
	if err.Is(errors.New("X")) {
		t.Error("S3Error should not match unrelated error")
	}
	if err.Is(&S3Error{Code: "Y"}) {
		t.Error("different codes should not match")
	}
}

func TestLocalInvalidBucketOperations(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	for _, bucket := range []string{"../x", "/x", "a\x00b"} {
		for _, call := range []func() error{
			func() error { _, err := c.HeadBucket(ctx, HeadBucketInput{Bucket: bucket}); return err },
			func() error { _, err := c.EnsureBucket(ctx, EnsureBucketInput{Bucket: bucket}); return err },
		} {
			if err := call(); err == nil {
				t.Errorf("invalid bucket %q accepted", bucket)
			}
		}
	}
}
