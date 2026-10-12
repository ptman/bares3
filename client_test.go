package bares3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

// P1: a file:// client holds an open directory handle that must be released.
func TestClientCloseReleasesLocalRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "k", Body: strings.NewReader("x"), ContentLength: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k"}); err == nil {
		t.Fatal("local operation succeeded after Close")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// A remote client holds no resources, so Close must succeed and stay harmless.
func TestClientCloseIsNoOpForRemote(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, http.Header{}, ""), nil })
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"}); err != nil {
		t.Fatalf("remote operation after Close: %v", err)
	}
}

// A body returned before Close stays usable, so callers can drain it.
func TestClientCloseLeavesOpenBodiesUsable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "k", Body: strings.NewReader("payload"), ContentLength: 7}); err != nil {
		t.Fatal(err)
	}
	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatalf("read after Close: %v", err)
	}
	if err := out.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("body = %q, want %q", data, "payload")
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

// Every operation must refuse to build a request when no bucket can be
// resolved, rather than falling back to an empty bucket or to the endpoint.
func TestNoBucketAcrossAllOperations(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{"GetObject", func(c *Client) error {
			_, err := c.GetObject(context.Background(), GetObjectInput{Key: "x"})
			return err
		}},
		{"PutObject", func(c *Client) error {
			_, err := c.PutObject(context.Background(), PutObjectInput{Key: "x"})
			return err
		}},
		{"DeleteObject", func(c *Client) error {
			_, err := c.DeleteObject(context.Background(), DeleteObjectInput{Key: "x"})
			return err
		}},
		{"HeadObject", func(c *Client) error {
			_, err := c.HeadObject(context.Background(), HeadObjectInput{Key: "x"})
			return err
		}},
		{"ListObjectsV2", func(c *Client) error {
			_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{})
			return err
		}},
		{"CreateBucket", func(c *Client) error {
			_, err := c.CreateBucket(context.Background(), CreateBucketInput{})
			return err
		}},
		{"HeadBucket", func(c *Client) error {
			_, err := c.HeadBucket(context.Background(), HeadBucketInput{})
			return err
		}},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			remote := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				t.Error("a request was sent without a resolved bucket")
				return response(200, http.Header{}, ""), nil
			})
			if err := operation.call(remote); !errors.Is(err, ErrNoBucket) {
				t.Errorf("remote error = %v, want ErrNoBucket", err)
			}
			if err := operation.call(newLocalClient(t)); !errors.Is(err, ErrNoBucket) {
				t.Errorf("local error = %v, want ErrNoBucket", err)
			}
		})
	}
}

// An empty key must be rejected before any request is built. On the wire a
// keyless object operation addresses the bucket root, where a GET lists the
// bucket, a PUT acts on the bucket, and a DELETE removes it.
func TestEmptyKeyRejectedBeforeTransport(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{"GetObject", func(c *Client) error {
			_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: ""})
			return err
		}},
		{"PutObject", func(c *Client) error {
			_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: ""})
			return err
		}},
		{"DeleteObject", func(c *Client) error {
			_, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: ""})
			return err
		}},
		{"HeadObject", func(c *Client) error {
			_, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: ""})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			t.Parallel()
			remote := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				t.Error("a request was sent for an empty key")
				return response(200, http.Header{}, ""), nil
			})
			err := operation.call(remote)
			var s3Err *S3Error
			if !errors.As(err, &s3Err) || s3Err.Code != "InvalidObjectName" || s3Err.StatusCode != 400 {
				t.Fatalf("remote error = %v, want InvalidObjectName 400", err)
			}

			local := newLocalClient(t)
			if _, err := local.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
				t.Fatal(err)
			}
			if err := operation.call(local); !errors.As(err, &s3Err) || s3Err.Code != "InvalidObjectName" {
				t.Fatalf("local error = %v, want InvalidObjectName", err)
			}
		})
	}
}

// HeadBucket and local CreateBucket used to bypass SetBucket, so a default
// bucket was honored by six operations but silently ignored by these two.
// Pin the shared resolution on both backends.
func TestDefaultBucketReachesHeadAndCreate(t *testing.T) {
	t.Parallel()
	var got *http.Request
	remote := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{}, ""), nil
	})
	remote.SetBucket("default")
	if _, err := remote.HeadBucket(context.Background(), HeadBucketInput{}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.URL.Path != "/default" {
		t.Fatalf("HeadBucket URL = %v, want path /default", got)
	}

	local := newLocalClient(t)
	local.SetBucket("default")
	if _, err := local.CreateBucket(context.Background(), CreateBucketInput{}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.HeadBucket(context.Background(), HeadBucketInput{}); err != nil {
		t.Fatalf("HeadBucket with default bucket = %v", err)
	}
}
