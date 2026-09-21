package bares3

import (
	"net/http"
	"strconv"
	"time"
)

func responseMetadata(h http.Header) responseMetadataValues {
	m := responseMetadataValues{
		AcceptRanges:            h.Get("Accept-Ranges"),
		Expiration:              h.Get("x-amz-expiration"),
		Restore:                 h.Get("x-amz-restore"),
		ETag:                    h.Get("ETag"),
		ChecksumCRC32:           h.Get("x-amz-checksum-crc32"),
		ChecksumCRC32C:          h.Get("x-amz-checksum-crc32c"),
		ChecksumSHA1:            h.Get("x-amz-checksum-sha1"),
		ChecksumSHA256:          h.Get("x-amz-checksum-sha256"),
		ContentType:             h.Get("Content-Type"),
		WebsiteRedirectLocation: h.Get("x-amz-website-redirect-location"),
		ServerSideEncryption:    h.Get("x-amz-server-side-encryption"),
		SSEKMSKeyID:             h.Get("x-amz-server-side-encryption-aws-kms-key-id"),
		VersionID:               h.Get("x-amz-version-id"),
		StorageClass:            h.Get("x-amz-storage-class"),
	}
	if v := h.Get("Content-Length"); v != "" {
		m.ContentLength, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := h.Get("Last-Modified"); v != "" {
		m.LastModified, _ = time.Parse(http.TimeFormat, v)
	}
	if v := h.Get("Expires"); v != "" {
		m.Expires, _ = time.Parse(http.TimeFormat, v)
	}
	if v := h.Get("x-amz-mp-parts-count"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			m.PartsCount = &n
		}
	}
	return m
}

type responseMetadataValues struct {
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
	SSEKMSKeyID             string
	VersionID               string
	StorageClass            string
	PartsCount              *int
}

func (m responseMetadataValues) getObject(resp *http.Response) *GetObjectOutput {
	return &GetObjectOutput{
		DeleteMarker:            deleteMarker(resp.Header),
		AcceptRanges:            m.AcceptRanges,
		Expiration:              m.Expiration,
		Restore:                 m.Restore,
		LastModified:            m.LastModified,
		ContentLength:           m.ContentLength,
		ETag:                    m.ETag,
		ChecksumCRC32:           m.ChecksumCRC32,
		ChecksumCRC32C:          m.ChecksumCRC32C,
		ChecksumSHA1:            m.ChecksumSHA1,
		ChecksumSHA256:          m.ChecksumSHA256,
		ContentRange:            resp.Header.Get("Content-Range"),
		ContentType:             m.ContentType,
		Expires:                 m.Expires,
		WebsiteRedirectLocation: m.WebsiteRedirectLocation,
		ServerSideEncryption:    m.ServerSideEncryption,
		SSEKMSKeyId:             m.SSEKMSKeyID,
		VersionId:               m.VersionID,
		StorageClass:            m.StorageClass,
		PartsCount:              m.PartsCount,
		Body:                    resp.Body,
	}
}

func (m responseMetadataValues) headObject() *HeadObjectOutput {
	return &HeadObjectOutput{
		DeleteMarker:            nil,
		AcceptRanges:            m.AcceptRanges,
		Expiration:              m.Expiration,
		Restore:                 m.Restore,
		LastModified:            m.LastModified,
		ContentLength:           m.ContentLength,
		ETag:                    m.ETag,
		ChecksumCRC32:           m.ChecksumCRC32,
		ChecksumCRC32C:          m.ChecksumCRC32C,
		ChecksumSHA1:            m.ChecksumSHA1,
		ChecksumSHA256:          m.ChecksumSHA256,
		ContentType:             m.ContentType,
		Expires:                 m.Expires,
		WebsiteRedirectLocation: m.WebsiteRedirectLocation,
		ServerSideEncryption:    m.ServerSideEncryption,
		SSEKMSKeyId:             m.SSEKMSKeyID,
		VersionId:               m.VersionID,
		StorageClass:            m.StorageClass,
		PartsCount:              m.PartsCount,
	}
}

func (m responseMetadataValues) headBucket() *HeadBucketOutput {
	return &HeadBucketOutput{
		AcceptRanges:            m.AcceptRanges,
		ContentLength:           m.ContentLength,
		ContentType:             m.ContentType,
		Expiration:              m.Expiration,
		Restore:                 m.Restore,
		LastModified:            m.LastModified,
		ETag:                    m.ETag,
		ChecksumCRC32:           m.ChecksumCRC32,
		ChecksumCRC32C:          m.ChecksumCRC32C,
		ChecksumSHA1:            m.ChecksumSHA1,
		ChecksumSHA256:          m.ChecksumSHA256,
		WebsiteRedirectLocation: m.WebsiteRedirectLocation,
		ServerSideEncryption:    m.ServerSideEncryption,
		SSEKMSKeyId:             m.SSEKMSKeyID,
		VersionId:               m.VersionID,
		StorageClass:            m.StorageClass,
	}
}

func deleteMarker(h http.Header) *bool {
	if h.Get("x-amz-delete-marker") != "true" {
		return nil
	}
	v := true
	return &v
}
