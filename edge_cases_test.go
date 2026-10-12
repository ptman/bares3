package bares3

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"slices"
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
	// Keys in order: a/x, a/y, b, c/z. With Delimiter "/" the two "a/" keys
	// collapse into the common prefix "a/", leaving the order a/, b, c/.
	// Scoped to Prefix "a/" there is no remaining delimiter, so a/x and a/y
	// are returned as direct contents.
	for _, test := range []struct {
		name        string
		input       ListObjectsV2Input
		wantContent []string
		wantPrefix  []string
		wantTrunc   bool
	}{
		{name: "single page", input: ListObjectsV2Input{Bucket: "b", Delimiter: "/"}, wantContent: []string{"b"}, wantPrefix: []string{"a/", "c/"}},
		{name: "prefix page one", input: ListObjectsV2Input{Bucket: "b", Prefix: "a/", Delimiter: "/", MaxKeys: 1}, wantContent: []string{"a/x"}, wantTrunc: true},
		{name: "prefix complete", input: ListObjectsV2Input{Bucket: "b", Prefix: "a/", Delimiter: "/", MaxKeys: 2}, wantContent: []string{"a/x", "a/y"}},
		{name: "delimiter truncates at a prefix", input: ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: 1}, wantPrefix: []string{"a/"}, wantTrunc: true},
		{name: "start after", input: ListObjectsV2Input{Bucket: "b", StartAfter: "a/x"}, wantContent: []string{"a/y", "b", "c/z"}},
		{name: "continuation token", input: ListObjectsV2Input{Bucket: "b", ContinuationToken: "b"}, wantContent: []string{"c/z"}},
		{name: "max keys above count", input: ListObjectsV2Input{Bucket: "b", MaxKeys: 99}, wantContent: []string{"a/x", "a/y", "b", "c/z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			out, err := c.ListObjectsV2(ctx, test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := objectKeys(out.Contents); !slices.Equal(got, test.wantContent) {
				t.Errorf("Contents = %v, want %v", got, test.wantContent)
			}
			if got := prefixNames(out.CommonPrefixes); !slices.Equal(got, test.wantPrefix) {
				t.Errorf("CommonPrefixes = %v, want %v", got, test.wantPrefix)
			}
			if out.IsTruncated != test.wantTrunc {
				t.Errorf("IsTruncated = %v, want %v", out.IsTruncated, test.wantTrunc)
			}
			if out.KeyCount <= 0 {
				t.Errorf("KeyCount = %d, want at least 1", out.KeyCount)
			}
			if out.KeyCount != len(out.Contents)+
				len(out.CommonPrefixes) {
				t.Errorf("KeyCount = %d, want %d", out.KeyCount, len(out.Contents)+len(out.CommonPrefixes))
			}
		})
	}
}

// B2: objects and common prefixes share one key ordering, so a page must be the
// first MaxKeys entries of that order. Hoisting objects ahead of prefixes
// before truncation drops entries that belong on the page.
func TestLocalListPageKeepsPrefixesInKeyOrder(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a.txt", "d/x", "m.txt", "z/x"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x"), ContentLength: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Key order is a.txt, d/, m.txt, z/.
	for _, test := range []struct {
		maxKeys     int
		wantContent []string
		wantPrefix  []string
		wantTrunc   bool
	}{
		{maxKeys: 0, wantContent: []string{"a.txt", "m.txt"}, wantPrefix: []string{"d/", "z/"}},
		{maxKeys: 2, wantContent: []string{"a.txt"}, wantPrefix: []string{"d/"}, wantTrunc: true},
		{maxKeys: 3, wantContent: []string{"a.txt", "m.txt"}, wantPrefix: []string{"d/"}, wantTrunc: true},
		{maxKeys: 4, wantContent: []string{"a.txt", "m.txt"}, wantPrefix: []string{"d/", "z/"}},
	} {
		out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: test.maxKeys})
		if err != nil {
			t.Fatal(err)
		}
		if got := objectKeys(out.Contents); !slices.Equal(got, test.wantContent) {
			t.Errorf("MaxKeys=%d Contents = %v, want %v", test.maxKeys, got, test.wantContent)
		}
		if got := prefixNames(out.CommonPrefixes); !slices.Equal(got, test.wantPrefix) {
			t.Errorf("MaxKeys=%d CommonPrefixes = %v, want %v", test.maxKeys, got, test.wantPrefix)
		}
		if out.IsTruncated != test.wantTrunc {
			t.Errorf("MaxKeys=%d IsTruncated = %v, want %v", test.maxKeys, out.IsTruncated, test.wantTrunc)
		}
	}
}

// Paging with a delimiter must visit every key exactly once across the pages.
func TestLocalListPagesCoverEveryEntry(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	keys := []string{"a", "b/c", "b/d", "e", "f/g", "h", "i", "j", "k", "l", "m"}
	for _, key := range keys {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x"), ContentLength: 1}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]int{}
	after := ""
	for page := 0; page < len(keys)+2; page++ {
		out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: 2, StartAfter: after})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Contents) == 0 && len(out.CommonPrefixes) == 0 {
			break
		}
		var pageKeys []string
		for _, o := range out.Contents {
			pageKeys = append(pageKeys, o.Key)
		}
		for _, p := range out.CommonPrefixes {
			pageKeys = append(pageKeys, p.Prefix)
		}
		for _, k := range pageKeys {
			seen[k]++
		}
		after = pageKeys[len(pageKeys)-1]
	}

	// Every object and every derived prefix must appear on exactly one page.
	for _, key := range []string{"a", "e", "h", "i", "j", "k", "l", "m", "b/", "f/"} {
		if seen[key] != 1 {
			t.Errorf("%q appeared on %d pages, want exactly 1 (page contents: %v)", key, seen[key], seen)
		}
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("%q appeared on %d pages", k, n)
		}
	}
}

// P3: a truncated page must hand back a token that resumes after the last key
// on it, so the documented pagination loop works against the local backend.
func TestLocalListNextContinuationToken(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x"), ContentLength: 1}); err != nil {
			t.Fatal(err)
		}
	}

	// Follow the documented loop: send NextContinuationToken back verbatim.
	var paged []string
	token := ""
	for range 10 {
		out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", MaxKeys: 2, ContinuationToken: token})
		if err != nil {
			t.Fatal(err)
		}
		if out.ContinuationToken != token {
			t.Errorf("ContinuationToken = %q, want the token that was sent %q", out.ContinuationToken, token)
		}
		for _, o := range out.Contents {
			paged = append(paged, o.Key)
		}
		if !out.IsTruncated {
			if out.NextContinuationToken != "" {
				t.Errorf("NextContinuationToken = %q on a final page, want empty", out.NextContinuationToken)
			}
			break
		}
		if out.NextContinuationToken == "" {
			t.Fatal("truncated page returned an empty NextContinuationToken")
		}
		token = out.NextContinuationToken
	}

	want := []string{"a", "b", "c", "d", "e"}
	if !slices.Equal(paged, want) {
		t.Errorf("paged keys = %v, want %v", paged, want)
	}
}

// P4: listing a bucket that does not exist is an error, not an empty page.
func TestLocalListMissingBucketIsAnError(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	_, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "missing"})
	if !errors.Is(err, ErrNoSuchBucket) {
		t.Fatalf("error = %v, want ErrNoSuchBucket", err)
	}
	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.StatusCode != 404 {
		t.Fatalf("error = %+v, want status 404", err)
	}

	// Once the bucket exists, listing succeeds.
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "missing"}); err != nil {
		t.Fatalf("listing an empty bucket: %v", err)
	}
}

func objectKeys(objects []Object) []string {
	keys := make([]string, 0, len(objects))
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return keys
}

func prefixNames(prefixes []CommonPrefix) []string {
	names := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		names = append(names, p.Prefix)
	}
	return names
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
