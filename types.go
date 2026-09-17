package bares3

import (
	"errors"
	"io"
	"time"
)

var ErrNoBucket = errors.New("bares3: no bucket specified and no default bucket set")

var (
	ErrNoSuchKey    = &S3Error{Code: "NoSuchKey", Message: "The specified key does not exist."}
	ErrNoSuchBucket = &S3Error{Code: "NoSuchBucket", Message: "The specified bucket does not exist."}
	ErrAccessDenied = &S3Error{Code: "AccessDenied", Message: "Access Denied."}
)

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
	EncodingType      string
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

type HeadBucketOutput struct {
	AcceptRanges            string
	ContentLength           int64
	ContentType             string
	Expiration              string
	Restore                 string
	LastModified            time.Time
	ETag                    string
	ChecksumCRC32           string
	ChecksumCRC32C          string
	ChecksumSHA1            string
	ChecksumSHA256          string
	WebsiteRedirectLocation string
	ServerSideEncryption    string
	SSEKMSKeyId             string
	VersionId               string
	StorageClass            string
}

type EnsureBucketInput struct {
	Bucket                    string
	CreateBucketConfiguration *CreateBucketConfiguration
}

type EnsureBucketOutput struct {
	Created  bool
	Location string
}
