package bares3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// B1: a nil Body with a non-zero ContentLength must not silently upload an
// empty object. Both backends have to agree on the rejection.
func TestPutObjectNilBodyWithNonZeroLengthIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	requests := 0
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		requests++
		var body []byte
		var err error
		if r.Body != nil {
			body, err = io.ReadAll(r.Body)
		}
		if err != nil {
			return nil, err
		}
		t.Errorf("request was sent with ContentLength=%d body=%q", r.ContentLength, body)
		return response(200, http.Header{}, ""), nil
	})

	_, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", ContentLength: 5})
	if err == nil {
		t.Fatal("PutObject accepted a nil body with a non-zero content length")
	}
	if err.Error() != "content length is non-zero but body is nil" {
		t.Errorf("error = %q", err.Error())
	}
	if requests != 0 {
		t.Errorf("PutObject sent %d requests before failing", requests)
	}

	local := newLocalClient(t)
	if _, err := local.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := local.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", ContentLength: 5}); err == nil {
		t.Fatal("local PutObject accepted a nil body with a non-zero content length")
	}
}

// The empty-upload forms must keep working: a nil body and an explicit zero
// length both upload nothing, and a supplied ContentSHA256 for an empty object
// is only valid when it is the empty-payload hash.
func TestPutObjectEmptyUploadForms(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, test := range []struct {
		name  string
		input PutObjectInput
		hash  string
	}{
		{name: "nil body zero length", input: PutObjectInput{Bucket: "b", Key: "x"}, hash: EmptySHA256},
		{name: "nil body explicit zero length", input: PutObjectInput{Bucket: "b", Key: "x", ContentLength: 0}, hash: EmptySHA256},
		{name: "body with zero length", input: PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("ignored"), ContentLength: 0}, hash: EmptySHA256},
		{name: "nil body empty hash", input: PutObjectInput{Bucket: "b", Key: "x", ContentSHA256: EmptySHA256}, hash: EmptySHA256},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var gotHash string
			var gotLength int64
			var gotBody string
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				gotHash = r.Header.Get("X-Amz-Content-Sha256")
				gotLength = r.ContentLength
				body, err := io.ReadAll(r.Body)
				gotBody = string(body)
				if err != nil {
					return nil, err
				}
				return response(200, http.Header{"Etag": {`"e"`}}, ""), nil
			})

			out, err := c.PutObject(ctx, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if out.ETag != `"e"` {
				t.Errorf("ETag = %q", out.ETag)
			}
			if gotHash != test.hash {
				t.Errorf("payload hash = %q, want %q", gotHash, test.hash)
			}
			if gotLength != 0 {
				t.Errorf("ContentLength = %d, want 0", gotLength)
			}
			if gotBody != "" {
				t.Errorf("body = %q, want empty", gotBody)
			}
		})
	}
}

// A nil body combined with a hash for non-empty content must be rejected
// rather than sending the empty-payload hash.
func TestPutObjectNilBodyRejectsNonEmptyHash(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Error("request was sent for an invalid input")
		return response(200, http.Header{}, ""), nil
	})
	for _, input := range []PutObjectInput{
		{Bucket: "b", Key: "x", ContentSHA256: HashSHA256([]byte("x"))},
		{Bucket: "b", Key: "x", Body: strings.NewReader(""), ContentLength: 0, ContentSHA256: HashSHA256([]byte("x"))},
	} {
		if _, err := c.PutObject(context.Background(), input); err == nil {
			t.Errorf("PutObject(%+v) accepted a hash that does not describe an empty body", input)
		}
	}
}

// T3: the declared length must reach the wire on every framing path.
func TestPutObjectRequestContentLength(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	payload := "hello world"

	// A negative input length means unknown-length streaming, which net/http
	// expresses as a zero ContentLength on a non-nil Body and then frames with
	// chunked transfer encoding.
	for _, test := range []struct {
		name       string
		input      PutObjectInput
		wantLength int64
		wantBody   string
		wantHash   string
	}{
		{
			name:       "exact length",
			input:      PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader(payload), ContentLength: int64(len(payload))},
			wantLength: int64(len(payload)),
			wantBody:   payload,
			wantHash:   HashSHA256([]byte(payload)),
		},
		{
			name:       "exact length with caller hash",
			input:      PutObjectInput{Bucket: "b", Key: "x", Body: nonSeeker{strings.NewReader(payload)}, ContentLength: int64(len(payload)), ContentSHA256: HashSHA256([]byte(payload))},
			wantLength: int64(len(payload)),
			wantBody:   payload,
			wantHash:   HashSHA256([]byte(payload)),
		},
		{
			name:       "unknown length streams",
			input:      PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader(payload), ContentLength: -1},
			wantLength: 0,
			wantBody:   payload,
			wantHash:   "UNSIGNED-PAYLOAD",
		},
		{
			name:       "unknown length with caller hash",
			input:      PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader(payload), ContentLength: -1, ContentSHA256: HashSHA256([]byte(payload))},
			wantLength: 0,
			wantBody:   payload,
			wantHash:   HashSHA256([]byte(payload)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var gotLength int64
			var gotHash, gotBody string
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				gotLength = r.ContentLength
				gotHash = r.Header.Get("X-Amz-Content-Sha256")
				body, err := io.ReadAll(r.Body)
				gotBody = string(body)
				if err != nil {
					return nil, err
				}
				return response(200, http.Header{}, ""), nil
			})
			if _, err := c.PutObject(ctx, test.input); err != nil {
				t.Fatal(err)
			}
			if gotLength != test.wantLength {
				t.Errorf("ContentLength = %d, want %d", gotLength, test.wantLength)
			}
			if gotHash != test.wantHash {
				t.Errorf("payload hash = %q, want %q", gotHash, test.wantHash)
			}
			if gotBody != test.wantBody {
				t.Errorf("body = %q, want %q", gotBody, test.wantBody)
			}
		})
	}
}

// nonSeeker hides the io.Seeker implementation of the wrapped reader so the
// streaming path is exercised instead of the whole-body pre-hash path.
type nonSeeker struct{ r io.Reader }

func (r nonSeeker) Read(p []byte) (int, error) { return r.r.Read(p) }

// T2: an upload whose declared length is met but whose SHA-256 does not match
// must fail, and the request must have signed the caller's hash rather than a
// locally recomputed one.
func TestPutObjectSizedUploadVerifiesCallerHash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	payload := "hello world"
	wantHash := HashSHA256([]byte(payload))
	wrongHash := HashSHA256([]byte("goodbye world"))

	for _, test := range []struct {
		name string
		body io.Reader
	}{
		{name: "seeker", body: strings.NewReader(payload)},
		{name: "non seeker", body: nonSeeker{strings.NewReader(payload)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// The body must be drained, otherwise uploadReadCloser never reaches
			// its trailing hash verification.
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				if _, err := io.ReadAll(r.Body); err != nil {
					return nil, err
				}
				return response(200, http.Header{}, ""), nil
			})
			_, err := c.PutObject(ctx, PutObjectInput{
				Bucket: "b", Key: "x", Body: test.body, ContentLength: int64(len(payload)), ContentSHA256: wrongHash,
			})
			if err == nil {
				t.Fatal("PutObject accepted a body that does not match ContentSHA256")
			}
			if !strings.Contains(err.Error(), "ContentSHA256") && !strings.Contains(err.Error(), "content SHA-256") {
				t.Errorf("error = %q, want a hash mismatch", err.Error())
			}
		})
	}

	// A matching hash must be accepted and must be the value that is signed.
	t.Run("matching hash is signed", func(t *testing.T) {
		t.Parallel()
		for _, body := range []io.Reader{strings.NewReader(payload), nonSeeker{strings.NewReader(payload)}} {
			var gotHash string
			c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
				gotHash = r.Header.Get("X-Amz-Content-Sha256")
				if _, err := io.ReadAll(r.Body); err != nil {
					return nil, err
				}
				return response(200, http.Header{}, ""), nil
			})
			if _, err := c.PutObject(context.Background(), PutObjectInput{
				Bucket: "b", Key: "x", Body: body, ContentLength: int64(len(payload)), ContentSHA256: wantHash,
			}); err != nil {
				t.Fatal(err)
			}
			if gotHash != wantHash {
				t.Errorf("signed payload hash = %q, want the caller hash %q", gotHash, wantHash)
			}
		}
	})
}

// A ReadSeeker body that is not positioned at the start must upload only the
// declared suffix of its content.
func TestPutObjectSeekerBodyAtOffset(t *testing.T) {
	t.Parallel()
	payload := "hello world"
	seeker := strings.NewReader(payload)
	if _, err := seeker.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	var gotBody string
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		gotBody = string(body)
		if err != nil {
			return nil, err
		}
		return response(200, http.Header{}, ""), nil
	})
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "x", Body: seeker, ContentLength: 5}); err != nil {
		t.Fatal(err)
	}
	if gotBody != "world" {
		t.Errorf("body = %q, want %q", gotBody, "world")
	}
}

// P5: an endpoint must not be able to make the client buffer without bound.
func TestResponseBodiesAreBounded(t *testing.T) {
	t.Parallel()

	t.Run("oversized listing is rejected", func(t *testing.T) {
		t.Parallel()
		c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
			// One byte past the cap, so the overflow is detected rather than
			// silently truncated into an XML parse failure.
			huge := strings.Repeat("x", maxListBodyBytes+1)
			return response(200, http.Header{}, "<ListBucketResult>"+huge), nil
		})
		_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
		if err == nil {
			t.Fatal("oversized listing response was accepted")
		}
		if !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("error = %q, want an actionable size error", err.Error())
		}
	})

	t.Run("oversized error document does not hang", func(t *testing.T) {
		t.Parallel()
		// An unterminated element past the cap: the decoder must stop at the
		// limit instead of consuming the stream.
		c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
			huge := "<Error><Code>SlowDown</Code><Message>" + strings.Repeat("x", maxErrorBodyBytes+1)
			return response(503, http.Header{}, huge), nil
		})
		_, err := c.ListObjectsV2(context.Background(), ListObjectsV2Input{Bucket: "b"})
		if err == nil {
			t.Fatal("oversized error document was accepted")
		}
		var s3Err *S3Error
		if !errors.As(err, &s3Err) || s3Err.StatusCode != 503 {
			t.Fatalf("error = %+v, want a 503 S3Error", err)
		}
	})
}

// B4: the default client must not carry a whole-transfer timeout, which would
// abort any object that takes longer to move than the budget allows.
func TestDefaultHTTPClientHasNoTransferTimeout(t *testing.T) {
	t.Parallel()
	c := defaultHTTPClient()
	if c.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 so long transfers are not aborted", c.Timeout)
	}
	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", c.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Error("ResponseHeaderTimeout is unset, so a stalled endpoint hangs forever")
	}
}

// The caller's context, not the client, bounds a request.
func TestClientHonorsContextDeadline(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// fakeSeeker emulates a 3-byte seekable source that fails on the nth Seek
// call (1-based) or on Read, to pin the sized-upload pre-hash error paths.
type fakeSeeker struct {
	data    string
	pos     int64
	failAt  int
	seekErr error
	readErr error
	calls   int
}

func (f *fakeSeeker) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	n := copy(p, f.data[f.pos:])
	f.pos += int64(n)
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (f *fakeSeeker) Seek(offset int64, whence int) (int64, error) {
	f.calls++
	if f.calls == f.failAt {
		return 0, f.seekErr
	}
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos += offset
	case io.SeekEnd:
		f.pos = int64(len(f.data)) + offset
	}
	return f.pos, nil
}

func TestPutObjectSeekerErrorPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	seekErr := errors.New("seek failed")
	readErr := errors.New("hash read failed")
	for _, test := range []struct {
		name    string
		seeker  *fakeSeeker
		wantErr error
	}{
		{"first seek", &fakeSeeker{data: "abc", failAt: 1, seekErr: seekErr}, seekErr},
		{"second seek", &fakeSeeker{data: "abc", failAt: 2, seekErr: seekErr}, seekErr},
		{"third seek", &fakeSeeker{data: "abc", failAt: 3, seekErr: seekErr}, seekErr},
		{"seek after hash", &fakeSeeker{data: "abc", failAt: 4, seekErr: seekErr}, seekErr},
		{"read during hash", &fakeSeeker{data: "abc", readErr: readErr}, readErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				t.Error("a request was sent despite a seeker failure")
				return response(200, http.Header{}, ""), nil
			})
			_, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: test.seeker, ContentLength: 3})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("err = %v, want %v", err, test.wantErr)
			}
		})
	}
}
