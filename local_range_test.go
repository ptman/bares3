package bares3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestResolveByteRange(t *testing.T) {
	t.Parallel()
	const size = 11 // "hello world"
	tests := []struct {
		name      string
		spec      string
		size      int64
		wantStart int64
		wantEnd   int64
		wantOK    bool
		wantErr   error
	}{
		{name: "closed", spec: "bytes=0-4", size: size, wantStart: 0, wantEnd: 4, wantOK: true},
		{name: "open ended", spec: "bytes=6-", size: size, wantStart: 6, wantEnd: 10, wantOK: true},
		{name: "open ended from zero", spec: "bytes=0-", size: size, wantStart: 0, wantEnd: 10, wantOK: true},
		{name: "suffix", spec: "bytes=-5", size: size, wantStart: 6, wantEnd: 10, wantOK: true},
		{name: "suffix beyond object", spec: "bytes=-99", size: size, wantStart: 0, wantEnd: 10, wantOK: true},
		{name: "suffix equals object", spec: "bytes=-11", size: size, wantStart: 0, wantEnd: 10, wantOK: true},
		{name: "end clamped", spec: "bytes=8-999", size: size, wantStart: 8, wantEnd: 10, wantOK: true},
		{name: "single byte", spec: "bytes=0-0", size: size, wantStart: 0, wantEnd: 0, wantOK: true},
		{name: "surrounding space", spec: "  bytes = 1 - 3  ", size: size, wantStart: 1, wantEnd: 3, wantOK: true},
		{name: "uppercase unit", spec: "BYTES=1-3", size: size, wantStart: 1, wantEnd: 3, wantOK: true},

		{name: "unsatisfiable start past end", spec: "bytes=11-", size: size, wantErr: ErrInvalidRange},
		{name: "unsatisfiable start past end closed", spec: "bytes=11-20", size: size, wantErr: ErrInvalidRange},
		{name: "unsatisfiable reversed", spec: "bytes=5-2", size: size, wantErr: ErrInvalidRange},
		{name: "unsatisfiable zero suffix", spec: "bytes=-0", size: size, wantErr: ErrInvalidRange},
		{name: "unsatisfiable empty object", spec: "bytes=0-0", size: 0, wantErr: ErrInvalidRange},
		{name: "unsatisfiable empty object suffix", spec: "bytes=-1", size: 0, wantErr: ErrInvalidRange},
		{name: "unsatisfiable empty object open", spec: "bytes=0-", size: 0, wantErr: ErrInvalidRange},

		{name: "empty", spec: "", size: size},
		{name: "missing unit", spec: "0-4", size: size},
		{name: "wrong unit", spec: "items=0-4", size: size},
		{name: "multiple ranges", spec: "bytes=0-1,5-6", size: size},
		{name: "trailing comma", spec: "bytes=0-1,", size: size},
		{name: "missing dash", spec: "bytes=04", size: size},
		{name: "non numeric", spec: "bytes=abc", size: size},
		{name: "non numeric end", spec: "bytes=0-xyz", size: size},
		{name: "non numeric start with end", spec: "bytes=abc-3", size: size},
		{name: "non numeric suffix", spec: "bytes=-xyz", size: size},
		{name: "negative start", spec: "bytes=-5-9", size: size},
		{name: "negative end", spec: "bytes=0--3", size: size},
		{name: "negative open ended", spec: "bytes=--3", size: size},
		{name: "overflow", spec: "bytes=0-99999999999999999999", size: size},
		{name: "overflow start", spec: "bytes=99999999999999999999-", size: size},
		{name: "overflow suffix", spec: "bytes=-99999999999999999999", size: size},
		{name: "empty specs", spec: "bytes=", size: size},
		{name: "empty suffix", spec: "bytes=-", size: size},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			start, end, ok, err := resolveByteRange(test.spec, test.size)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("err = %v, want %v", err, test.wantErr)
				}
				if ok {
					t.Fatal("unsatisfiable range reported as satisfiable")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}
			if !test.wantOK {
				return
			}
			if start != test.wantStart || end != test.wantEnd {
				t.Fatalf("range = %d-%d, want %d-%d", start, end, test.wantStart, test.wantEnd)
			}
			if end < start || end >= test.size {
				t.Fatalf("range %d-%d outside object of size %d", start, end, test.size)
			}
		})
	}
}

func TestFormatContentRange(t *testing.T) {
	t.Parallel()
	if got := formatContentRange(0, 9, 443); got != "bytes 0-9/443" {
		t.Fatalf("formatContentRange = %q", got)
	}
}

func newRangeClient(t *testing.T, body string) (*Client, string) {
	t.Helper()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "k", Body: strings.NewReader(body), ContentLength: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	return c, body
}

func TestLocalGetObjectRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	content := "hello world"
	c, _ := newRangeClient(t, content)

	tests := []struct {
		name       string
		spec       string
		wantBody   string
		wantLength int64
		wantRange  string
	}{
		{name: "closed", spec: "bytes=0-4", wantBody: "hello", wantLength: 5, wantRange: "bytes 0-4/11"},
		{name: "open ended", spec: "bytes=6-", wantBody: "world", wantLength: 5, wantRange: "bytes 6-10/11"},
		{name: "suffix", spec: "bytes=-5", wantBody: "world", wantLength: 5, wantRange: "bytes 6-10/11"},
		{name: "clamped", spec: "bytes=6-100", wantBody: "world", wantLength: 5, wantRange: "bytes 6-10/11"},
		{name: "unparsable serves whole object", spec: "bytes=abc", wantBody: content, wantLength: 11},
		{name: "multiple ranges serve whole object", spec: "bytes=0-1,5-6", wantBody: content, wantLength: 11},
		{name: "wrong unit serves whole object", spec: "items=0-1", wantBody: content, wantLength: 11},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: test.spec})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = out.Body.Close() }()

			if out.ContentLength != test.wantLength {
				t.Errorf("ContentLength = %d, want %d", out.ContentLength, test.wantLength)
			}
			if out.ContentRange != test.wantRange {
				t.Errorf("ContentRange = %q, want %q", out.ContentRange, test.wantRange)
			}
			if out.AcceptRanges != "bytes" {
				t.Errorf("AcceptRanges = %q, want %q", out.AcceptRanges, "bytes")
			}
			got, err := io.ReadAll(out.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.wantBody {
				t.Errorf("body = %q, want %q", got, test.wantBody)
			}
			if int64(len(got)) != out.ContentLength {
				t.Errorf("read %d bytes, ContentLength %d", len(got), out.ContentLength)
			}
		})
	}
}

func TestLocalGetObjectRangeKeepsObjectMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, content := newRangeClient(t, "hello world")

	full, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_ = full.Body.Close()

	part, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: "bytes=6-"})
	if err != nil {
		t.Fatal(err)
	}
	_ = part.Body.Close()

	if part.ETag != full.ETag {
		t.Errorf("ETag = %q, want %q", part.ETag, full.ETag)
	}
	if part.ContentType != full.ContentType {
		t.Errorf("ContentType = %q, want %q", part.ContentType, full.ContentType)
	}
	if !part.LastModified.Equal(full.LastModified) {
		t.Errorf("LastModified = %v, want %v", part.LastModified, full.LastModified)
	}
	if part.ContentLength == full.ContentLength {
		t.Error("ranged ContentLength should differ from full object length")
	}
	if int64(len(content)) != full.ContentLength {
		t.Errorf("full ContentLength = %d, want %d", full.ContentLength, len(content))
	}
}

func TestLocalGetObjectRangeUnsatisfiable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	for _, spec := range []string{"bytes=11-", "bytes=5-2", "bytes=-0", "bytes=999-"} {
		_, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: spec})
		if err == nil {
			t.Fatalf("Range %q unexpectedly succeeded", spec)
		}
		if !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("Range %q error = %v, want ErrInvalidRange", spec, err)
		}
		var s3Err *S3Error
		if !errors.As(err, &s3Err) || s3Err.StatusCode != 416 {
			t.Fatalf("Range %q error = %+v, want status 416", spec, err)
		}
	}
}

func TestLocalGetObjectRangeEmptyObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "k", ContentLength: 0}); err != nil {
		t.Fatal(err)
	}

	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_ = out.Body.Close()
	if out.ContentLength != 0 || out.ContentRange != "" {
		t.Errorf("empty object = length %d, range %q", out.ContentLength, out.ContentRange)
	}

	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: "bytes=0-0"}); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("range on empty object error = %v, want ErrInvalidRange", err)
	}
}

func TestLocalRangedBodyClosesUnderlyingFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: "bytes=0-4"})
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Body.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := io.ReadAll(out.Body); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after Close error = %v, want os.ErrClosed", err)
	}
}

func TestLocalRangeErrorPathsReleaseFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	// Repeated failures must not leak handles; a leak would eventually exhaust
	// the descriptor limit and fail the final successful read.
	for i := 0; i < 512; i++ {
		if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: "bytes=99-"}); !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("iteration %d error = %v, want ErrInvalidRange", i, err)
		}
		if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", IfNoneMatch: "*"}); !errors.Is(err, ErrNotModified) {
			t.Fatalf("iteration %d error = %v, want ErrNotModified", i, err)
		}
	}

	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "k", Range: "bytes=0-4"})
	if err != nil {
		t.Fatalf("final GetObject: %v", err)
	}
	got, err := io.ReadAll(out.Body)
	_ = out.Body.Close()
	if err != nil || string(got) != "hello" {
		t.Fatalf("final read = %q, %v", got, err)
	}
}

func TestLocalHeadObjectRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	head, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentLength != 11 || head.ContentRange != "" {
		t.Errorf("head = length %d, range %q", head.ContentLength, head.ContentRange)
	}
	if head.AcceptRanges != "bytes" {
		t.Errorf("AcceptRanges = %q, want %q", head.AcceptRanges, "bytes")
	}

	head, err = c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k", Range: "bytes=0-4"})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentLength != 5 {
		t.Errorf("ranged ContentLength = %d, want 5", head.ContentLength)
	}
	if head.ContentRange != "bytes 0-4/11" {
		t.Errorf("ranged ContentRange = %q, want %q", head.ContentRange, "bytes 0-4/11")
	}

	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k", Range: "bytes=99-"}); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("unsatisfiable head error = %v, want ErrInvalidRange", err)
	}
}

func TestLocalGetObjectPreconditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	head, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	etag := head.ETag
	modified := head.LastModified

	past := modified.Add(-time.Hour)
	future := modified.Add(time.Hour)

	tests := []struct {
		name    string
		input   GetObjectInput
		wantErr error
	}{
		{name: "if-match hit", input: GetObjectInput{IfMatch: etag}},
		{name: "if-match miss", input: GetObjectInput{IfMatch: `"0000"`}, wantErr: ErrPreconditionFailed},
		{name: "if-match wildcard", input: GetObjectInput{IfMatch: "*"}},
		{name: "if-match list hit", input: GetObjectInput{IfMatch: `"0000", ` + etag}},
		{name: "if-match list miss", input: GetObjectInput{IfMatch: `"0000", "1111"`}, wantErr: ErrPreconditionFailed},
		{name: "if-none-match miss", input: GetObjectInput{IfNoneMatch: `"0000"`}},
		{name: "if-none-match hit", input: GetObjectInput{IfNoneMatch: etag}, wantErr: ErrNotModified},
		{name: "if-none-match wildcard", input: GetObjectInput{IfNoneMatch: "*"}, wantErr: ErrNotModified},
		{name: "if-modified-since old", input: GetObjectInput{IfModifiedSince: &past}},
		{name: "if-modified-since same second", input: GetObjectInput{IfModifiedSince: &modified}, wantErr: ErrNotModified},
		{name: "if-modified-since future", input: GetObjectInput{IfModifiedSince: &future}, wantErr: ErrNotModified},
		{name: "if-unmodified-since future", input: GetObjectInput{IfUnmodifiedSince: &future}},
		{name: "if-unmodified-since same second", input: GetObjectInput{IfUnmodifiedSince: &modified}},
		{name: "if-unmodified-since old", input: GetObjectInput{IfUnmodifiedSince: &past}, wantErr: ErrPreconditionFailed},

		// Precedence documented by S3: If-Match overrides If-Unmodified-Since.
		{name: "if-match overrides if-unmodified-since", input: GetObjectInput{IfMatch: etag, IfUnmodifiedSince: &past}},
		{name: "if-match failure overrides if-unmodified-since", input: GetObjectInput{IfMatch: `"0000"`, IfUnmodifiedSince: &future}, wantErr: ErrPreconditionFailed},
		// If-None-Match overrides If-Modified-Since.
		{name: "if-none-match overrides if-modified-since", input: GetObjectInput{IfNoneMatch: `"0000"`, IfModifiedSince: &past}},
		{name: "if-none-match failure overrides if-modified-since", input: GetObjectInput{IfNoneMatch: etag, IfModifiedSince: &future}, wantErr: ErrNotModified},
		// Preconditions short-circuit before the range is applied.
		{name: "not modified precedes range", input: GetObjectInput{IfNoneMatch: "*", Range: "bytes=0-4"}, wantErr: ErrNotModified},
		{name: "precondition failure precedes range", input: GetObjectInput{IfMatch: `"0000"`, Range: "bytes=0-4"}, wantErr: ErrPreconditionFailed},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := test.input
			input.Bucket = "b"
			input.Key = "k"
			out, err := c.GetObject(ctx, input)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer func() { _ = out.Body.Close() }()
			got, err := io.ReadAll(out.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "hello world" {
				t.Errorf("body = %q, want whole object", got)
			}
		})
	}
}

func TestLocalHeadObjectPreconditions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	head, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k", IfMatch: `"0000"`}); !errors.Is(err, ErrPreconditionFailed) {
		t.Errorf("If-Match error = %v, want ErrPreconditionFailed", err)
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k", IfNoneMatch: head.ETag}); !errors.Is(err, ErrNotModified) {
		t.Errorf("If-None-Match error = %v, want ErrNotModified", err)
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "k", IfMatch: head.ETag, Range: "bytes=0-4"}); err != nil {
		t.Errorf("matched precondition with range: %v", err)
	}
}

func TestLocalPreconditionStatusCodes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	cases := []struct {
		input  GetObjectInput
		status int
		err    error
	}{
		{input: GetObjectInput{IfMatch: `"0000"`}, status: 412, err: ErrPreconditionFailed},
		{input: GetObjectInput{IfNoneMatch: "*"}, status: 304, err: ErrNotModified},
		{input: GetObjectInput{Range: "bytes=99-"}, status: 416, err: ErrInvalidRange},
	}
	for _, test := range cases {
		input := test.input
		input.Bucket = "b"
		input.Key = "k"
		_, err := c.GetObject(ctx, input)
		var s3Err *S3Error
		if !errors.As(err, &s3Err) || s3Err.StatusCode != test.status || !errors.Is(err, test.err) {
			t.Errorf("%+v error = %+v, want status %d matching %v", input, err, test.status, test.err)
		}
	}
}

func TestLocalPreconditionsIgnoreMissingObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}

	// S3 answers conditional reads against a missing object with NoSuchKey,
	// not with a precondition failure.
	for _, input := range []GetObjectInput{
		{Bucket: "b", Key: "missing", IfMatch: "*"},
		{Bucket: "b", Key: "missing", IfNoneMatch: "*"},
		{Bucket: "b", Key: "missing", IfMatch: `"0000"`},
	} {
		if _, err := c.GetObject(ctx, input); !errors.Is(err, ErrNoSuchKey) {
			t.Errorf("%+v error = %v, want ErrNoSuchKey", input, err)
		}
	}
}

func TestLocalPreconditionSubSecondTimestamps(t *testing.T) {
	t.Parallel()
	// Filesystem timestamps carry nanoseconds while HTTP dates do not, so a
	// conditional read dated to the same second must still match.
	modified := time.Date(2020, 1, 2, 3, 4, 5, 500_000_000, time.UTC)
	sameSecond := modified.Truncate(time.Second)
	previousSecond := sameSecond.Add(-time.Second)

	if err := checkLocalPreconditions(`"etag"`, modified, "", "", &sameSecond, nil); !errors.Is(err, ErrNotModified) {
		t.Errorf("If-Modified-Since within the same second error = %v, want ErrNotModified", err)
	}
	if err := checkLocalPreconditions(`"etag"`, modified, "", "", nil, &sameSecond); err != nil {
		t.Errorf("If-Unmodified-Since within the same second error = %v, want nil", err)
	}
	if err := checkLocalPreconditions(`"etag"`, modified, "", "", &previousSecond, nil); err != nil {
		t.Errorf("If-Modified-Since before the object error = %v, want nil", err)
	}
	if err := checkLocalPreconditions(`"etag"`, modified, "", "", nil, &previousSecond); !errors.Is(err, ErrPreconditionFailed) {
		t.Errorf("If-Unmodified-Since before the object error = %v, want ErrPreconditionFailed", err)
	}
}

func TestEtagConditionMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		condition string
		etag      string
		want      bool
	}{
		{condition: "*", etag: `"a"`, want: true},
		{condition: `"a"`, etag: `"a"`, want: true},
		{condition: `"a"`, etag: `"b"`, want: false},
		{condition: `"b", "a"`, etag: `"a"`, want: true},
		{condition: `"b",  "c"`, etag: `"a"`, want: false},
		{condition: "", etag: `"a"`, want: false},
	}
	for _, test := range tests {
		if got := etagConditionMatches(test.condition, test.etag); got != test.want {
			t.Errorf("etagConditionMatches(%q, %q) = %v, want %v", test.condition, test.etag, got, test.want)
		}
	}
}

func TestLocalRangeAndPreconditionsAreIndependent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	// Sequential ranged reads must each start at the requested offset rather
	// than continuing from the previous position on the same file.
	prefix := "prefix:"
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "p", Body: strings.NewReader(prefix + "abcdefghij"), ContentLength: int64(len(prefix) + 10)}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ spec, want string }{
		{spec: "bytes=7-8", want: "ab"},
		{spec: "bytes=9-10", want: "cd"},
		{spec: "bytes=7-", want: "abcdefghij"},
		{spec: "bytes=-2", want: "ij"},
	} {
		out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "p", Range: test.spec})
		if err != nil {
			t.Fatalf("%s: %v", test.spec, err)
		}
		got, err := io.ReadAll(out.Body)
		_ = out.Body.Close()
		if err != nil {
			t.Fatalf("%s: %v", test.spec, err)
		}
		if string(got) != test.want {
			t.Errorf("%s body = %q, want %q", test.spec, got, test.want)
		}
	}
}

func TestLocalRangedReadRejectsInvalidKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	for _, key := range []string{"../outside", "", "a\x00b"} {
		if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: key, Range: "bytes=0-1"}); err == nil {
			t.Errorf("GetObject accepted invalid key %q with a range", key)
		}
		if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: key, Range: "bytes=0-1"}); err == nil {
			t.Errorf("HeadObject accepted invalid key %q with a range", key)
		}
	}
}

func TestLocalRangedGetRejectsMissingBucket(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")
	c.SetBucket("b")

	if _, err := c.GetObject(ctx, GetObjectInput{Key: "k", Range: "bytes=0-1"}); err != nil {
		t.Fatalf("default bucket range read: %v", err)
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Key: "k", Range: "bytes=0-1"}); err != nil {
		t.Fatalf("default bucket range head: %v", err)
	}
}

func TestRemoteConditionalAndRangeStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    error
		wantCode   string
		wantStatus int
	}{
		{
			name: "not modified get", status: 304, wantErr: ErrNotModified, wantCode: "NotModified", wantStatus: 304,
		},
		{
			name: "not modified head", status: 304, wantErr: ErrNotModified, wantCode: "NotModified", wantStatus: 304,
		},
		{
			name: "head precondition failed", status: 412, wantErr: ErrPreconditionFailed, wantCode: "PreconditionFailed", wantStatus: 412,
		},
		{
			name: "head invalid range", status: 416, wantErr: ErrInvalidRange, wantCode: "InvalidRange", wantStatus: 416,
		},
		{
			name: "head not found", status: 404, wantCode: "NotFound", wantStatus: 404,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				return response(test.status, http.Header{}, test.body), nil
			})
			ctx := context.Background()
			_, getErr := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x", IfNoneMatch: "*"})
			_, headErr := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "x", IfNoneMatch: "*"})
			for _, err := range []error{getErr, headErr} {
				if test.wantErr != nil && !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				var s3Err *S3Error
				if !errors.As(err, &s3Err) || s3Err.Code != test.wantCode || s3Err.StatusCode != test.wantStatus {
					t.Fatalf("error = %+v, want code %q status %d", err, test.wantCode, test.wantStatus)
				}
			}
		})
	}
}

func TestRemoteGetNotModifiedClosesBody(t *testing.T) {
	t.Parallel()
	body := &trackingBody{Reader: strings.NewReader("")}
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusNotModified, Status: "304 Not Modified", Header: http.Header{}, Body: body}, nil
	})
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x", IfNoneMatch: "*"}); !errors.Is(err, ErrNotModified) {
		t.Fatalf("error = %v, want ErrNotModified", err)
	}
	if !body.closed {
		t.Fatal("304 response body was not closed")
	}
}

func TestRemoteHeadParsesContentRange(t *testing.T) {
	t.Parallel()
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(206, http.Header{
			"Accept-Ranges":  {"bytes"},
			"Content-Range":  {"bytes 0-9/443"},
			"Content-Length": {"10"},
			"ETag":           {`"e"`},
		}, "0123456789"), nil
	})
	head, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: "x", Range: "bytes=0-9"})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentRange != "bytes 0-9/443" {
		t.Errorf("ContentRange = %q, want %q", head.ContentRange, "bytes 0-9/443")
	}
	if head.ContentLength != 10 {
		t.Errorf("ContentLength = %d, want 10", head.ContentLength)
	}
	if head.AcceptRanges != "bytes" {
		t.Errorf("AcceptRanges = %q, want %q", head.AcceptRanges, "bytes")
	}
}

func TestStatusOnlyError(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]string{
		304: "NotModified",
		404: "NotFound",
		412: "PreconditionFailed",
		416: "InvalidRange",
	} {
		err := statusOnlyError(status)
		if err == nil || err.Code != want || err.StatusCode != status {
			t.Errorf("statusOnlyError(%d) = %+v, want code %q status %d", status, err, want, status)
		}
	}
	if err := statusOnlyError(500); err != nil {
		t.Errorf("statusOnlyError(500) = %+v, want nil", err)
	}
}

func TestLocalRangedReadLargeObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _ := newRangeClient(t, "hello world")

	want := bytes.Repeat([]byte("abcdefghij"), 5000)
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "big", Body: bytes.NewReader(want), ContentLength: int64(len(want))}); err != nil {
		t.Fatal(err)
	}
	out, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "big", Range: fmt.Sprintf("bytes=%d-%d", 12345, 45678)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Body.Close() }()
	got, err := io.ReadAll(out.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want[12345:45679]) {
		t.Error("ranged body does not match the requested slice")
	}
	if out.ContentRange != fmt.Sprintf("bytes 12345-45678/%d", len(want)) {
		t.Errorf("ContentRange = %q", out.ContentRange)
	}
	if out.ContentLength != int64(len(want[12345:45679])) {
		t.Errorf("ContentLength = %d, want %d", out.ContentLength, len(want[12345:45679]))
	}
}

// The package sentinels exist so callers can match remote errors without
// depending on an exact message. S3Error.Is compares codes, so a provider's
// own AccessDenied response matches the sentinel even though the library never
// constructs it.
func TestSentinelsMatchProviderErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		code     string
		sentinal error
	}{
		{"AccessDenied", ErrAccessDenied},
		{"NoSuchKey", ErrNoSuchKey},
		{"NoSuchBucket", ErrNoSuchBucket},
		{"InvalidRange", ErrInvalidRange},
		{"PreconditionFailed", ErrPreconditionFailed},
		{"NotModified", ErrNotModified},
	} {
		t.Run(test.code, func(t *testing.T) {
			t.Parallel()
			c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
				return response(400, http.Header{}, `<Error><Code>`+test.code+`</Code><Message>provider wording</Message></Error>`), nil
			})
			_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"})
			if !errors.Is(err, test.sentinal) {
				t.Fatalf("provider error %q does not match %v", test.code, test.sentinal)
			}
			var s3Err *S3Error
			if !errors.As(err, &s3Err) || s3Err.Message != "provider wording" {
				t.Errorf("provider message was not preserved: %+v", err)
			}
		})
	}

	// A code with no sentinel must not match any of them.
	c := remoteTestClient(t, func(*http.Request) (*http.Response, error) {
		return response(400, http.Header{}, `<Error><Code>SomeOtherFailure</Code><Message>m</Message></Error>`), nil
	})
	_, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"})
	for _, sentinal := range []error{ErrAccessDenied, ErrNoSuchKey, ErrInvalidRange, ErrNotModified} {
		if errors.Is(err, sentinal) {
			t.Errorf("unrelated error matched %v", sentinal)
		}
	}
}
