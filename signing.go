package bares3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	Algorithm   = "AWS4-HMAC-SHA256"
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	DateFormat  = "20060102"
	TimeFormat  = "20060102T150405Z"
)

// HashSHA256 returns the hex-encoded SHA-256 hash of data.
func HashSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// CanonicalRequest builds a canonical request string per AWS SigV4 spec.
// See https://docs.aws.amazon.com/general/latest/gr/sigv4-create-canonical-request.html
func CanonicalRequest(method, canonicalURI, canonicalQueryString, canonicalHeaders, signedHeaders, payloadHash string) string {
	return strings.Join([]string{
		method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")
}

// CanonicalURI returns the URI-encoded path with each segment double-encoded.
func CanonicalURI(path string) string {
	if path == "" || path == "/" {
		return "/"
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	encoded := make([]string, len(parts))
	for i, p := range parts {
		encoded[i] = uriEncode(p, false)
	}
	return "/" + strings.Join(encoded, "/")
}

func canonicalQueryValues(values url.Values) string {
	type param struct{ name, value string }
	params := make([]param, 0, len(values))
	for name, entries := range values {
		for _, value := range entries {
			params = append(params, param{uriEncode(name, true), uriEncode(value, true)})
		}
	}
	sort.Slice(params, func(i, j int) bool {
		if params[i].name != params[j].name {
			return params[i].name < params[j].name
		}
		return params[i].value < params[j].value
	})
	out := make([]string, len(params))
	for i, p := range params {
		out[i] = p.name + "=" + p.value
	}
	return strings.Join(out, "&")
}

// CanonicalHeaders returns the canonical headers string and signed headers list.
// headers must be sorted by key (lowercase). Each header is "key:value\n".
func CanonicalHeaders(headers map[string]string) (canonicalHeaders, signedHeaders string) {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	signedParts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(headers[k])
		parts = append(parts, k+":"+v)
		signedParts = append(signedParts, k)
	}
	canonicalHeaders = strings.Join(parts, "\n") + "\n"
	signedHeaders = strings.Join(signedParts, ";")
	return canonicalHeaders, signedHeaders
}

// StringToSign builds the string to sign per AWS SigV4 spec.
func StringToSign(algorithm, dateTime, credentialScope, hashedCanonicalRequest string) string {
	return strings.Join([]string{
		algorithm,
		dateTime,
		credentialScope,
		hashedCanonicalRequest,
	}, "\n")
}

// CredentialScope returns the credential scope string.
func CredentialScope(date, region, service string) string {
	return strings.Join([]string{date, region, service, "aws4_request"}, "/")
}

// DeriveSigningKey derives the signing key per AWS SigV4 spec.
func DeriveSigningKey(secretKey, date, region, service string) []byte {
	hmacHash := func(data string, key []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(data))
		return h.Sum(nil)
	}
	kDate := hmacHash(date, []byte("AWS4"+secretKey))
	kRegion := hmacHash(region, kDate)
	kService := hmacHash(service, kRegion)
	kSigning := hmacHash("aws4_request", kService)
	return kSigning
}

// Sign computes the signature from signing key and string to sign.
func Sign(signingKey []byte, stringToSign string) string {
	h := hmac.New(sha256.New, signingKey)
	h.Write([]byte(stringToSign))
	return hex.EncodeToString(h.Sum(nil))
}

// BuildAuthorizationHeader builds the Authorization header value.
func BuildAuthorizationHeader(algorithm, accessKey, credentialScope, signedHeaders, signature string) string {
	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, accessKey, credentialScope, signedHeaders, signature)
}

// uriEncode encodes a string per AWS SigV4 spec.
// If query is true, slash is encoded as required for query parameters.
func uriEncode(s string, query bool) string {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		b := s[i]
		if isAllowedChar(b, query) {
			buf.WriteByte(b)
		} else {
			fmt.Fprintf(&buf, "%%%02X", b)
		}
	}
	return buf.String()
}

func isAllowedChar(b byte, allowSlash bool) bool {
	if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') {
		return true
	}
	switch b {
	case '_', '-', '~', '.':
		return true
	case '/':
		return false
	}
	return false
}
