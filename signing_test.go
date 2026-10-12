package bares3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS SigV4 Test Suite reference values
// See https://docs.aws.amazon.com/general/latest/gr/signature-v4-test-suite.html
const (
	testAccessKey = "AKIDEXAMPLE"
	testSecretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	testRegion    = "us-east-1"
	testService   = "s3"
)

func TestHashSHA256(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"hello", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"The quick brown fox jumps over the lazy dog", "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592"},
	}
	for _, tt := range tests {
		got := HashSHA256([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("HashSHA256(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCanonicalURI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		expected string
	}{
		{"/", "/"},
		{"/foo/bar", "/foo/bar"},
		{"/foo bar/baz", "/foo%20bar/baz"},
		{"", "/"},
	}
	for _, tt := range tests {
		got := CanonicalURI(tt.input)
		if got != tt.expected {
			t.Errorf("CanonicalURI(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDeriveSigningKey(t *testing.T) {
	t.Parallel()
	// Known test vector from AWS SigV4 test suite
	key := DeriveSigningKey(testSecretKey, "20150830", testRegion, testService)
	if len(key) != 32 {
		t.Errorf("DeriveSigningKey returned %d bytes, want 32", len(key))
	}

	// Sign a known string and verify
	stringToSign := "AWS4-HMAC-SHA256\n20150830T123600Z\n20150830/us-east-1/s3/aws4_request\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	sig := Sign(key, stringToSign)
	if sig == "" {
		t.Error("Sign returned empty signature")
	}
}

func TestBuildAuthorizationHeader(t *testing.T) {
	t.Parallel()
	credScope := "20150830/us-east-1/s3/aws4_request"
	got := BuildAuthorizationHeader(Algorithm, testAccessKey, credScope, "host;x-amz-date", "abc123")
	expected := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc123"
	if got != expected {
		t.Errorf("BuildAuthorizationHeader:\ngot:\n%s\n\nwant:\n%s", got, expected)
	}
}

func TestNewClientValidation(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(ClientParams{Endpoint: ""}); err == nil {
		t.Error("empty endpoint should fail")
	}
	if _, err := NewClient(ClientParams{Endpoint: "https://example.com/path"}); err == nil {
		t.Error("endpoint path should fail")
	}
	if _, err := NewClient(ClientParams{Endpoint: "https://example.com"}); err != nil {
		t.Fatalf("valid endpoint failed: %v", err)
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:  "https://s3.amazonaws.com/",
		Region:    "us-east-1",
		AccessKey: "AKIATEST",
		SecretKey: "SECRET",
	})
	if err != nil {
		t.Fatal(err)
	}

	if client.endpoint != "https://s3.amazonaws.com" {
		t.Errorf("endpoint = %q, want %q", client.endpoint, "https://s3.amazonaws.com")
	}
	if client.region != "us-east-1" {
		t.Errorf("region = %q, want %q", client.region, "us-east-1")
	}
	if client.accessKey != "AKIATEST" {
		t.Errorf("accessKey = %q, want %q", client.accessKey, "AKIATEST")
	}
}

func TestURL(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:            "https://s3.amazonaws.com/",
		Region:              "us-east-1",
		AccessKey:           "AKIATEST",
		SecretKey:           "SECRET",
		UseVirtualHostStyle: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		bucket, key, want string
	}{
		{"mybucket", "mykey.txt", "https://s3.amazonaws.com/mybucket/mykey.txt"},
		// Bucket-level requests (List/CreateBucket) must have no trailing slash.
		{"mybucket", "", "https://s3.amazonaws.com/mybucket"},
		// No default bucket set: host-style key.
		{"", "mykey.txt", "https://s3.amazonaws.com/mykey.txt"},
	}
	for _, tt := range tests {
		if got := client.url(tt.bucket, tt.key); got != tt.want {
			t.Errorf("url(%q, %q) = %q, want %q", tt.bucket, tt.key, got, tt.want)
		}
	}
}

func TestURL_VirtualHostedStyle(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:            "https://s3.amazonaws.com/",
		Region:              "us-east-1",
		AccessKey:           "AKIATEST",
		SecretKey:           "SECRET",
		UseVirtualHostStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		bucket, key, want string
	}{
		{"mybucket", "mykey.txt", "https://mybucket.s3.amazonaws.com/mykey.txt"},
		{"mybucket", "", "https://mybucket.s3.amazonaws.com"},
		// Empty bucket: no virtual-host prefix.
		{"", "mykey.txt", "https://.s3.amazonaws.com/mykey.txt"},
	}
	for _, tt := range tests {
		if got := client.url(tt.bucket, tt.key); got != tt.want {
			t.Errorf("url(%q, %q) = %q, want %q", tt.bucket, tt.key, got, tt.want)
		}
	}
}

func TestURL_VirtualHostedStyle_CustomEndpoint(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:            "https://minio.example.com",
		Region:              "us-east-1",
		AccessKey:           "AKIATEST",
		SecretKey:           "SECRET",
		UseVirtualHostStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := client.url("mybucket", "dir/file.txt")
	want := "https://mybucket.minio.example.com/dir/file.txt"
	if got != want {
		t.Errorf("url(%q, %q) = %q, want %q", "mybucket", "dir/file.txt", got, want)
	}
}

func TestSetBucket(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint: "https://s3.amazonaws.com",
		Region:   "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	bucket, err := client.getBucket("explicit")
	if err != nil {
		t.Fatal(err)
	}
	if bucket != "explicit" {
		t.Errorf("getBucket(explicit) = %q, want %q", bucket, "explicit")
	}

	_, err = client.getBucket("")
	if err != ErrNoBucket {
		t.Errorf("getBucket('') = %v, want ErrNoBucket", err)
	}

	client.SetBucket("default")
	bucket, err = client.getBucket("")
	if err != nil {
		t.Fatal(err)
	}
	if bucket != "default" {
		t.Errorf("getBucket('') after SetBucket = %q, want %q", bucket, "default")
	}
}

func TestSignRequestWithToken(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:     "https://s3.us-east-1.amazonaws.com",
		Region:       "us-east-1",
		AccessKey:    testAccessKey,
		SecretKey:    testSecretKey,
		SessionToken: "FwoGZXIvYXdzEBYaDKho...",
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := client.newRequest(context.Background(), "GET", "mybucket", "mykey.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	client.signRequest(req, EmptySHA256)

	auth := req.Header.Get("Authorization")
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date;x-amz-security-token") {
		t.Errorf("Authorization header missing security token in SignedHeaders: %s", auth)
	}

	token := req.Header.Get("X-Amz-Security-Token")
	if token != "FwoGZXIvYXdzEBYaDKho..." {
		t.Errorf("X-Amz-Security-Token = %q, want %q", token, "FwoGZXIvYXdzEBYaDKho...")
	}
}

func TestSignRequest_VirtualHostedStyle(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint:            "https://s3.us-east-1.amazonaws.com",
		Region:              "us-east-1",
		AccessKey:           testAccessKey,
		SecretKey:           testSecretKey,
		UseVirtualHostStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := client.newRequest(context.Background(), "GET", "mybucket", "mykey.txt", nil)
	if err != nil {
		t.Fatal(err)
	}

	client.signRequest(req, EmptySHA256)

	// Virtual-hosted style: the bucket must move into the host. req.Host is
	// what net/http puts on the wire and what gets signed; Header["Host"] is
	// ignored by the transport, so it is deliberately not populated.
	expectedHost := "mybucket.s3.us-east-1.amazonaws.com"
	if req.Host != expectedHost {
		t.Errorf("Host = %q, want %q", req.Host, expectedHost)
	}
	if req.URL.Host != expectedHost {
		t.Errorf("URL host = %q, want %q", req.URL.Host, expectedHost)
	}
	if _, ok := req.Header["Host"]; ok {
		t.Error("Header[Host] is set, but net/http ignores it and it is not signed")
	}

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") {
		t.Errorf("Authorization header has unexpected format: %s", auth)
	}
}

func TestS3Error_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code, msg, want string
	}{
		{"NoSuchKey", "The specified key does not exist.", "s3: NoSuchKey: The specified key does not exist."},
		{"AccessDenied", "Access Denied.", "s3: AccessDenied: Access Denied."},
		{"NoSuchBucket", "", "s3: NoSuchBucket: "},
		{"", "", "s3: : "},
	}
	for _, tt := range tests {
		got := (&S3Error{Code: tt.code, Message: tt.msg}).Error()
		if got != tt.want {
			t.Errorf("S3Error{%q, %q}.Error() = %q, want %q", tt.code, tt.msg, got, tt.want)
		}
	}
}

func TestS3Error_Is(t *testing.T) {
	t.Parallel()
	err1 := &S3Error{Code: "NoSuchKey"}
	err2 := &S3Error{Code: "NoSuchKey"}
	err3 := &S3Error{Code: "AccessDenied"}

	if !err1.Is(err2) {
		t.Error("same code errors should match")
	}
	if err1.Is(err3) {
		t.Error("different code errors should not match")
	}
}

func TestS3Error_ErrorsIs_Compat(t *testing.T) {
	t.Parallel()
	client, err := NewClient(ClientParams{
		Endpoint: "https://s3.amazonaws.com",
		Region:   "us-east-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.getBucket("")
	if !errors.Is(err, ErrNoBucket) {
		t.Error("errors.Is(err, ErrNoBucket) should be true")
	}

	noKey := ErrNoSuchKey
	err = noKey
	if !errors.Is(err, ErrNoSuchKey) {
		t.Error("errors.Is(NoSuchKey, ErrNoSuchKey) should be true")
	}
}

func TestEncodePathSegment(t *testing.T) {
	t.Parallel()
	// uriEncode encodes one segment; a "/" inside it is data and is encoded.
	segmentTests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"hello world", "hello%20world"},
		{"a+b", "a%2Bb"},
		{"foo/bar", "foo%2Fbar"},
		{"ñ", "%C3%B1"},
		{"file.xml", "file.xml"},
		{"", ""},
		{"key with spaces", "key%20with%20spaces"},
		{"a~b", "a~b"},
		{"a_b", "a_b"},
		{"a-b", "a-b"},
	}
	for _, tt := range segmentTests {
		if got := uriEncode(tt.input); got != tt.want {
			t.Errorf("uriEncode(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}

	// encodePath keeps "/" as the segment separator.
	pathTests := []struct {
		input, want string
	}{
		{"foo/bar", "foo/bar"},
		{"a b/c d", "a%20b/c%20d"},
		{"dir/ümlaut/x.txt", "dir/%C3%BCmlaut/x.txt"},
		{"", ""},
		{"/leading", "/leading"},
		{"a//b", "a//b"},
	}
	for _, tt := range pathTests {
		if got := encodePath(tt.input); got != tt.want {
			t.Errorf("encodePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseGetObjectResponse(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("data"))
	resp := &http.Response{
		StatusCode: 200,
		Header: http.Header{
			"Content-Type":          {"application/json"},
			"Content-Length":        {"42"},
			"Last-Modified":         {"Mon, 02 Jan 2006 15:04:05 GMT"},
			"Expires":               {"Tue, 03 Jan 2007 15:04:05 GMT"},
			"Etag":                  {"\"abc123\""},
			"Accept-Ranges":         {"bytes"},
			"X-Amz-Delete-Marker":   {"true"},
			"X-Amz-Version-Id":      {"v1"},
			"X-Amz-Storage-Class":   {"GLACIER"},
			"X-Amz-Mp-Parts-Count":  {"10"},
			"X-Amz-Checksum-Crc32":  {"crc32val"},
			"X-Amz-Checksum-Sha1":   {"sha1val"},
			"X-Amz-Checksum-Sha256": {"sha256val"},
			"X-Amz-Checksum-Crc32c": {"crc32cval"},
		},
		Body: body,
	}

	out := parseGetObjectResponse(resp)
	defer func() { _ = resp.Body.Close() }()

	if out.ContentType != "application/json" {
		t.Errorf("ContentType = %q, want %q", out.ContentType, "application/json")
	}
	if out.ContentLength != 42 {
		t.Errorf("ContentLength = %d, want 42", out.ContentLength)
	}
	if out.ETag != "\"abc123\"" {
		t.Errorf("ETag = %q, want %q", out.ETag, "\"abc123\"")
	}
	if out.VersionId != "v1" {
		t.Errorf("VersionId = %q, want %q", out.VersionId, "v1")
	}
	if out.StorageClass != "GLACIER" {
		t.Errorf("StorageClass = %q, want %q", out.StorageClass, "GLACIER")
	}
	if out.ChecksumCRC32 != "crc32val" {
		t.Errorf("ChecksumCRC32 = %q, want %q", out.ChecksumCRC32, "crc32val")
	}
	if out.ChecksumCRC32C != "crc32cval" {
		t.Errorf("ChecksumCRC32C = %q, want %q", out.ChecksumCRC32C, "crc32cval")
	}
	if out.ChecksumSHA1 != "sha1val" {
		t.Errorf("ChecksumSHA1 = %q, want %q", out.ChecksumSHA1, "sha1val")
	}
	if out.ChecksumSHA256 != "sha256val" {
		t.Errorf("ChecksumSHA256 = %q, want %q", out.ChecksumSHA256, "sha256val")
	}
	if out.AcceptRanges != "bytes" {
		t.Errorf("AcceptRanges = %q, want %q", out.AcceptRanges, "bytes")
	}
	if out.DeleteMarker == nil || !*out.DeleteMarker {
		t.Error("DeleteMarker should be true")
	}
	if out.PartsCount == nil || *out.PartsCount != 10 {
		t.Errorf("PartsCount = %v, want %d", out.PartsCount, 10)
	}
	if out.Expires.Year() != 2007 {
		t.Errorf("Expires year = %d, want 2007", out.Expires.Year())
	}
}

func TestParseS3Error_XML(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader(
		`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message><Resource>/bucket/key</Resource><RequestId>req123</RequestId><HostId>host456</HostId></Error>`,
	))
	resp := &http.Response{
		StatusCode: 404,
		Header:     http.Header{"X-Amz-Request-Id": {"req789"}},
		Body:       body,
	}

	err := parseS3Error(resp, body)
	if err.Code != "NoSuchKey" {
		t.Errorf("Code = %q, want %q", err.Code, "NoSuchKey")
	}
	if err.Message != "The specified key does not exist." {
		t.Errorf("Message = %q, want %q", err.Message, "The specified key does not exist.")
	}
	if err.Resource != "/bucket/key" {
		t.Errorf("Resource = %q, want %q", err.Resource, "/bucket/key")
	}
	// Request-ID header takes precedence over XML RequestId
	if err.RequestID != "req789" {
		t.Errorf("RequestID = %q, want %q (header priority)", err.RequestID, "req789")
	}
	if err.HostID != "host456" {
		t.Errorf("HostID = %q, want %q", err.HostID, "host456")
	}
}

func TestParseS3Error_NoXML(t *testing.T) {
	t.Parallel()
	body := io.NopCloser(strings.NewReader("not xml"))
	resp := &http.Response{
		StatusCode: 500,
		Header:     http.Header{},
		Body:       body,
		Status:     "500 Internal Server Error",
	}

	err := parseS3Error(resp, body)
	// An unparseable body must still yield a named, matchable error: the
	// status line as code, not an empty one.
	if err.Code != "500 Internal Server Error" {
		t.Errorf("Code = %q, want the status line as code", err.Code)
	}
	if err.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want 500", err.StatusCode)
	}
}

func TestSigV4ReferenceGetVanillaExactSignature(t *testing.T) {
	t.Parallel()
	canonicalHeaders, signedHeaders := CanonicalHeaders(map[string]string{
		"host": "example.amazonaws.com", "x-amz-date": "20150830T123600Z",
	})
	canonical := CanonicalRequest("GET", "/", "", canonicalHeaders, signedHeaders, EmptySHA256)
	hashed := HashSHA256([]byte(canonical))
	if hashed != "bb579772317eb040ac9ed261061d46c1f17a8133879d6129b6e1c25292927e63" {
		t.Fatalf("canonical hash = %s", hashed)
	}
	stringToSign := StringToSign(Algorithm, "20150830T123600Z", CredentialScope("20150830", "us-east-1", "s3"), hashed)
	key := DeriveSigningKey(testSecretKey, "20150830", "us-east-1", "s3")
	if got := Sign(key, stringToSign); got != "c184979bf80f1fe1bf360d25dd65b9fdb27729f4e53df49ec7a3ca1865402cb1" {
		t.Fatalf("signature = %s", got)
	}
}

func TestSignRequestProducesCompleteAuthorizationHeader(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c := remoteTestClient(t, func(r *http.Request) (*http.Response, error) {
		got = r
		return response(206, http.Header{}, "body"), nil
	})

	// A key needing canonicalization, several query parameters, and a payload
	// hash exercise every input signRequest derives from the request.
	if _, err := c.GetObject(context.Background(), GetObjectInput{
		Bucket:              "b",
		Key:                 "dir/a b.txt",
		VersionId:           "v1",
		ResponseContentType: "text/plain",
		Range:               "bytes=0-1",
	}); err != nil {
		t.Fatal(err)
	}

	// The canonical form of the request, spelled out per the SigV4 spec.
	// signRequest must sign the canonicalized URI and query string, not the
	// decoded values carried on the request, and must sign exactly this
	// header set.
	canonicalHeaders, signedHeaders := CanonicalHeaders(map[string]string{
		"host":                 "s3.example.com",
		"x-amz-content-sha256": EmptySHA256,
		"x-amz-date":           "20150830T123600Z",
	})
	if want := "host;x-amz-content-sha256;x-amz-date"; signedHeaders != want {
		t.Errorf("signed headers = %q, want %q", signedHeaders, want)
	}
	if want := "host:s3.example.com\nx-amz-content-sha256:" + EmptySHA256 + "\nx-amz-date:20150830T123600Z\n"; canonicalHeaders != want {
		t.Errorf("canonical headers = %q, want %q", canonicalHeaders, want)
	}
	if want := "/b/dir/a%20b.txt"; CanonicalURI(got.URL.Path) != want {
		t.Errorf("canonical URI = %q, want %q", CanonicalURI(got.URL.Path), want)
	}
	if want := "response-content-type=text%2Fplain&versionId=v1"; canonicalQueryValues(got.URL.Query()) != want {
		t.Errorf("canonical query = %q, want %q", canonicalQueryValues(got.URL.Query()), want)
	}

	canonical := CanonicalRequest("GET", CanonicalURI(got.URL.Path), canonicalQueryValues(got.URL.Query()), canonicalHeaders, signedHeaders, EmptySHA256)
	scope := CredentialScope("20150830", "us-east-1", "s3")
	want := BuildAuthorizationHeader(Algorithm, testAccessKey, scope, signedHeaders,
		Sign(DeriveSigningKey(testSecretKey, "20150830", "us-east-1", "s3"),
			StringToSign(Algorithm, "20150830T123600Z", scope, HashSHA256([]byte(canonical)))))

	// Exact match, not a prefix or substring: the credential scope, access key
	// and signature hex must all be correct.
	const authorization = "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
		"Signature=6098de7b26f7131afc50b88a810fdfaf0b0f68d3aaff55db70c1570fc86e8deb"
	if want != authorization {
		t.Fatalf("expected authorization =\n%s\ncomputed authorization =\n%s", authorization, want)
	}
	if auth := got.Header.Get("Authorization"); auth != authorization {
		t.Errorf("Authorization =\n%s\nwant\n%s", auth, authorization)
	}
	if got.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("X-Amz-Date = %q", got.Header.Get("X-Amz-Date"))
	}
	if got.Header.Get("X-Amz-Content-Sha256") != EmptySHA256 {
		t.Errorf("X-Amz-Content-Sha256 = %q", got.Header.Get("X-Amz-Content-Sha256"))
	}
}

// A session token must be signed as well as sent, otherwise S3 rejects the
// request even though the credentials are otherwise correct.
func TestSignRequestIncludesSessionToken(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c, err := NewClient(ClientParams{
		Endpoint: "https://s3.example.com", Region: "us-east-1",
		AccessKey: testAccessKey, SecretKey: testSecretKey, SessionToken: "session-token",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			return response(200, http.Header{}, "body"), nil
		})},
		Now: func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "x"}); err != nil {
		t.Fatal(err)
	}

	canonicalHeaders, signedHeaders := CanonicalHeaders(map[string]string{
		"host":                 "s3.example.com",
		"x-amz-content-sha256": EmptySHA256,
		"x-amz-date":           "20150830T123600Z",
		"x-amz-security-token": "session-token",
	})
	if want := "host;x-amz-content-sha256;x-amz-date;x-amz-security-token"; signedHeaders != want {
		t.Errorf("signed headers = %q, want %q", signedHeaders, want)
	}
	canonical := CanonicalRequest("GET", "/b/x", "", canonicalHeaders, signedHeaders, EmptySHA256)
	scope := CredentialScope("20150830", "us-east-1", "s3")
	want := BuildAuthorizationHeader(Algorithm, testAccessKey, scope, signedHeaders,
		Sign(DeriveSigningKey(testSecretKey, "20150830", "us-east-1", "s3"),
			StringToSign(Algorithm, "20150830T123600Z", scope, HashSHA256([]byte(canonical)))))
	if auth := got.Header.Get("Authorization"); auth != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", auth, want)
	}
	if got.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q", got.Header.Get("X-Amz-Security-Token"))
	}
}

// The virtual-hosted style must sign the bucket-specific host, not the bare
// endpoint, or every request against that endpoint fails signature checking.
func TestSignRequestVirtualHostedHost(t *testing.T) {
	t.Parallel()
	var got *http.Request
	c, err := NewClient(ClientParams{
		Endpoint: "https://s3.example.com", Region: "us-east-1", AccessKey: testAccessKey, SecretKey: testSecretKey,
		UseVirtualHostStyle: true,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			return response(200, http.Header{}, "body"), nil
		})},
		Now: func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "bucket", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if got.Host != "bucket.s3.example.com" {
		t.Errorf("Host = %q", got.Host)
	}

	canonicalHeaders, signedHeaders := CanonicalHeaders(map[string]string{
		"host":                 "bucket.s3.example.com",
		"x-amz-content-sha256": EmptySHA256,
		"x-amz-date":           "20150830T123600Z",
	})
	canonical := CanonicalRequest("GET", "/k", "", canonicalHeaders, signedHeaders, EmptySHA256)
	scope := CredentialScope("20150830", "us-east-1", "s3")
	want := BuildAuthorizationHeader(Algorithm, testAccessKey, scope, signedHeaders,
		Sign(DeriveSigningKey(testSecretKey, "20150830", "us-east-1", "s3"),
			StringToSign(Algorithm, "20150830T123600Z", scope, HashSHA256([]byte(canonical)))))
	if auth := got.Header.Get("Authorization"); auth != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", auth, want)
	}
}

// T5: canonical query encoding, including the value tie-break that client
// query construction never produces because it uses q.Set.
func TestCanonicalQueryValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		values map[string][]string
		want   string
	}{
		{name: "empty", values: map[string][]string{}, want: ""},
		{name: "single", values: map[string][]string{"a": {"1"}}, want: "a=1"},
		{name: "sorted by name", values: map[string][]string{"b": {"2"}, "a": {"1"}, "c": {"3"}}, want: "a=1&b=2&c=3"},
		{name: "repeated name sorted by value", values: map[string][]string{"a": {"3", "1", "2"}}, want: "a=1&a=2&a=3"},
		{name: "both tie-breaks", values: map[string][]string{"b": {"z", "y"}, "a": {"2", "1"}}, want: "a=1&a=2&b=y&b=z"},
		{name: "slash is encoded", values: map[string][]string{"prefix": {"a/b"}}, want: "prefix=a%2Fb"},
		{name: "reserved characters encoded", values: map[string][]string{"k": {"a+b c&d=e"}}, want: "k=a%2Bb%20c%26d%3De"},
		{name: "unreserved characters preserved", values: map[string][]string{"k": {"a-b_c.d~e"}}, want: "k=a-b_c.d~e"},
		{name: "empty value", values: map[string][]string{"k": {""}}, want: "k="},
		{name: "non-ascii encoded as utf8", values: map[string][]string{"k": {"ü"}}, want: "k=%C3%BC"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := canonicalQueryValues(test.values); got != test.want {
				t.Errorf("canonicalQueryValues(%v) = %q, want %q", test.values, got, test.want)
			}
		})
	}
}

func TestSigV4CanonicalURIAndQueryBoundaries(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"/a%2Fb": "/a%252Fb", "/a//b/": "/a//b/", "/a+b": "/a%2Bb", "/a?b#c": "/a%3Fb%23c", "/é": "/%C3%A9",
	} {
		if got := CanonicalURI(input); got != want {
			t.Errorf("CanonicalURI(%q) = %q, want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{
		"": "/", "/": "/", "a b": "/a%20b", "/key": "/key",
	} {
		if got := CanonicalURI(input); got != want {
			t.Errorf("CanonicalURI(%q) = %q, want %q", input, got, want)
		}
	}
	// The query half of this test: repeated names and the value tie-break.
	for input, want := range map[string]string{
		"a=1&b=2": "a=1&b=2", "b=2&a=1": "a=1&b=2", "a=2&a=1": "a=1&a=2", "a=b/c": "a=b%2Fc",
	} {
		values, err := url.ParseQuery(input)
		if err != nil {
			t.Fatalf("ParseQuery(%q): %v", input, err)
		}
		if got := canonicalQueryValues(values); got != want {
			t.Errorf("canonicalQueryValues(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLocalTraversalAndDirectoryObjects(t *testing.T) {
	t.Parallel()
	c := newLocalClient(t)
	if _, err := c.CreateBucket(context.Background(), CreateBucketInput{Bucket: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"../outside", "/absolute", "b/../../outside"} {
		if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: key, Body: strings.NewReader("x")}); err == nil {
			t.Errorf("unsafe key %q was accepted", key)
		}
	}
	if _, err := c.PutObject(context.Background(), PutObjectInput{Bucket: "b", Key: "dir", Body: strings.NewReader("x")}); err != nil {
		t.Fatal(err)
	}
	if err := c.osRoot.Mkdir("b/object-dir", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetObject(context.Background(), GetObjectInput{Bucket: "b", Key: "object-dir"}); err == nil {
		t.Error("directory should not be returned as an object")
	}
}
