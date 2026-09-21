package bares3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type closeTrackingReader struct {
	io.Reader
	closed bool
}

func (r *closeTrackingReader) Close() error {
	r.closed = true
	return nil
}

func TestSigV4RequestVectorWithEncodedKeyAndQuery(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{}, "data"), nil
	})
	out, err := c.GetObject(context.Background(), GetObjectInput{
		Bucket: "bucket", Key: "a%2Fb + ü", VersionId: "v 1", ResponseContentType: "text/plain",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Body.Close()
	if got.URL.EscapedPath() != "/bucket/a%252Fb%20%2B%20%C3%BC" {
		t.Fatalf("escaped path = %q", got.URL.EscapedPath())
	}
	if got.URL.Query().Get("versionId") != "v 1" || got.URL.Query().Get("response-content-type") != "text/plain" {
		t.Fatalf("query = %q", got.URL.RawQuery)
	}
	if !strings.Contains(got.Header.Get("Authorization"), "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization = %q", got.Header.Get("Authorization"))
	}
}

func TestRemoteUploadClosesSourceOnTransportFailure(t *testing.T) {
	t.Parallel()
	source := &closeTrackingReader{Reader: strings.NewReader("payload")}
	transportErr := errors.New("transport failed")
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return nil, transportErr })
	_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: source, ContentLength: 7})
	if !errors.Is(err, transportErr) {
		t.Fatalf("error = %v", err)
	}
	if !source.closed {
		t.Fatal("upload source was not closed")
	}
}

func TestRemoteUploadSourceCloseOnLengthMismatch(t *testing.T) {
	t.Parallel()
	source := &closeTrackingReader{Reader: strings.NewReader("x")}
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		_, err := io.ReadAll(r.Body)
		return response(200, http.Header{}, ""), err
	})
	_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: source, ContentLength: 2})
	if err == nil || !source.closed {
		t.Fatalf("error = %v, closed = %v", err, source.closed)
	}
}

func TestLocalListPaginationAfterCommonPrefix(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/file", "b", "c/file", "d"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader(key), ContentLength: int64(len(key))}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: 1})
	if err != nil || first.KeyCount != 1 || !first.IsTruncated {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", ContinuationToken: "a/", MaxKeys: 10})
	if err != nil || second.KeyCount == 0 || second.Contents[0].Key != "b" {
		t.Fatalf("second = %+v, %v", second, err)
	}
}

func TestRemoteErrorSemanticsAcrossOperations(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{"get", func(c *Client) error {
			_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"})
			return err
		}},
		{"put", func(c *Client) error {
			_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x"})
			return err
		}},
		{"delete", func(c *Client) error {
			_, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: "x"})
			return err
		}},
		{"head", func(c *Client) error {
			_, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: "x"})
			return err
		}},
		{"list", func(c *Client) error {
			_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
			return err
		}},
		{"bucket", func(c *Client) error {
			_, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "b"})
			return err
		}},
		{"create", func(c *Client) error {
			_, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"})
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				return response(503, http.Header{"X-Amz-Request-Id": {"req"}}, `<Error><Code>SlowDown</Code><Message>retry</Message></Error>`), nil
			})
			err := operation.call(c)
			var s3Err *S3Error
			if !errors.As(err, &s3Err) || s3Err.Code != "SlowDown" || s3Err.StatusCode != 503 || s3Err.RequestID != "req" {
				t.Fatalf("error = %+v", err)
			}
		})
	}
}
