package bares3

import (
	"errors"
	"io"
	"net/http"
	"time"
)

var ErrNoBucket = errors.New("bares3: no bucket specified and no default bucket set")

var (
	ErrNoSuchKey    = &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist.", StatusCode: http.StatusNotFound}
	ErrNoSuchBucket = &S3Error{Code: "NoSuchBucket", Message: "The specified bucket does not exist.", StatusCode: http.StatusNotFound}
	ErrAccessDenied = &S3Error{Code: "AccessDenied", Message: "Access Denied.", StatusCode: http.StatusForbidden}

	// ErrInvalidRange reports a well-formed Range request that cannot be
	// satisfied by the object. Remote backends surface it with HTTP status 416.
	ErrInvalidRange = &S3Error{Code: "InvalidRange", Message: "The requested range is not satisfiable."}
	// ErrPreconditionFailed reports a failed If-Match or If-Unmodified-Since
	// condition. Remote backends surface it with HTTP status 412.
	ErrPreconditionFailed = &S3Error{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold"}
	// ErrNotModified reports a matched If-None-Match or If-Modified-Since
	// condition. Remote backends surface it with HTTP status 304.
	ErrNotModified = &S3Error{Code: "NotModified", Message: "Not Modified"}
)

// invalidRangeError, preconditionFailedError, and notModifiedError build
// distinct values so callers can read StatusCode as well as match the package
// sentinels through errors.Is, which compares codes.
func invalidRangeError() *S3Error {
	return &S3Error{Code: ErrInvalidRange.Code, Message: ErrInvalidRange.Message, StatusCode: http.StatusRequestedRangeNotSatisfiable}
}

func preconditionFailedError() *S3Error {
	return &S3Error{Code: ErrPreconditionFailed.Code, Message: ErrPreconditionFailed.Message, StatusCode: http.StatusPreconditionFailed}
}

func notModifiedError() *S3Error {
	return &S3Error{Code: ErrNotModified.Code, Message: ErrNotModified.Message, StatusCode: http.StatusNotModified}
}

// statusOnlyError names the S3 errors that can arrive without an XML error
// document, such as the conditional-read and range replies to a HEAD request.
// Compatible providers are free to omit the document, so the status alone has
// to be enough to classify the failure.
func statusOnlyError(statusCode int) *S3Error {
	switch statusCode {
	case http.StatusNotModified:
		return notModifiedError()
	case http.StatusPreconditionFailed:
		return preconditionFailedError()
	case http.StatusRequestedRangeNotSatisfiable:
		return invalidRangeError()
	case http.StatusNotFound:
		return &S3Error{Code: "NotFound", Message: "Not Found", StatusCode: statusCode}
	default:
		return nil
	}
}

type S3Error struct {
	Code       string `xml:"Code"`
	Message    string `xml:"Message"`
	Resource   string `xml:"Resource"`
	RequestID  string `xml:"RequestId"`
	BucketName string `xml:"BucketName"`
	HostID     string `xml:"HostId"`
	StatusCode int    `xml:"-"`
}

func (e *S3Error) Error() string {
	return "s3: " + e.Code + ": " + e.Message
}

func (e *S3Error) Is(target error) bool {
	if e == nil {
		return false
	}
	targetErr, ok := target.(*S3Error)
	if !ok || targetErr == nil {
		return false
	}
	return targetErr.Code == e.Code
}

type GetObjectInput struct {
	Bucket                     string
	Key                        string
	IfMatch                    string
	IfModifiedSince            *time.Time
	IfNoneMatch                string
	IfUnmodifiedSince          *time.Time
	Range                      string
	VersionId                  string
	SSECustomerAlgorithm       string
	SSECustomerKey             string
	SSECustomerKeyMD5          string
	ResponseCacheControl       string
	ResponseContentDisposition string
	ResponseContentEncoding    string
	ResponseContentLanguage    string
	ResponseContentType        string
	ResponseExpires            string
}

type GetObjectOutput struct {
	DeleteMarker            *bool
	AcceptRanges            string
	Expiration              string
	Restore                 string
	LastModified            time.Time
	ContentLength           int64
	ETag                    string
	ChecksumCRC32           string
	ChecksumCRC32C          string
	ChecksumSHA1            string
	ChecksumSHA256          string
	ContentRange            string
	ContentType             string
	Expires                 time.Time
	WebsiteRedirectLocation string
	ServerSideEncryption    string
	SSEKMSKeyId             string
	VersionId               string
	StorageClass            string
	PartsCount              *int
	Body                    io.ReadCloser
}

type PutObjectInput struct {
	Bucket string
	Key    string
	Body   io.Reader // ContentLength is the exact number of bytes to upload. A negative value
	// requests unknown-length streaming. Zero means an empty upload; a nil
	// Body is always treated as empty.
	ContentLength int64
	// ContentSHA256 is the expected SHA-256 payload hash. When supplied, it is
	// used for SigV4 signing and verified while the body streams.
	ContentSHA256 string

	ContentType             string
	ACL                     string
	CacheControl            string
	ContentDisposition      string
	ContentEncoding         string
	ContentLanguage         string
	ContentMD5              string
	Expires                 *time.Time
	Metadata                map[string]string
	ServerSideEncryption    string
	StorageClass            string
	WebsiteRedirectLocation string
	Tagging                 string
}

type PutObjectOutput struct {
	Expiration     string
	ETag           string
	ChecksumCRC32  string
	ChecksumCRC32C string
	ChecksumSHA1   string
	ChecksumSHA256 string
	VersionId      string
	SSEKMSKeyId    string
}

type DeleteObjectInput struct {
	Bucket    string
	Key       string
	VersionId string
}

type DeleteObjectOutput struct {
	DeleteMarker *bool
	VersionId    string
}

type HeadObjectInput struct {
	Bucket               string
	Key                  string
	IfMatch              string
	IfModifiedSince      *time.Time
	IfNoneMatch          string
	IfUnmodifiedSince    *time.Time
	Range                string
	VersionId            string
	SSECustomerAlgorithm string
	SSECustomerKey       string
	SSECustomerKeyMD5    string
}

type HeadObjectOutput struct {
	DeleteMarker            *bool
	AcceptRanges            string
	Expiration              string
	Restore                 string
	LastModified            time.Time
	ContentLength           int64
	ETag                    string
	ChecksumCRC32           string
	ChecksumCRC32C          string
	ChecksumSHA1            string
	ChecksumSHA256          string
	ContentRange            string
	ContentType             string
	Expires                 time.Time
	WebsiteRedirectLocation string
	ServerSideEncryption    string
	SSEKMSKeyId             string
	VersionId               string
	StorageClass            string
	PartsCount              *int
}

type ListObjectsV2Input struct {
	Bucket            string
	ContinuationToken string
	Delimiter         string
	FetchOwner        bool
	MaxKeys           int
	Prefix            string
	StartAfter        string
}

type ListObjectsV2Output struct {
	IsTruncated           bool
	Contents              []Object
	Name                  string
	Prefix                string
	Delimiter             string
	MaxKeys               int
	CommonPrefixes        []CommonPrefix
	ContinuationToken     string
	NextContinuationToken string
	KeyCount              int
}

type Object struct {
	Key          string
	LastModified time.Time
	ETag         string
	Size         int64
	StorageClass string
	Owner        *Owner
}

type CommonPrefix struct {
	Prefix string
}

type Owner struct {
	ID          string
	DisplayName string
}

type CreateBucketInput struct {
	Bucket                    string
	CreateBucketConfiguration *CreateBucketConfiguration
	ACL                       string
}

type CreateBucketConfiguration struct {
	LocationConstraint string
}

type CreateBucketOutput struct {
	Location string
}

type HeadBucketInput struct {
	Bucket string
}

// HeadBucketOutput reports what a bucket HEAD can actually carry. Object-shaped
// attributes (ETag, checksums, storage class, ...) never apply to a bucket and
// were deliberately not part of this type.
type HeadBucketOutput struct {
	LastModified time.Time
}

type EnsureBucketInput struct {
	Bucket                    string
	CreateBucketConfiguration *CreateBucketConfiguration
}

type EnsureBucketOutput struct {
	Created  bool
	Location string
}
