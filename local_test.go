package bares3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newLocalClient(t *testing.T) *Client {
	t.Helper()
	dir := t.TempDir()
	c, err := NewClient(ClientParams{Endpoint: "file://" + dir})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLocal_CreateBucket(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	out, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "mybucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if out == nil {
		t.Fatal("CreateBucket returned nil output")
	}

	out2, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "mybucket"})
	if err != nil {
		t.Fatalf("CreateBucket idempotent: %v", err)
	}
	if out2 == nil {
		t.Fatal("CreateBucket idempotent returned nil output")
	}
}

func TestLocal_PutObject_GetObject(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	body := []byte("hello world")
	putOut, err := c.PutObject(ctx, PutObjectInput{
		Key:           "dir/file.txt",
		Body:          bytes.NewReader(body),
		ContentLength: int64(len(body)),
		ContentType:   "text/plain",
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if putOut.ETag == "" {
		t.Error("PutObject ETag is empty")
	}

	getOut, err := c.GetObject(ctx, GetObjectInput{Key: "dir/file.txt"})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = getOut.Body.Close() }()

	if !strings.HasPrefix(getOut.ContentType, "text/plain") {
		t.Errorf("ContentType = %q, want prefix %q", getOut.ContentType, "text/plain")
	}
	if getOut.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", getOut.ContentLength, len(body))
	}

	got, err := io.ReadAll(getOut.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestLocal_GetObject_NotFound(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	_, err = c.GetObject(ctx, GetObjectInput{Key: "does-not-exist.txt"})
	if err == nil {
		t.Error("GetObject for non-existent key should fail")
	}
}

func TestLocal_HeadObject(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	body := []byte("head me")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "head.txt",
		Body:          bytes.NewReader(body),
		ContentLength: int64(len(body)),
		ContentType:   "text/plain",
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	head, err := c.HeadObject(ctx, HeadObjectInput{Key: "head.txt"})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}

	if head.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", head.ContentLength, len(body))
	}
	if !strings.HasPrefix(head.ContentType, "text/plain") {
		t.Errorf("ContentType = %q, want prefix %q", head.ContentType, "text/plain")
	}
	if head.ETag == "" {
		t.Error("ETag is empty")
	}
}

func TestLocal_DeleteObject(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	body := []byte("delete me")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "delete.txt",
		Body:          bytes.NewReader(body),
		ContentLength: int64(len(body)),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	_, err = c.DeleteObject(ctx, DeleteObjectInput{Key: "delete.txt"})
	if err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}

	_, err = c.HeadObject(ctx, HeadObjectInput{Key: "delete.txt"})
	if err == nil {
		t.Error("HeadObject after delete should fail")
	}
}

func TestLocal_ListObjectsV2(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	keys := []string{"list/a.txt", "list/b.txt", "list/sub/c.txt", "other/d.txt"}
	for _, key := range keys {
		body := []byte("content for " + key)
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           key,
			Body:          bytes.NewReader(body),
			ContentLength: int64(len(body)),
		})
		if err != nil {
			t.Fatalf("PutObject(%s): %v", key, err)
		}
	}

	result, err := c.ListObjectsV2(ctx, ListObjectsV2Input{
		Prefix: "list/",
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}

	if len(result.Contents) != 3 {
		t.Errorf("ListObjectsV2 returned %d objects, want 3", len(result.Contents))
	}
	if result.KeyCount != 3 {
		t.Errorf("KeyCount = %d, want 3", result.KeyCount)
	}
}

func TestLocal_ListObjectsV2_Delimiter(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	keys := []string{"a/1.txt", "a/2.txt", "b/3.txt", "root.txt"}
	for _, key := range keys {
		body := []byte("content")
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           key,
			Body:          bytes.NewReader(body),
			ContentLength: int64(len(body)),
		})
		if err != nil {
			t.Fatalf("PutObject(%s): %v", key, err)
		}
	}

	result, err := c.ListObjectsV2(ctx, ListObjectsV2Input{
		Delimiter: "/",
	})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}

	if len(result.Contents) != 1 {
		t.Errorf("Contents = %d objects, want 1", len(result.Contents))
	}
	if result.Contents[0].Key != "root.txt" {
		t.Errorf("Contents[0].Key = %q, want %q", result.Contents[0].Key, "root.txt")
	}
	if len(result.CommonPrefixes) != 2 {
		t.Errorf("CommonPrefixes = %d, want 2", len(result.CommonPrefixes))
	}
}

func TestLocal_PutObject_ExplicitBucket(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "bucket1"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	body := []byte("explicit bucket")
	_, err = c.PutObject(ctx, PutObjectInput{
		Bucket:        "bucket1",
		Key:           "file.txt",
		Body:          bytes.NewReader(body),
		ContentLength: int64(len(body)),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	getOut, err := c.GetObject(ctx, GetObjectInput{
		Bucket: "bucket1",
		Key:    "file.txt",
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = getOut.Body.Close() }()

	got, err := io.ReadAll(getOut.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

func TestLocal_PutObject_NoGlobalMimeSideEffect(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// ".zzz" is not in the built-in mime table; a custom content type must
	// not be registered globally for the extension.
	body := []byte("x")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "file.zzz",
		Body:          bytes.NewReader(body),
		ContentLength: int64(len(body)),
		ContentType:   "text/polluted",
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	if got := mime.TypeByExtension(".zzz"); got != "" {
		t.Errorf("PutObject polluted the global mime table: .zzz = %q, want \"\"", got)
	}
}

func TestLocal_PathTraversal(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	// os.Root should block paths that escape the root directory.
	// Keys with .. get normalized by filepath.Join, so we test with keys
	// that would escape if filepath.Join didn't normalize them.
	// The real protection is that os.Root.Open() returns "path escapes from parent"
	// for any path component that would go above the root.

	// Test that paths within root work fine
	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.PutObject(ctx, PutObjectInput{
		Bucket: "testbucket",
		Key:    "normal/file.txt",
		Body:   bytes.NewReader([]byte("data")),
	})
	if err != nil {
		t.Fatalf("PutObject with normal key failed: %v", err)
	}

	// Traversal components are rejected rather than silently normalized.
	_, err = c.PutObject(ctx, PutObjectInput{
		Bucket: "testbucket",
		Key:    "normal/../escaped.txt",
		Body:   bytes.NewReader([]byte("data")),
	})
	if err == nil {
		t.Fatal("PutObject with traversal key should fail")
	}
}

func TestLocal_HeadBucket(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	// Non-existent bucket should fail
	_, err := c.HeadBucket(ctx, HeadBucketInput{Bucket: "nonexistent"})
	if err == nil {
		t.Error("HeadBucket for non-existent bucket should fail")
	}

	// Create and head
	_, err = c.CreateBucket(ctx, CreateBucketInput{Bucket: "mybucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	head, err := c.HeadBucket(ctx, HeadBucketInput{Bucket: "mybucket"})
	if err != nil {
		t.Fatalf("HeadBucket: %v", err)
	}
	if head.LastModified.IsZero() {
		t.Error("LastModified should be set")
	}
}

func TestLocal_EnsureBucket(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()

	// Ensure on non-existent bucket creates it
	out, err := c.EnsureBucket(ctx, EnsureBucketInput{Bucket: "newbucket"})
	if err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	if !out.Created {
		t.Error("EnsureBucket should report Created=true for new bucket")
	}

	// Ensure on existing bucket is idempotent
	out, err = c.EnsureBucket(ctx, EnsureBucketInput{Bucket: "newbucket"})
	if err != nil {
		t.Fatalf("EnsureBucket (idempotent): %v", err)
	}
	if out.Created {
		t.Error("EnsureBucket should report Created=false for existing bucket")
	}
}

func TestLocal_PutObject_NoBody(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	_, err = c.PutObject(ctx, PutObjectInput{
		Key: "empty.txt",
	})
	if err != nil {
		t.Fatalf("PutObject with nil body: %v", err)
	}

	// Verify the file exists and is empty
	_, err = c.HeadObject(ctx, HeadObjectInput{Key: "empty.txt"})
	if err != nil {
		t.Fatalf("HeadObject for empty file: %v", err)
	}
}

func TestLocal_ListObjectsV2_MaxKeys(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	for i := range 5 {
		data := []byte(fmt.Sprintf("content-%d", i))
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           fmt.Sprintf("item-%d.txt", i),
			Body:          bytes.NewReader(data),
			ContentLength: int64(len(data)),
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
	}

	result, err := c.ListObjectsV2(ctx, ListObjectsV2Input{MaxKeys: 3})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(result.Contents) != 3 || !result.IsTruncated {
		t.Errorf("expected three objects and truncation, got %d/%v", len(result.Contents), result.IsTruncated)
	}
}

func TestLocal_ListObjectsV2_StartAfter(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	for _, k := range []string{"aaa", "bbb", "ccc", "ddd"} {
		data := []byte(k)
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           k,
			Body:          bytes.NewReader(data),
			ContentLength: int64(len(data)),
		})
		if err != nil {
			t.Fatalf("PutObject: %v", err)
		}
	}

	result, err := c.ListObjectsV2(ctx, ListObjectsV2Input{StartAfter: "bbb"})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(result.Contents) != 2 || result.Contents[0].Key != "ccc" {
		t.Errorf("unexpected StartAfter result: %+v", result.Contents)
	}
}

func TestLocal_GetObject_ContentTypeByExtension(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	tests := []struct {
		key, wantContentType string
	}{
		{"file.html", "text/html"},
		{"file.json", "application/json"},
		{"file.xml", "text/xml"},
		{"file.unknown123", "application/octet-stream"},
	}
	for _, tt := range tests {
		data := []byte("data")
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           tt.key,
			Body:          bytes.NewReader(data),
			ContentLength: int64(len(data)),
		})
		if err != nil {
			t.Fatalf("PutObject(%s): %v", tt.key, err)
		}

		obj, err := c.GetObject(ctx, GetObjectInput{Key: tt.key})
		if err != nil {
			t.Fatalf("GetObject(%s): %v", tt.key, err)
		}
		_ = obj.Body.Close()
		if !strings.HasPrefix(obj.ContentType, strings.Split(tt.wantContentType, ";")[0]) {
			t.Errorf("GetObject(%s) ContentType = %q, want prefix %q", tt.key, obj.ContentType, tt.wantContentType)
		}
	}
}

func TestLocal_HeadObject_ContentTypeByExtension(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	data := []byte("json data")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "data.json",
		Body:          bytes.NewReader(data),
		ContentLength: int64(len(data)),
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	head, err := c.HeadObject(ctx, HeadObjectInput{Key: "data.json"})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if !strings.HasPrefix(head.ContentType, "application/json") {
		t.Errorf("ContentType = %q, want prefix %q", head.ContentType, "application/json")
	}
}

func TestLocal_DeleteObject_NotFound(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// Deleting a non-existent key should not error (S3 behavior)
	_, err = c.DeleteObject(ctx, DeleteObjectInput{Key: "nonexistent.txt"})
	if err != nil {
		t.Fatalf("DeleteObject nonexistent should not error: %v", err)
	}
}

func TestLocal_PutObject_UpdateExisting(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	first := []byte("first")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "overwrite.txt",
		Body:          bytes.NewReader(first),
		ContentLength: int64(len(first)),
	})
	if err != nil {
		t.Fatalf("PutObject first: %v", err)
	}

	second := []byte("second version")
	_, err = c.PutObject(ctx, PutObjectInput{
		Key:           "overwrite.txt",
		Body:          bytes.NewReader(second),
		ContentLength: int64(len(second)),
	})
	if err != nil {
		t.Fatalf("PutObject second: %v", err)
	}

	obj, err := c.GetObject(ctx, GetObjectInput{Key: "overwrite.txt"})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = obj.Body.Close() }()
	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("body = %q, want %q", got, second)
	}
}

func TestLocal_ListObjectsV2_PrefixMatch(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	c.SetBucket("testbucket")
	ctx := context.Background()

	_, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "testbucket"})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	keys := []string{"logs/2024/a.txt", "logs/2024/b.txt", "logs/2025/c.txt", "other/d.txt"}
	for _, k := range keys {
		data := []byte(k)
		_, err := c.PutObject(ctx, PutObjectInput{
			Key:           k,
			Body:          bytes.NewReader(data),
			ContentLength: int64(len(data)),
		})
		if err != nil {
			t.Fatalf("PutObject(%s): %v", k, err)
		}
	}

	result, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Prefix: "logs/2024/"})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(result.Contents) != 2 {
		t.Errorf("Contents = %d, want 2", len(result.Contents))
	}
	for i, obj := range result.Contents {
		if !strings.HasPrefix(obj.Key, "logs/2024/") {
			t.Errorf("Contents[%d].Key = %q, want prefix %q", i, obj.Key, "logs/2024/")
		}
	}
}

func TestLocalObjectReaderError(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: "x", Body: strings.NewReader("x")}); err != nil {
		t.Fatal(err)
	}
	// Removing the object after creation exercises the normal missing-key path.
	if _, err := c.DeleteObject(ctx, DeleteObjectInput{Bucket: "b", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "x"}); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("get = %v", err)
	}
}

func TestLocalListTruncationWithPrefixesOnly(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a/x", "b/y", "c/z"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x")}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: 1})
	if err != nil || len(out.Contents) != 0 || len(out.CommonPrefixes) != 1 || !out.IsTruncated {
		t.Fatalf("list = %+v, %v", out, err)
	}
}

func TestValidateLocalPathBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		path       string
		allowEmpty bool
		wantErr    bool
	}{
		{"empty allowed", "", true, false},
		{"empty rejected", "", false, true},
		{"absolute", "/tmp/object", false, true},
		{"nul", "a\x00b", false, true},
		{"parent", "a/../b", false, true},
		{"current", "a/./b", false, false},
		{"normal", "a/b", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateLocalPath(test.path, test.allowEmpty)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestLocalInvalidKeysAcrossOperations(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "../x", "/x", "a\x00b"} {
		if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x")}); err == nil {
			t.Errorf("PutObject accepted invalid key %q", key)
		}
		if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: key}); err == nil {
			t.Errorf("GetObject accepted invalid key %q", key)
		}
		if _, err := c.HeadObject(context.Background(), HeadObjectInput{Bucket: "b", Key: key}); err == nil {
			t.Errorf("HeadObject accepted invalid key %q", key)
		}
		if _, err := c.DeleteObject(context.Background(), DeleteObjectInput{Bucket: "b", Key: key}); err == nil {
			t.Errorf("DeleteObject accepted invalid key %q", key)
		}
	}
	for _, bucket := range []string{"", "../b", "/b", "a\x00b"} {
		if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: bucket}); bucket == "" && !errors.Is(err, ErrNoBucket) {
			t.Errorf("empty bucket error = %v", err)
		} else if bucket != "" && err == nil {
			t.Errorf("CreateBucket accepted invalid bucket %q", bucket)
		}
	}
}

func TestLocalListPaginationAndDelimiterBoundary(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "dir/x", "dir/y", "z"} {
		if _, err := c.PutObject(ctx, PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader(key)}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", MaxKeys: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.KeyCount != 2 || !page.IsTruncated {
		t.Fatalf("page = %+v", page)
	}
	if page.Contents[0].Key != "a" || page.Contents[1].Key != "b" {
		t.Fatalf("unexpected first page: %+v", page.Contents)
	}
	page, err = c.ListObjectsV2(ctx, ListObjectsV2Input{Bucket: "b", Delimiter: "/", ContinuationToken: "b", MaxKeys: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Contents) != 1 || page.Contents[0].Key != "z" || len(page.CommonPrefixes) != 1 || page.CommonPrefixes[0].Prefix != "dir/" {
		t.Fatalf("unexpected continuation page: %+v", page)
	}
}

func TestLocalSymlinkEscape(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	ctx := context.Background()
	if _, err := c.CreateBucket(ctx, CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.Symlink(outside, "b/link.txt"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := c.GetObject(ctx, GetObjectInput{Bucket: "b", Key: "link.txt"}); err == nil {
		t.Error("GetObject followed a symlink outside the local root")
	}
	if _, err := c.HeadObject(ctx, HeadObjectInput{Bucket: "b", Key: "link.txt"}); err == nil {
		t.Error("HeadObject followed a symlink outside the local root")
	}
}
