package bares3_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ptman/bares3"
)

type config struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
}

func loadConfig(t *testing.T) config {
	t.Helper()
	if os.Getenv("REMOTE_TEST") == "" {
		t.Skip("REMOTE_TEST not set, skipping integration tests")
	}
	data, err := os.ReadFile("config.json")
	if err != nil {
		t.Fatalf("failed to read config.json: %v", err)
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse config.json: %v", err)
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		t.Fatal("config.json must contain endpoint, bucket, access_key, and secret_key")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	return cfg
}

func newTestClient(t *testing.T, cfg config) *bares3.Client {
	t.Helper()
	client, err := bares3.NewClient(bares3.ClientParams{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func uniqueKey(t *testing.T, name string) string {
	t.Helper()
	return fmt.Sprintf("integration-test/%s/%s-%d", name, strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
}

func cleanupObject(t *testing.T, client *bares3.Client, bucket, key string) {
	t.Helper()
	if _, err := client.DeleteObject(context.Background(), bares3.DeleteObjectInput{Bucket: bucket, Key: key}); err != nil {
		t.Logf("cleanup %s: %v", key, err)
	}
}

func TestIntegration_CreateBucket(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	out, err := client.CreateBucket(context.Background(), bares3.CreateBucketInput{Bucket: cfg.Bucket})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	t.Logf("CreateBucket output: %+v", out)
}

func TestIntegration_PutObject(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	key := uniqueKey(t, "put")
	t.Cleanup(func() { cleanupObject(t, client, cfg.Bucket, key) })
	body := []byte("integration test object content")
	out, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: bytes.NewReader(body), ContentLength: int64(len(body)), ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	t.Logf("PutObject output: %+v", out)
}

func TestIntegration_GetObject(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	key := uniqueKey(t, "get")
	t.Cleanup(func() { cleanupObject(t, client, cfg.Bucket, key) })
	expected := []byte("get object test data")
	if _, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: bytes.NewReader(expected), ContentLength: int64(len(expected)), ContentType: "application/octet-stream"}); err != nil {
		t.Fatalf("PutObject setup: %v", err)
	}
	obj, err := client.GetObject(ctx, bares3.GetObjectInput{Bucket: cfg.Bucket, Key: key})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = obj.Body.Close() }()
	if obj.ContentType != "application/octet-stream" || obj.ContentLength != int64(len(expected)) {
		t.Fatalf("metadata = type %q length %d", obj.ContentType, obj.ContentLength)
	}
	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, expected) {
		t.Errorf("body = %q, want %q", got, expected)
	}
}

func TestIntegration_HeadObject(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	key := uniqueKey(t, "head")
	t.Cleanup(func() { cleanupObject(t, client, cfg.Bucket, key) })
	expected := []byte("head object test")
	if _, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: bytes.NewReader(expected), ContentLength: int64(len(expected)), ContentType: "text/plain"}); err != nil {
		t.Fatal(err)
	}
	head, err := client.HeadObject(ctx, bares3.HeadObjectInput{Bucket: cfg.Bucket, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	if head.ContentType != "text/plain" || head.ContentLength != int64(len(expected)) || head.ETag == "" {
		t.Fatalf("head = %+v", head)
	}
}

func TestIntegration_DeleteObject(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	key := uniqueKey(t, "delete")
	if _, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: strings.NewReader("delete me"), ContentLength: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteObject(ctx, bares3.DeleteObjectInput{Bucket: cfg.Bucket, Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.HeadObject(ctx, bares3.HeadObjectInput{Bucket: cfg.Bucket, Key: key}); err == nil {
		t.Fatal("HeadObject after delete should fail")
	}
}

func TestIntegration_ListObjectsV2(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	prefix := uniqueKey(t, "list") + "/"
	keys := []string{prefix + "a.txt", prefix + "b.txt", prefix + "c.txt"}
	t.Cleanup(func() {
		for _, key := range keys {
			cleanupObject(t, client, cfg.Bucket, key)
		}
	})
	for _, key := range keys {
		body := []byte("content for " + key)
		if _, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: bytes.NewReader(body), ContentLength: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := client.ListObjectsV2(ctx, bares3.ListObjectsV2Input{Bucket: cfg.Bucket, Prefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Contents) < len(keys) {
		t.Errorf("got %d objects, want at least %d", len(result.Contents), len(keys))
	}
}

func TestIntegration_GetObject_NotFound(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	if _, err := client.GetObject(context.Background(), bares3.GetObjectInput{Bucket: cfg.Bucket, Key: uniqueKey(t, "missing")}); err == nil {
		t.Fatal("expected missing-key error")
	}
}

func TestIntegration_PutObject_GetObject_Roundtrip(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	ctx := context.Background()
	key := uniqueKey(t, "roundtrip")
	t.Cleanup(func() { cleanupObject(t, client, cfg.Bucket, key) })
	original := []byte("roundtrip test\nline 2\nline 3\n")
	if _, err := client.PutObject(ctx, bares3.PutObjectInput{Bucket: cfg.Bucket, Key: key, Body: bytes.NewReader(original), ContentLength: int64(len(original))}); err != nil {
		t.Fatal(err)
	}
	obj, err := client.GetObject(ctx, bares3.GetObjectInput{Bucket: cfg.Bucket, Key: key})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obj.Body.Close() }()
	got, err := io.ReadAll(obj.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("got %q, want %q", got, original)
	}
}

func TestIntegration_Error_GetObject_NoSuchKey(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	key := uniqueKey(t, "no-such-key")
	_, err := client.GetObject(context.Background(), bares3.GetObjectInput{Bucket: cfg.Bucket, Key: key})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok {
		t.Fatalf("error type %T", err)
	}
	if s3Err.Code != "NoSuchKey" || s3Err.StatusCode != 404 {
		t.Fatalf("error = %+v", s3Err)
	}
	if !errors.Is(err, bares3.ErrNoSuchKey) {
		t.Error("error should match ErrNoSuchKey")
	}
}

func TestIntegration_Error_HeadObject_NoSuchKey(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	_, err := client.HeadObject(context.Background(), bares3.HeadObjectInput{Bucket: cfg.Bucket, Key: uniqueKey(t, "no-such-head")})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok || s3Err.StatusCode != 404 {
		t.Fatalf("error = %+v", err)
	}
}

func TestIntegration_Error_NoSuchBucket(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	_, err := client.GetObject(context.Background(), bares3.GetObjectInput{Bucket: "nonexistent-bucket-" + fmt.Sprint(time.Now().UnixNano()), Key: "any-key"})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok || s3Err.StatusCode < 400 {
		t.Fatalf("error = %+v", err)
	}
}

func TestIntegration_Error_ListObjectsV2_NoSuchBucket(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	_, err := client.ListObjectsV2(context.Background(), bares3.ListObjectsV2Input{Bucket: "nonexistent-bucket-" + fmt.Sprint(time.Now().UnixNano())})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok || (s3Err.StatusCode != 403 && s3Err.StatusCode != 404) {
		t.Fatalf("error = %+v", err)
	}
}

func TestIntegration_Error_PutObject_NoSuchBucket(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	body := []byte("should fail")
	_, err := client.PutObject(context.Background(), bares3.PutObjectInput{Bucket: "nonexistent-bucket-" + fmt.Sprint(time.Now().UnixNano()), Key: "any-key", Body: bytes.NewReader(body), ContentLength: int64(len(body))})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok || (s3Err.StatusCode != 403 && s3Err.StatusCode != 404) {
		t.Fatalf("error = %+v", err)
	}
}

func TestIntegration_Error_AccessDenied(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client, err := bares3.NewClient(bares3.ClientParams{Endpoint: cfg.Endpoint, Region: cfg.Region, AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetObject(context.Background(), bares3.GetObjectInput{Bucket: cfg.Bucket, Key: uniqueKey(t, "access-denied")})
	if err == nil {
		t.Fatal("expected error")
	}
	s3Err, ok := errors.AsType[*bares3.S3Error](err)
	if !ok || s3Err.StatusCode < 400 {
		t.Fatalf("error = %+v", err)
	}
}

func TestIntegration_Error_ErrorMessage(t *testing.T) {
	t.Parallel()
	cfg := loadConfig(t)
	client := newTestClient(t, cfg)
	_, err := client.GetObject(context.Background(), bares3.GetObjectInput{Bucket: cfg.Bucket, Key: uniqueKey(t, "error-message")})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.HasPrefix(err.Error(), "s3: NoSuchKey:") {
		t.Errorf("Error() = %q", err.Error())
	}
}
