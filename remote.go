package bares3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// uploadReadCloser streams the source, enforces an optional length, and
// verifies an optional expected SHA-256 without buffering the object.
type uploadReadCloser struct {
	body     io.ReadCloser
	hash     hash.Hash
	expected []byte
	read     int64
	limit    int64
	checked  bool
}

func (r *uploadReadCloser) Read(p []byte) (int, error) {
	if r.limit >= 0 && r.read == r.limit {
		return r.finish(p)
	}
	if r.limit >= 0 {
		remaining := r.limit - r.read
		if int64(len(p)) > remaining {
			p = p[:remaining]
		}
	}
	n, err := r.body.Read(p)
	if n > 0 {
		_, _ = r.hash.Write(p[:n])
		r.read += int64(n)
		if r.limit >= 0 && r.read == r.limit && err == nil {
			return n, nil
		}
	}
	if err == io.EOF {
		if r.limit >= 0 && r.read != r.limit {
			_ = r.body.Close()
			return n, errors.New("content length does not match body")
		}
		if finishErr := r.verify(); finishErr != nil {
			return n, finishErr
		}
	}
	return n, err
}

func (r *uploadReadCloser) finish(_ []byte) (int, error) {
	var extra [1]byte
	n, err := r.body.Read(extra[:])
	if n != 0 {
		_ = r.body.Close()
		return 0, errors.New("content length does not match body")
	}
	if err != nil && err != io.EOF {
		return 0, err
	}
	if verifyErr := r.verify(); verifyErr != nil {
		return 0, verifyErr
	}
	return 0, io.EOF
}

func (r *uploadReadCloser) verify() error {
	if r.checked || len(r.expected) == 0 {
		return nil
	}
	r.checked = true
	if !bytes.Equal(r.hash.Sum(nil), r.expected) {
		return errors.New("content SHA-256 does not match ContentSHA256")
	}
	return nil
}

func (r *uploadReadCloser) Close() error { return r.body.Close() }

func parseContentSHA256(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("ContentSHA256 must be a 64-character hexadecimal SHA-256 hash")
	}
	return decoded, nil
}

func asReadCloser(body io.Reader) io.ReadCloser {
	if body == nil {
		return io.NopCloser(strings.NewReader(""))
	}
	if body, ok := body.(io.ReadCloser); ok {
		return body
	}
	return io.NopCloser(body)
}

func parseS3Error(resp *http.Response, body io.Reader) *S3Error {
	s3Err := &S3Error{StatusCode: resp.StatusCode}
	s3Err.RequestID = resp.Header.Get("X-Amz-Request-Id")
	var xmlErr S3Error
	if err := xml.NewDecoder(body).Decode(&xmlErr); err != nil {
		s3Err.Message = resp.Status
		return s3Err
	}
	s3Err.Code = xmlErr.Code
	s3Err.Message = xmlErr.Message
	s3Err.Resource = xmlErr.Resource
	s3Err.BucketName = xmlErr.BucketName
	s3Err.HostID = xmlErr.HostID
	if s3Err.RequestID == "" {
		s3Err.RequestID = xmlErr.RequestID
	}
	return s3Err
}

func (c *Client) GetObject(ctx context.Context, input GetObjectInput) (*GetObjectOutput, error) {
	if c.localDir != "" {
		return c.localGetObject(ctx, input)
	}

	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "GET", bucket, input.Key, nil)
	if err != nil {
		return nil, err
	}

	if input.IfMatch != "" {
		req.Header.Set("If-Match", input.IfMatch)
	}
	if input.IfModifiedSince != nil {
		req.Header.Set("If-Modified-Since", input.IfModifiedSince.Format(http.TimeFormat))
	}
	if input.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", input.IfNoneMatch)
	}
	if input.IfUnmodifiedSince != nil {
		req.Header.Set("If-Unmodified-Since", input.IfUnmodifiedSince.Format(http.TimeFormat))
	}
	if input.Range != "" {
		req.Header.Set("Range", input.Range)
	}
	if input.SSECustomerAlgorithm != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-algorithm", input.SSECustomerAlgorithm)
	}
	if input.SSECustomerKey != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-key", input.SSECustomerKey)
	}
	if input.SSECustomerKeyMD5 != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-key-MD5", input.SSECustomerKeyMD5)
	}

	q := req.URL.Query()
	if input.VersionId != "" {
		q.Set("versionId", input.VersionId)
	}
	if input.ResponseCacheControl != "" {
		q.Set("response-cache-control", input.ResponseCacheControl)
	}
	if input.ResponseContentDisposition != "" {
		q.Set("response-content-disposition", input.ResponseContentDisposition)
	}
	if input.ResponseContentEncoding != "" {
		q.Set("response-content-encoding", input.ResponseContentEncoding)
	}
	if input.ResponseContentLanguage != "" {
		q.Set("response-content-language", input.ResponseContentLanguage)
	}
	if input.ResponseContentType != "" {
		q.Set("response-content-type", input.ResponseContentType)
	}
	if input.ResponseExpires != "" {
		q.Set("response-expires", input.ResponseExpires)
	}
	req.URL.RawQuery = q.Encode()

	if err := c.signRequest(req, EmptySHA256); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		return nil, parseS3Error(resp, resp.Body)
	}

	output := parseGetObjectResponse(resp)
	return output, nil
}

func parseGetObjectResponse(resp *http.Response) *GetObjectOutput {
	return responseMetadata(resp.Header).getObject(resp)
}

func (c *Client) PutObject(ctx context.Context, input PutObjectInput) (*PutObjectOutput, error) {
	if c.localDir != "" {
		return c.localPutObject(ctx, input)
	}

	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "PUT", bucket, input.Key, nil)
	if err != nil {
		return nil, err
	}

	if input.ContentLength >= 0 {
		req.ContentLength = input.ContentLength
	}
	if input.ContentType != "" {
		req.Header.Set("Content-Type", input.ContentType)
	}
	if input.ACL != "" {
		req.Header.Set("x-amz-acl", input.ACL)
	}
	if input.CacheControl != "" {
		req.Header.Set("Cache-Control", input.CacheControl)
	}
	if input.ContentDisposition != "" {
		req.Header.Set("Content-Disposition", input.ContentDisposition)
	}
	if input.ContentEncoding != "" {
		req.Header.Set("Content-Encoding", input.ContentEncoding)
	}
	if input.ContentLanguage != "" {
		req.Header.Set("Content-Language", input.ContentLanguage)
	}
	if input.ContentMD5 != "" {
		req.Header.Set("Content-MD5", input.ContentMD5)
	}
	if input.Expires != nil {
		req.Header.Set("Expires", input.Expires.Format(http.TimeFormat))
	}
	for k, v := range input.Metadata {
		req.Header.Set("x-amz-meta-"+k, v)
	}
	if input.ServerSideEncryption != "" {
		req.Header.Set("x-amz-server-side-encryption", input.ServerSideEncryption)
	}
	if input.StorageClass != "" {
		req.Header.Set("x-amz-storage-class", input.StorageClass)
	}
	if input.WebsiteRedirectLocation != "" {
		req.Header.Set("x-amz-website-redirect-location", input.WebsiteRedirectLocation)
	}
	if input.Tagging != "" {
		req.Header.Set("x-amz-tagging", input.Tagging)
	}

	expectedHash, err := parseContentSHA256(input.ContentSHA256)
	if err != nil {
		return nil, err
	}

	var payloadHash string
	if input.Body == nil {
		if input.ContentSHA256 != "" && input.ContentSHA256 != EmptySHA256 {
			return nil, errors.New("ContentSHA256 does not match empty body")
		}
		payloadHash = EmptySHA256
		req.Body = io.NopCloser(strings.NewReader(""))
		req.ContentLength = 0
	} else if input.ContentLength == 0 {
		// A zero length is the explicit empty-upload form. Keep compatibility
		// with existing callers that provide an unused reader for this form.
		if input.ContentSHA256 != "" && input.ContentSHA256 != EmptySHA256 {
			return nil, errors.New("ContentSHA256 does not match empty body")
		}
		payloadHash = EmptySHA256
		req.Body = io.NopCloser(strings.NewReader(""))
		req.ContentLength = 0
	} else if input.ContentLength > 0 {
		if input.Body == nil {
			return nil, errors.New("content length is non-zero but body is nil")
		}
		payloadHash = "UNSIGNED-PAYLOAD"
		if seeker, ok := input.Body.(io.ReadSeeker); ok {
			start, seekErr := seeker.Seek(0, io.SeekCurrent)
			if seekErr != nil {
				return nil, seekErr
			}
			end, seekErr := seeker.Seek(0, io.SeekEnd)
			if seekErr != nil {
				return nil, seekErr
			}
			if end-start != input.ContentLength {
				return nil, errors.New("content length does not match body")
			}
			if _, seekErr = seeker.Seek(start, io.SeekStart); seekErr != nil {
				return nil, seekErr
			}
			if input.ContentSHA256 == "" {
				h := sha256.New()
				if _, copyErr := io.Copy(h, seeker); copyErr != nil {
					return nil, copyErr
				}
				payloadHash = hex.EncodeToString(h.Sum(nil))
				if _, seekErr = seeker.Seek(start, io.SeekStart); seekErr != nil {
					return nil, seekErr
				}
			}
		}
		wrapped := &uploadReadCloser{
			body:     asReadCloser(input.Body),
			hash:     sha256.New(),
			expected: expectedHash,
			limit:    input.ContentLength,
		}
		req.Body = wrapped
		req.ContentLength = input.ContentLength
		if input.ContentSHA256 != "" {
			payloadHash = input.ContentSHA256
		}
	} else {
		req.Body = &uploadReadCloser{body: asReadCloser(input.Body), hash: sha256.New(), expected: expectedHash, limit: -1}
		if input.ContentSHA256 != "" {
			payloadHash = input.ContentSHA256
		} else {
			payloadHash = "UNSIGNED-PAYLOAD"
		}
	}

	if err := c.signRequest(req, payloadHash); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		s3Err := parseS3Error(resp, resp.Body)
		return nil, s3Err
	}

	output := &PutObjectOutput{
		ETag:           resp.Header.Get("ETag"),
		Expiration:     resp.Header.Get("x-amz-expiration"),
		ChecksumCRC32:  resp.Header.Get("x-amz-checksum-crc32"),
		ChecksumCRC32C: resp.Header.Get("x-amz-checksum-crc32c"),
		ChecksumSHA1:   resp.Header.Get("x-amz-checksum-sha1"),
		ChecksumSHA256: resp.Header.Get("x-amz-checksum-sha256"),
		VersionId:      resp.Header.Get("x-amz-version-id"),
		SSEKMSKeyId:    resp.Header.Get("x-amz-server-side-encryption-aws-kms-key-id"),
	}
	return output, nil
}

func (c *Client) DeleteObject(ctx context.Context, input DeleteObjectInput) (*DeleteObjectOutput, error) {
	if c.localDir != "" {
		return c.localDeleteObject(ctx, input)
	}

	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "DELETE", bucket, input.Key, nil)
	if err != nil {
		return nil, err
	}

	if input.VersionId != "" {
		q := req.URL.Query()
		q.Set("versionId", input.VersionId)
		req.URL.RawQuery = q.Encode()
	}

	if err := c.signRequest(req, EmptySHA256); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		s3Err := parseS3Error(resp, resp.Body)
		return nil, s3Err
	}

	output := &DeleteObjectOutput{
		VersionId: resp.Header.Get("x-amz-version-id"),
	}
	if v := resp.Header.Get("x-amz-delete-marker"); v == "true" {
		t := true
		output.DeleteMarker = &t
	}
	return output, nil
}

func (c *Client) HeadObject(ctx context.Context, input HeadObjectInput) (*HeadObjectOutput, error) {
	if c.localDir != "" {
		return c.localHeadObject(ctx, input)
	}

	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "HEAD", bucket, input.Key, nil)
	if err != nil {
		return nil, err
	}

	if input.IfMatch != "" {
		req.Header.Set("If-Match", input.IfMatch)
	}
	if input.IfModifiedSince != nil {
		req.Header.Set("If-Modified-Since", input.IfModifiedSince.Format(http.TimeFormat))
	}
	if input.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", input.IfNoneMatch)
	}
	if input.IfUnmodifiedSince != nil {
		req.Header.Set("If-Unmodified-Since", input.IfUnmodifiedSince.Format(http.TimeFormat))
	}
	if input.Range != "" {
		req.Header.Set("Range", input.Range)
	}
	if input.SSECustomerAlgorithm != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-algorithm", input.SSECustomerAlgorithm)
	}
	if input.SSECustomerKey != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-key", input.SSECustomerKey)
	}
	if input.SSECustomerKeyMD5 != "" {
		req.Header.Set("x-amz-server-side-encryption-customer-key-MD5", input.SSECustomerKeyMD5)
	}

	if input.VersionId != "" {
		q := req.URL.Query()
		q.Set("versionId", input.VersionId)
		req.URL.RawQuery = q.Encode()
	}

	if err := c.signRequest(req, EmptySHA256); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		s3Err := parseS3Error(resp, resp.Body)
		if s3Err.Code == "" {
			switch resp.StatusCode {
			case 404:
				s3Err.Code = "NotFound"
			default:
				s3Err.Code = resp.Status
			}
		}
		return nil, s3Err
	}

	return responseMetadata(resp.Header).headObject(), nil
}

type listObjectsV2Response struct {
	XMLName               xml.Name          `xml:"ListBucketResult"`
	Name                  string            `xml:"Name"`
	Prefix                string            `xml:"Prefix"`
	KeyCount              int               `xml:"KeyCount"`
	MaxKeys               int               `xml:"MaxKeys"`
	Delimiter             string            `xml:"Delimiter"`
	IsTruncated           bool              `xml:"IsTruncated"`
	ContinuationToken     string            `xml:"ContinuationToken"`
	NextContinuationToken string            `xml:"NextContinuationToken"`
	Contents              []objectXML       `xml:"Contents"`
	CommonPrefixes        []commonPrefixXML `xml:"CommonPrefixes"`
}

type objectXML struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	Owner        *ownerXML `xml:"Owner"`
}

type ownerXML struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type commonPrefixXML struct {
	Prefix string `xml:"Prefix"`
}

func (c *Client) ListObjectsV2(ctx context.Context, input ListObjectsV2Input) (*ListObjectsV2Output, error) {
	if c.localDir != "" {
		return c.localListObjectsV2(ctx, input)
	}

	bucket, err := c.getBucket(input.Bucket)
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, "GET", bucket, "", nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Set("list-type", "2")
	if input.ContinuationToken != "" {
		q.Set("continuation-token", input.ContinuationToken)
	}
	if input.Delimiter != "" {
		q.Set("delimiter", input.Delimiter)
	}
	if input.EncodingType != "" {
		q.Set("encoding-type", input.EncodingType)
	}
	if input.FetchOwner {
		q.Set("fetch-owner", "true")
	}
	if input.MaxKeys > 0 {
		q.Set("max-keys", strconv.Itoa(input.MaxKeys))
	}
	if input.Prefix != "" {
		q.Set("prefix", input.Prefix)
	}
	if input.StartAfter != "" {
		q.Set("start-after", input.StartAfter)
	}
	req.URL.RawQuery = q.Encode()

	if err := c.signRequest(req, EmptySHA256); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, parseS3Error(resp, io.NopCloser(bytes.NewReader(body)))
	}

	var xmlResp listObjectsV2Response
	if err := xml.Unmarshal(body, &xmlResp); err != nil {
		return nil, err
	}

	output := &ListObjectsV2Output{
		IsTruncated:           xmlResp.IsTruncated,
		Name:                  xmlResp.Name,
		Prefix:                xmlResp.Prefix,
		Delimiter:             xmlResp.Delimiter,
		MaxKeys:               xmlResp.MaxKeys,
		ContinuationToken:     xmlResp.ContinuationToken,
		NextContinuationToken: xmlResp.NextContinuationToken,
		KeyCount:              xmlResp.KeyCount,
	}

	output.Contents = make([]Object, len(xmlResp.Contents))
	for i, o := range xmlResp.Contents {
		output.Contents[i] = Object{
			Key:          o.Key,
			LastModified: o.LastModified,
			ETag:         o.ETag,
			Size:         o.Size,
			StorageClass: o.StorageClass,
		}
		if o.Owner != nil {
			output.Contents[i].Owner = &Owner{
				ID:          o.Owner.ID,
				DisplayName: o.Owner.DisplayName,
			}
		}
	}

	output.CommonPrefixes = make([]CommonPrefix, len(xmlResp.CommonPrefixes))
	for i, p := range xmlResp.CommonPrefixes {
		output.CommonPrefixes[i] = CommonPrefix{Prefix: p.Prefix} //nolint:staticcheck // struct-to-struct conversion required
	}

	return output, nil
}

func (c *Client) HeadBucket(ctx context.Context, input HeadBucketInput) (*HeadBucketOutput, error) {
	if c.localDir != "" {
		return c.localHeadBucket(ctx, input)
	}

	bucket := input.Bucket
	if bucket == "" {
		return nil, ErrNoBucket
	}

	req, err := c.newRequest(ctx, "HEAD", bucket, "", nil)
	if err != nil {
		return nil, err
	}

	if err := c.signRequest(req, EmptySHA256); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		s3Err := parseS3Error(resp, resp.Body)
		if s3Err.Code == "" {
			switch resp.StatusCode {
			case 404:
				s3Err.Code = "NotFound"
			default:
				s3Err.Code = resp.Status
			}
		}
		return nil, s3Err
	}

	return responseMetadata(resp.Header).headBucket(), nil
}

func (c *Client) CreateBucket(ctx context.Context, input CreateBucketInput) (*CreateBucketOutput, error) {
	if c.localDir != "" {
		return c.localCreateBucket(ctx, input)
	}

	var body io.Reader
	if input.CreateBucketConfiguration != nil && input.CreateBucketConfiguration.LocationConstraint != "" {
		var buf bytes.Buffer
		buf.WriteString(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>`)
		xml.Escape(&buf, []byte(input.CreateBucketConfiguration.LocationConstraint))
		buf.WriteString(`</LocationConstraint></CreateBucketConfiguration>`)
		body = &buf
	}

	req, err := c.newRequest(ctx, "PUT", input.Bucket, "", body)
	if err != nil {
		return nil, err
	}

	if input.ACL != "" {
		req.Header.Set("x-amz-acl", input.ACL)
	}

	var payloadHash string
	if body != nil {
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, &S3Error{Code: "InternalError", Message: err.Error(), StatusCode: 500}
		}
		payloadHash = HashSHA256(bodyBytes)
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	} else {
		payloadHash = EmptySHA256
	}

	if err := c.signRequest(req, payloadHash); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return nil, parseS3Error(resp, resp.Body)
	}

	return &CreateBucketOutput{
		Location: resp.Header.Get("Location"),
	}, nil
}
