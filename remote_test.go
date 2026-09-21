package bares3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func remoteTestClient(t *testing.T, rt roundTripFunc) *Client {
	t.Helper()
	c, err := NewClient(ClientParams{
		Endpoint:   "https://s3.example.com",
		Region:     "us-east-1",
		AccessKey:  testAccessKey,
		SecretKey:  testSecretKey,
		HTTPClient: &http.Client{Transport: rt},
		Now:        func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func response(status int, headers http.Header, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRemoteVirtualHostedRequest(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c, err := NewClient(ClientParams{
		Endpoint: "https://s3.example.com:8443", Region: "r", AccessKey: "a", SecretKey: "s",
		UseVirtualHostStyle: true, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { got = r; return response(200, http.Header{}, "x"), nil })},
		Now: func() time.Time { return time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "bucket", Key: "a b/ümlaut"})
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Body.Close()
	if got.Host != "bucket.s3.example.com:8443" || got.URL.Path != "/a%20b/%C3%BCmlaut" && got.URL.EscapedPath() != "/a%20b/%C3%BCmlaut" {
		t.Fatalf("request URL = %s, host=%s", got.URL.String(), got.Host)
	}
}

func TestRemoteOperations_RequestAndResponsePaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		call  func(*Client) error
		check func(*testing.T, *http.Request)
		resp  *http.Response
	}{
		{
			name: "get",
			call: func(c *Client) error {
				out, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "dir/a b.txt", Range: "bytes=1-2", VersionId: "v", ResponseContentType: "text/plain"})
				if err == nil {
					defer func() { _ = out.Body.Close() }()
					data, readErr := io.ReadAll(out.Body)
					if readErr != nil || string(data) != "ok" {
						return errors.New("unexpected get body")
					}
				}
				return err
			},
			check: func(t *testing.T, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/b/dir/a b.txt" || r.URL.RawPath != "" {
					t.Errorf("request = %s %s", r.Method, r.URL.String())
				}
				if r.Header.Get("Range") != "bytes=1-2" || r.URL.Query().Get("versionId") != "v" {
					t.Error("GET options missing")
				}
			},
			resp: response(200, http.Header{"Content-Length": {"2"}, "Content-Range": {"bytes 1-2/4"}, "ETag": {"\"e\""}}, "ok"),
		},
		{
			name: "put",
			call: func(c *Client) error {
				_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("payload"), ContentLength: 7, Metadata: map[string]string{"foo": "bar"}, Tagging: "a=b"})
				return err
			},
			check: func(t *testing.T, r *http.Request) {
				if r.Method != http.MethodPut || r.Header.Get("X-Amz-Meta-Foo") != "bar" || r.Header.Get("X-Amz-Tagging") != "a=b" {
					t.Error("PUT headers missing")
				}
				data, _ := io.ReadAll(r.Body)
				if string(data) != "payload" || r.Header.Get("X-Amz-Content-Sha256") != HashSHA256(data) {
					t.Error("PUT payload/hash mismatch")
				}
			},
			resp: response(200, http.Header{"ETag": {"\"put\""}, "x-amz-version-id": {"v1"}}, ""),
		},
		{
			name: "delete",
			call: func(c *Client) error {
				out, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: "x", VersionId: "v1"})
				if err == nil && (out.VersionId != "v1" || out.DeleteMarker == nil || !*out.DeleteMarker) {
					return errors.New("unexpected delete output")
				}
				return err
			},
			check: func(t *testing.T, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Query().Get("versionId") != "v1" {
					t.Error("DELETE request mismatch")
				}
			},
			resp: response(204, http.Header{"X-Amz-Version-Id": {"v1"}, "X-Amz-Delete-Marker": {"true"}}, ""),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *http.Request
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) { got = r; return tt.resp, nil })
			if err := tt.call(c); err != nil {
				t.Fatal(err)
			}
			tt.check(t, got)
		})
	}
}

func TestRemoteOperations_ErrorAndTransportPaths(t *testing.T) {
	t.Parallel()
	errBody := `<Error><Code>NoSuchKey</Code><Message>missing</Message><RequestId>r</RequestId></Error>`
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(404, http.Header{"X-Amz-Request-Id": {"h"}}, errBody), nil
	})
	_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "missing"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "NoSuchKey" || s3Err.RequestID != "h" {
		t.Fatalf("unexpected S3 error: %v", err)
	}
	if !errors.Is(err, ErrNoSuchKey) {
		t.Error("error should match ErrNoSuchKey")
	}

	transportErr := errors.New("transport failure")
	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) { return nil, transportErr })
	_, err = c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "b"})
	if !errors.Is(err, transportErr) {
		t.Fatalf("transport error = %v", err)
	}
}

func TestRemoteOperations_ListAndBucketParsing(t *testing.T) {
	t.Parallel()
	body := `<ListBucketResult><Name>b</Name><Prefix>p/</Prefix><KeyCount>2</KeyCount><MaxKeys>10</MaxKeys><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>p/a</Key><LastModified>2024-01-02T03:04:05Z</LastModified><ETag>&#34;e&#34;</ETag><Size>3</Size><StorageClass>STANDARD</StorageClass><Owner><ID>id</ID><DisplayName>name</DisplayName></Owner></Contents><CommonPrefixes><Prefix>p/sub/</Prefix></CommonPrefixes></ListBucketResult>`
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) { got = r; return response(200, http.Header{}, body), nil })
	out, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b", Prefix: "p/", Delimiter: "/", MaxKeys: 10, FetchOwner: true, ContinuationToken: "old", StartAfter: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if out.KeyCount != 2 || len(out.Contents) != 1 || out.Contents[0].Owner == nil || out.NextContinuationToken != "next" {
		t.Fatalf("unexpected list output: %+v", out)
	}
	if got.URL.Query().Get("list-type") != "2" || got.URL.Query().Get("fetch-owner") != "true" {
		t.Errorf("list query = %s", got.URL.RawQuery)
	}

	got = nil
	c = remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{"Location": {"/b"}}, ""), nil
	})
	outBucket, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b", ACL: "private", CreateBucketConfiguration: &CreateBucketConfiguration{LocationConstraint: "eu-west-1"}})
	if err != nil || outBucket.Location != "/b" {
		t.Fatalf("CreateBucket = %+v, %v", outBucket, err)
	}
	data, _ := io.ReadAll(got.Body)
	if !bytes.Contains(data, []byte("eu-west-1")) || got.Header.Get("X-Amz-Acl") != "private" {
		t.Errorf("bucket request body/headers invalid: %q", data)
	}
}

func TestRemoteHeadOperations(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{
			"Content-Length":   {"7"},
			"Content-Type":     {"text/plain"},
			"Etag":             {"\"etag\""},
			"Last-Modified":    {"Mon, 02 Jan 2006 15:04:05 GMT"},
			"X-Amz-Version-Id": {"v1"},
		}, ""), nil
	})
	head, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: "x", VersionId: "v1", Range: "bytes=0-1"})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentLength != 7 || head.ContentType != "text/plain" || head.ETag != "\"etag\"" || head.VersionId != "v1" {
		t.Fatalf("unexpected head output: %+v", head)
	}
	if got.URL.Query().Get("versionId") != "v1" || got.Header.Get("Range") != "bytes=0-1" {
		t.Errorf("HEAD request missing options: %s", got.URL.String())
	}

	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, http.Header{"Last-Modified": {"Mon, 02 Jan 2006 15:04:05 GMT"}}, ""), nil
	})
	bucket, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "b"})
	if err != nil || bucket.LastModified.IsZero() {
		t.Fatalf("HeadBucket = %+v, %v", bucket, err)
	}

	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(404, http.Header{}, ""), nil
	})
	_, err = c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: "x"})
	if err == nil {
		t.Fatal("HeadObject should return an error")
	}
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "NotFound" {
		t.Fatalf("unexpected HeadObject error: %v", err)
	}
}

func TestRemotePutObjectPayloadModes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		input    PutObjectInput
		wantHash string
		wantBody string
	}{
		{name: "empty", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("ignored")}, wantHash: EmptySHA256, wantBody: ""},
		{name: "unknown", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("stream"), ContentLength: -1}, wantHash: "UNSIGNED-PAYLOAD", wantBody: "stream"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotHash, gotBody string
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				gotHash = r.Header.Get("X-Amz-Content-Sha256")
				body, _ := io.ReadAll(r.Body)
				gotBody = string(body)
				return response(200, http.Header{}, ""), nil
			})
			if _, err := c.PutObject(context.Background(), tt.input); err != nil {
				t.Fatal(err)
			}
			if gotHash != tt.wantHash || gotBody != tt.wantBody {
				t.Errorf("hash/body = %q/%q, want %q/%q", gotHash, gotBody, tt.wantHash, tt.wantBody)
			}
		})
	}
}

func TestEnsureBucketRemote(t *testing.T) {
	t.Parallel()
	calls := 0
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(404, http.Header{}, `<Error><Code>NoSuchBucket</Code><Message>missing</Message></Error>`), nil
		}
		return response(200, http.Header{"Location": {"/b"}}, ""), nil
	})
	out, err := c.EnsureBucket(context.Background(), EnsureBucketInput{Bucket: "b"})
	if err != nil || out == nil || !out.Created || calls != 2 {
		t.Fatalf("EnsureBucket = %+v, %v, calls=%d", out, err, calls)
	}
}

func TestRemoteOperationMalformedAndErrorResponses(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		call func(*Client) error
	}{
		{name: "put error", call: func(c *Client) error {
			_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: 1})
			return err
		}},
		{name: "delete error", call: func(c *Client) error {
			_, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: "x"})
			return err
		}},
		{name: "list malformed", call: func(c *Client) error {
			_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
			return err
		}},
		{name: "create error", call: func(c *Client) error {
			_, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				if test.name == "list malformed" {
					return response(200, http.Header{}, "not xml"), nil
				}
				return response(500, http.Header{}, `<Error><Code>InternalError</Code><Message>broken</Message></Error>`), nil
			})
			if err := test.call(c); err == nil {
				t.Fatal("expected operation error")
			}
		})
	}

	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(404, http.Header{}, ""), nil })
	_, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "b"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "NotFound" {
		t.Fatalf("unexpected HeadBucket error: %v", err)
	}
}

func TestRemoteOperationContextAndRequestFailures(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	_, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context error = %v", err)
	}

	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("failed") })
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x"}); err == nil {
		t.Error("PutObject should return transport error")
	}
	if _, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"}); err == nil {
		t.Error("ListObjectsV2 should return transport error")
	}
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err == nil {
		t.Error("CreateBucket should return transport error")
	}
}

func TestNewClientFileErrorsAndDefaults(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientParams{Endpoint: "file:///path/that/does/not/exist"}); err == nil {
		t.Error("invalid local root should fail")
	}
	c, err := NewClient(ClientParams{Endpoint: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if c.region != "us-east-1" || c.httpClient == nil || c.now == nil {
		t.Fatalf("defaults not initialized: %+v", c)
	}
	for _, endpoint := range []string{"example.com", "https:///missing-host", "https://example.com/a/b", "ftp://example.com/path"} {
		if _, err := NewClient(ClientParams{Endpoint: endpoint}); err == nil {
			t.Errorf("endpoint %q should fail", endpoint)
		}
	}
}

func TestLocalEdgeCases(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.PutObject(context.Background(), PutObjectInput{Key: "x"}); !errors.Is(err, ErrNoBucket) {
		t.Errorf("missing bucket = %v", err)
	}
	if _, err := c.EnsureBucket(context.Background(), EnsureBucketInput{}); !errors.Is(err, ErrNoBucket) {
		t.Errorf("empty ensure bucket = %v", err)
	}
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "file"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "file/child"}); err != nil {
		t.Fatalf("local backend currently permits nested bucket paths: %v", err)
	}
	if _, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "file/child"}); err != nil {
		t.Fatalf("nested bucket should be visible with current local semantics: %v", err)
	}
}

func TestEnsureBucketRemoteExistingAndUnexpectedError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		status      int
		body        string
		wantCreated bool
	}{
		{name: "existing", status: 200, wantCreated: false},
		{name: "access denied", status: 403, body: `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				return response(test.status, http.Header{}, test.body), nil
			})
			out, err := c.EnsureBucket(context.Background(), EnsureBucketInput{Bucket: "b"})
			if test.name == "existing" {
				if err != nil || out == nil || out.Created || calls != 1 {
					t.Fatalf("existing = %+v, %v, calls=%d", out, err, calls)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("expected error, got %+v, %v, calls=%d", out, err, calls)
			}
		})
	}
}

func TestRemoteGetOptionsAndPutHeaders(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{}, "body"), nil
	})
	obj, err := c.GetObject(context.Background(), GetObjectInput{
		Bucket: "b", Key: "x", IfMatch: "match", IfNoneMatch: "none",
		IfModifiedSince: ptrTime(), IfUnmodifiedSince: ptrTime(),
		Range: "bytes=0-1", SSECustomerAlgorithm: "AES256", SSECustomerKey: "key", SSECustomerKeyMD5: "md5",
		ResponseCacheControl: "max-age=1", ResponseContentDisposition: "inline", ResponseContentEncoding: "gzip",
		ResponseContentLanguage: "en", ResponseContentType: "text/plain", ResponseExpires: "tomorrow", VersionId: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = obj.Body.Close()
	for key, want := range map[string]string{"If-Match": "match", "If-None-Match": "none", "Range": "bytes=0-1", "x-amz-server-side-encryption-customer-algorithm": "AES256", "x-amz-server-side-encryption-customer-key": "key", "x-amz-server-side-encryption-customer-key-MD5": "md5"} {
		if got.Header.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Header.Get(key), want)
		}
	}
	if got.URL.Query().Get("response-content-language") != "en" || got.URL.Query().Get("response-expires") != "tomorrow" {
		t.Errorf("GET query = %s", got.URL.RawQuery)
	}

	c = remoteTestClient(t, func(r *http.Request) (*http.Response, error) { got = r; return response(200, http.Header{}, ""), nil })
	when := ptrTime()
	_, err = c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: 1, ContentType: "text/plain", ACL: "private", CacheControl: "no-cache", ContentDisposition: "inline", ContentEncoding: "gzip", ContentLanguage: "en", ContentMD5: "md5", Expires: when, ServerSideEncryption: "AES256", StorageClass: "STANDARD", WebsiteRedirectLocation: "/new", Tagging: "k=v", Metadata: map[string]string{"one": "two"}})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"Content-Type": "text/plain", "X-Amz-Acl": "private", "Cache-Control": "no-cache", "Content-Disposition": "inline", "Content-Encoding": "gzip", "Content-Language": "en", "Content-MD5": "md5", "X-Amz-Server-Side-Encryption": "AES256", "X-Amz-Storage-Class": "STANDARD", "X-Amz-Website-Redirect-Location": "/new", "X-Amz-Tagging": "k=v", "X-Amz-Meta-One": "two"} {
		if got.Header.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Header.Get(key), want)
		}
	}
	if got.Header.Get("Expires") == "" {
		t.Error("Expires header missing")
	}
}

func TestLocalNonDirectoryAndETagMissing(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if err := c.osRoot.WriteFile("plain", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "plain"}); err == nil {
		t.Error("file should not be a bucket")
	}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "plain", Key: "x", Body: strings.NewReader("x")}); err == nil {
		t.Error("put into file bucket should fail")
	}
	if computeLocalETag(c.osRoot, filepath.Join("missing", "x")) != "" {
		t.Error("missing ETag should be empty")
	}
}

func TestLocalErrorReadersAndMissingPaths(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "missing", Key: "x"}); !errors.Is(err, ErrNoSuchBucket) {
		t.Errorf("missing bucket get = %v", err)
	}
	if _, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "missing", Key: "x"}); !errors.Is(err, ErrNoSuchBucket) {
		t.Errorf("missing bucket head = %v", err)
	}
	if _, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "missing", Key: "x"}); !errors.Is(err, ErrNoSuchBucket) {
		t.Errorf("missing bucket delete = %v", err)
	}
	if _, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "missing"}); err != nil {
		t.Errorf("missing bucket list currently returns an empty page: %v", err)
	}

	bad := errorReader{}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "missing", Key: "x", Body: bad}); err == nil {
		t.Error("missing bucket should fail before reading")
	}
	_ = io.EOF
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func ptrTime() *time.Time                    { t := time.Unix(1700000000, 0).UTC(); return &t }

func TestLocalDeleteInvalidAndMissingObject(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "b", Key: "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "b", Key: "../bad"}); err == nil {
		t.Error("invalid delete key accepted")
	}
}

func TestLocalListDelimiterPrefixBranches(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/x/y", "b.txt"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Prefix: "a/", Delimiter: "/", MaxKeys: 1})
	if err != nil || len(out.CommonPrefixes) != 1 || out.CommonPrefixes[0].Prefix != "a/x/" {
		t.Fatalf("list = %+v, %v", out, err)
	}
}

func TestRemoteCreateBucketEmptyAndListError(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, http.Header{}, ""), nil })
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(400, http.Header{}, `<Error><Code>InvalidRequest</Code><Message>bad</Message></Error>`), nil
	})
	_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "InvalidRequest" {
		t.Fatalf("list error = %v", err)
	}
}

func TestRemoteDeleteNoDeleteMarker(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(204, http.Header{}, ""), nil })
	out, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: "x"})
	if err != nil || out.DeleteMarker != nil || out.VersionId != "" {
		t.Fatalf("delete = %+v, %v", out, err)
	}
}

func TestResponseMetadataInvalidValues(t *testing.T) {
	t.Parallel()
	m := responseMetadata(http.Header{"Content-Length": {"bad"}, "Last-Modified": {"bad"}, "Expires": {"bad"}, "X-Amz-Mp-Parts-Count": {"bad"}})
	if m.ContentLength != 0 || !m.LastModified.IsZero() || !m.Expires.IsZero() || m.PartsCount != nil {
		t.Fatalf("invalid metadata parsed: %+v", m)
	}
	if _, ok := any(io.EOF).(error); !ok {
		t.Fatal("unreachable")
	}
}

func TestRemoteListQueryEncodingAllOptions(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{}, `<ListBucketResult/>`), nil
	})
	_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b", ContinuationToken: "a+b", Delimiter: "/", EncodingType: "url", FetchOwner: true, MaxKeys: 5, Prefix: "a b/", StartAfter: "a"})
	if err != nil {
		t.Fatal(err)
	}
	q := got.URL.Query()
	for key, want := range map[string]string{"list-type": "2", "continuation-token": "a+b", "delimiter": "/", "encoding-type": "url", "fetch-owner": "true", "max-keys": "5", "prefix": "a b/", "start-after": "a"} {
		if q.Get(key) != want {
			t.Errorf("query %s = %q, want %q", key, q.Get(key), want)
		}
	}
}

func TestRemoteUploadHelpersAndEmptyHash(t *testing.T) {
	t.Parallel()
	if _, err := parseContentSHA256("0"); err == nil {
		t.Fatal("odd-length hash should fail")
	}
	if got := asReadCloser(strings.NewReader("x")); got == nil {
		t.Fatal("reader should be wrapped")
	}
	if got := asReadCloser(nil); got == nil {
		t.Fatal("nil reader should be wrapped")
	}
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Amz-Content-Sha256") != EmptySHA256 {
			t.Errorf("empty hash = %q", r.Header.Get("X-Amz-Content-Sha256"))
		}
		return response(200, http.Header{}, ""), nil
	})
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", ContentSHA256: EmptySHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", ContentSHA256: HashSHA256([]byte("x"))}); err == nil {
		t.Fatal("wrong empty hash should fail")
	}
}

func TestRemotePutObjectContentSHA256Streaming(t *testing.T) {
	t.Parallel()
	body := "streamed payload"
	hash := HashSHA256([]byte(body))
	var gotHash, gotBody string
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		data, err := io.ReadAll(r.Body)
		gotBody = string(data)
		return response(200, http.Header{}, ""), err
	})
	if _, err := c.PutObject(context.Background(), PutObjectInput{
		Bucket: "b", Key: "x", Body: strings.NewReader(body),
		ContentLength: -1, ContentSHA256: hash,
	}); err != nil {
		t.Fatal(err)
	}
	if gotHash != hash || gotBody != body {
		t.Fatalf("hash/body = %q/%q, want %q/%q", gotHash, gotBody, hash, body)
	}
}

func TestRemotePutObjectContentSHA256Errors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		input PutObjectInput
	}{
		{name: "invalid hash", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: -1, ContentSHA256: "bad"}},
		{name: "mismatch", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: -1, ContentSHA256: HashSHA256([]byte("y"))}},
		{name: "short body", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: 2, ContentSHA256: HashSHA256([]byte("x"))}},
		{name: "extra body", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("xy"), ContentLength: 1, ContentSHA256: HashSHA256([]byte("x"))}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				_, err := io.ReadAll(r.Body)
				return response(200, http.Header{}, ""), err
			})
			if _, err := c.PutObject(context.Background(), test.input); err == nil {
				t.Fatal("expected upload error")
			}
		})
	}
}

func TestRemotePutObjectUnknownLengthStreams(t *testing.T) {
	t.Parallel()
	var gotBody string
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Amz-Content-Sha256") != "UNSIGNED-PAYLOAD" {
			t.Errorf("payload hash = %q", r.Header.Get("X-Amz-Content-Sha256"))
		}
		data, err := io.ReadAll(r.Body)
		gotBody = string(data)
		return response(200, http.Header{}, ""), err
	})
	if _, err := c.PutObject(context.Background(), PutObjectInput{
		Bucket: "b", Key: "x", Body: strings.NewReader("unknown length"), ContentLength: -1,
	}); err != nil {
		t.Fatal(err)
	}
	if gotBody != "unknown length" {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestRemotePutLengthMismatchAndNilBody(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, http.Header{}, ""), nil })
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("long"), ContentLength: 2}); err == nil {
		t.Error("long body should fail")
	}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x"), ContentLength: 2}); err == nil {
		t.Error("short body should fail")
	}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: nil}); err != nil {
		t.Fatal(err)
	}
	_ = io.EOF
}

func TestRemotePutOutputAllMetadata(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, http.Header{
			"Etag": {"\"e\""}, "X-Amz-Expiration": {"exp"}, "X-Amz-Checksum-Crc32": {"c32"},
			"X-Amz-Checksum-Crc32c": {"c32c"}, "X-Amz-Checksum-Sha1": {"s1"}, "X-Amz-Checksum-Sha256": {"s256"},
			"X-Amz-Version-Id": {"v"}, "X-Amz-Server-Side-Encryption-Aws-Kms-Key-Id": {"kms"},
		}, ""), nil
	})
	out, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ETag != "\"e\"" || out.Expiration != "exp" || out.ChecksumCRC32 != "c32" || out.ChecksumCRC32C != "c32c" || out.ChecksumSHA1 != "s1" || out.ChecksumSHA256 != "s256" || out.VersionId != "v" || out.SSEKMSKeyId != "kms" {
		t.Fatalf("output = %+v", out)
	}
}

func TestCreateBucketXMLDoesEscapeLocation(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) { got = r; return response(200, http.Header{}, ""), nil })
	_, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b", CreateBucketConfiguration: &CreateBucketConfiguration{LocationConstraint: "a&b"}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(got.Body)
	var body struct {
		Location string `xml:"LocationConstraint"`
	}
	if err := xml.Unmarshal(data, &body); err != nil || body.Location != "a&b" {
		t.Fatalf("body = %q, parsed = %+v, err=%v", data, body, err)
	}
}

func TestEnsureBucketPropagatesLocation(t *testing.T) {
	t.Parallel()
	calls := 0
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(404, http.Header{}, `<Error><Code>NotFound</Code></Error>`), nil
		}
		return response(200, http.Header{"Location": {"/b"}}, ""), nil
	})
	out, err := c.EnsureBucket(context.Background(), EnsureBucketInput{Bucket: "b"})
	if err != nil || out.Location != "/b" || !out.Created {
		t.Fatalf("out = %+v, err=%v", out, err)
	}
}

func TestRemoteAllOperationResponseBodiesClose(t *testing.T) {
	t.Parallel()
	for _, call := range []func(*Client) error{
		func(c *Client) error {
			_, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x"})
			return err
		},
		func(c *Client) error {
			_, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: "x"})
			return err
		},
		func(c *Client) error {
			_, err := c.HeadBucket(context.Background(), HeadBucketInput{Bucket: "b"})
			return err
		},
	} {
		body := &trackingBody{Reader: strings.NewReader("")}
		c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: body}, nil
		})
		if err := call(c); err != nil {
			t.Fatal(err)
		}
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	}
}

func TestRemoteHeadObjectAllOptions(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(200, http.Header{"Content-Length": {"1"}, "Content-Type": {"text/plain"}}, ""), nil
	})
	_, err := c.HeadObject(context.Background(), HeadObjectInput{
		Bucket: "b", Key: "x", IfMatch: "m", IfNoneMatch: "n", IfModifiedSince: ptrTime(), IfUnmodifiedSince: ptrTime(),
		Range: "bytes=0-1", VersionId: "v", SSECustomerAlgorithm: "AES256", SSECustomerKey: "k", SSECustomerKeyMD5: "md5",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"If-Match": "m", "If-None-Match": "n", "Range": "bytes=0-1",
		"x-amz-server-side-encryption-customer-algorithm": "AES256",
		"x-amz-server-side-encryption-customer-key":       "k",
		"x-amz-server-side-encryption-customer-key-MD5":   "md5",
	} {
		if got.Header.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, got.Header.Get(key), want)
		}
	}
	if got.URL.Query().Get("versionId") != "v" {
		t.Error("versionId missing")
	}
}

func TestRemoteListReadAndXMLFailures(t *testing.T) {
	t.Parallel()
	readErr := errors.New("body read failed")
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: readErrorCloser{err: readErr}}, nil
	})
	_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
	if !errors.Is(err, readErr) {
		t.Fatalf("read error = %v", err)
	}

	c = remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(200, http.Header{}, "<bad"), nil })
	_, err = c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
	if err == nil {
		t.Fatal("malformed XML should fail")
	}
}

type readErrorCloser struct{ err error }

func (r readErrorCloser) Read([]byte) (int, error) { return 0, r.err }
func (r readErrorCloser) Close() error             { return nil }

func TestRemoteHeadNon404FallbackCode(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) { return response(403, http.Header{}, ""), nil })
	_, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: "x"})
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "Forbidden" {
		t.Fatalf("error = %+v", err)
	}
}
