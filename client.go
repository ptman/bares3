package bares3

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var hexUpper = []byte("0123456789ABCDEF")

type ClientParams struct {
	Endpoint            string
	Region              string
	AccessKey           string
	SecretKey           string
	SessionToken        string
	HTTPClient          *http.Client
	UseVirtualHostStyle bool
	Now                 func() time.Time
}

type Client struct {
	endpoint       string
	region         string
	accessKey      string
	secretKey      string
	sessionToken   string
	httpClient     *http.Client
	defaultBucket  string
	useVirtualHost bool
	localDir       string
	now            func() time.Time
	osRoot         *os.Root
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     120 * time.Second,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
}

func NewClient(params ClientParams) (*Client, error) {
	c := &Client{
		useVirtualHost: params.UseVirtualHostStyle,
		region:         params.Region,
		accessKey:      params.AccessKey,
		secretKey:      params.SecretKey,
		sessionToken:   params.SessionToken,
		httpClient:     params.HTTPClient,
		now:            params.Now,
	}
	if after, ok := strings.CutPrefix(params.Endpoint, "file://"); ok {
		c.localDir = after
		var err error
		c.osRoot, err = os.OpenRoot(after)
		if err != nil {
			return nil, err
		}
	} else {
		parsed, err := url.Parse(params.Endpoint)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" && parsed.Path != "/" {
			if err == nil {
				err = errors.New("endpoint must be an absolute URL without a path")
			}
			return nil, err
		}
		c.endpoint = strings.TrimRight(params.Endpoint, "/")
	}
	if params.UseVirtualHostStyle && params.Endpoint == "" {
		return nil, errors.New("endpoint is required for virtual-hosted style")
	}
	if c.region == "" {
		c.region = "us-east-1"
	}
	if c.httpClient == nil {
		c.httpClient = defaultHTTPClient()
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c, nil
}

func (c *Client) SetBucket(bucket string) {
	c.defaultBucket = bucket
}

func (c *Client) EnsureBucket(ctx context.Context, input EnsureBucketInput) (*EnsureBucketOutput, error) {
	_, err := c.HeadBucket(ctx, HeadBucketInput{Bucket: input.Bucket})
	if err == nil {
		return &EnsureBucketOutput{Created: false}, nil
	}

	var s3Err *S3Error
	if !errors.As(err, &s3Err) || s3Err.Code != "NotFound" && s3Err.Code != "NoSuchBucket" {
		return nil, err
	}

	created, err := c.CreateBucket(ctx, CreateBucketInput{
		Bucket:                    input.Bucket,
		CreateBucketConfiguration: input.CreateBucketConfiguration,
	})
	if err != nil {
		return nil, err
	}
	return &EnsureBucketOutput{Created: true, Location: created.Location}, nil
}

func (c *Client) getBucket(bucket string) (string, error) {
	if bucket != "" {
		return bucket, nil
	}
	if c.defaultBucket != "" {
		return c.defaultBucket, nil
	}
	return "", ErrNoBucket
}

func (c *Client) url(bucket, key string) string {
	if c.useVirtualHost {
		return c.urlVirtualHostedStyle(bucket, key)
	}
	return c.urlPathStyle(bucket, key)
}

func (c *Client) urlPathStyle(bucket, key string) string {
	parsed, _ := url.Parse(c.endpoint)
	path := ""
	rawPath := ""
	if bucket != "" {
		path = "/" + bucket
		rawPath = "/" + encodePathSegment(bucket)
	}
	if key != "" {
		path += "/" + key
		rawPath += "/" + encodePath(key)
	}
	parsed.Path = path
	parsed.RawPath = rawPath
	return parsed.String()
}

func (c *Client) urlVirtualHostedStyle(bucket, key string) string {
	parsed, _ := url.Parse(c.endpoint)
	parsed.Host = encodePathSegment(bucket) + "." + parsed.Host
	parsed.Path = ""
	parsed.RawPath = ""
	if key != "" {
		parsed.Path = "/" + decodeEscapedPath(encodePath(key))
		parsed.RawPath = "/" + encodePath(key)
	}
	return parsed.String()
}

func encodePathSegment(s string) string {
	encoded := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if isAllowedChar(b, false) {
			encoded = append(encoded, b)
		} else {
			encoded = append(encoded, '%')
			encoded = append(encoded, hexUpper[b>>4], hexUpper[b&0x0f])
		}
	}
	return string(encoded)
}

func decodeEscapedPath(s string) string {
	decoded, err := url.PathUnescape(s)
	if err != nil {
		return s
	}
	return decoded
}

func encodePath(s string) string {
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = encodePathSegment(p)
	}
	return strings.Join(parts, "/")
}

func (c *Client) newRequest(ctx context.Context, method, bucket, key string, body io.Reader) (*http.Request, error) {
	u := c.url(bucket, key)
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Host", req.Host)
	return req, nil
}

func (c *Client) signRequest(req *http.Request, payloadHash string) error {
	now := c.now().UTC()
	date := now.Format(DateFormat)
	dateTime := now.Format(TimeFormat)

	headers := map[string]string{
		"host":                 req.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           dateTime,
	}
	if c.sessionToken != "" {
		headers["x-amz-security-token"] = c.sessionToken
	}

	canonicalHeaders, signedHeaders := CanonicalHeaders(headers)

	canonicalURI := CanonicalURI(req.URL.Path)
	canonicalQueryString := canonicalQueryValues(req.URL.Query())

	canonicalReq := CanonicalRequest(
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	)

	hashedCanonicalReq := HashSHA256([]byte(canonicalReq))
	credentialScope := CredentialScope(date, c.region, "s3")
	stringToSign := StringToSign(Algorithm, dateTime, credentialScope, hashedCanonicalReq)

	signingKey := DeriveSigningKey(c.secretKey, date, c.region, "s3")
	signature := Sign(signingKey, stringToSign)

	authHeader := BuildAuthorizationHeader(Algorithm, c.accessKey, credentialScope, signedHeaders, signature)

	req.Header.Set("Authorization", authHeader)
	req.Header.Set("X-Amz-Date", dateTime)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.sessionToken)
	}

	return nil
}
