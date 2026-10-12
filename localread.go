package bares3

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// limitedBody bounds a reader to a byte range while keeping the closer of the
// underlying resource, so a ranged read still releases the file handle.
type limitedBody struct {
	io.Reader
	io.Closer
}

// resolveByteRange maps an HTTP Range value onto the inclusive byte interval
// [start,end] of an object of the given size.
//
// ok is false when the range must be ignored and the whole object served. That
// covers a missing or non-"bytes" range unit, a multi-range request, and any
// value that is not a single well-formed byte range, which matches RFC 9110
// and Amazon S3. err is non-nil when the range is well-formed but cannot be
// satisfied, in which case the caller must fail the request with
// ErrInvalidRange.
func resolveByteRange(spec string, size int64) (start, end int64, ok bool, err error) {
	value := strings.TrimSpace(spec)
	unit, specs, found := strings.Cut(value, "=")
	if !found || !strings.EqualFold(strings.TrimSpace(unit), "bytes") {
		return 0, 0, false, nil
	}
	specs = strings.TrimSpace(specs)
	if specs == "" || strings.Contains(specs, ",") {
		return 0, 0, false, nil
	}

	first, last, found := strings.Cut(specs, "-")
	if !found {
		return 0, 0, false, nil
	}
	first = strings.TrimSpace(first)
	last = strings.TrimSpace(last)

	var from, to int64
	switch {
	case first == "":
		// A suffix range asks for the final N bytes of the object.
		suffix, convErr := strconv.ParseInt(last, 10, 64)
		if convErr != nil || suffix < 0 {
			// A negative suffix is not a valid range-spec, so it is ignored
			// like any other malformed range rather than rejected.
			return 0, 0, false, nil
		}
		if suffix == 0 {
			return 0, 0, false, invalidRangeError()
		}
		if suffix < size {
			from = size - suffix
		}
		to = size - 1
	case last == "":
		var convErr error
		if from, convErr = strconv.ParseInt(first, 10, 64); convErr != nil || from < 0 {
			return 0, 0, false, nil
		}
		to = size - 1
	default:
		var convErr error
		if from, convErr = strconv.ParseInt(first, 10, 64); convErr != nil || from < 0 {
			return 0, 0, false, nil
		}
		if to, convErr = strconv.ParseInt(last, 10, 64); convErr != nil || to < 0 {
			return 0, 0, false, nil
		}
		if to >= size {
			to = size - 1
		}
	}

	// An empty object satisfies no range at all, and neither does a start past
	// the end or a range whose end precedes its start.
	if from >= size || to < from {
		return 0, 0, false, invalidRangeError()
	}
	return from, to, true, nil
}

func formatContentRange(start, end, size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", start, end, size)
}

// checkLocalPreconditions evaluates the conditional read headers against an
// object's ETag and modification time using the precedence Amazon S3
// documents: If-Match overrides If-Unmodified-Since, and If-None-Match
// overrides If-Modified-Since.
//
// modTime is truncated to whole seconds because HTTP dates have no sub-second
// precision while filesystem timestamps do. Evaluating against an untruncated
// timestamp would reject a conditional read of an object written in the same
// second as the supplied date.
func checkLocalPreconditions(etag string, modTime time.Time, ifMatch, ifNoneMatch string, ifModifiedSince, ifUnmodifiedSince *time.Time) error {
	modified := modTime.Truncate(time.Second)

	if ifMatch != "" {
		if !etagConditionMatches(ifMatch, etag) {
			return preconditionFailedError()
		}
	} else if ifUnmodifiedSince != nil {
		if modified.After(ifUnmodifiedSince.Truncate(time.Second)) {
			return preconditionFailedError()
		}
	}

	if ifNoneMatch != "" {
		if etagConditionMatches(ifNoneMatch, etag) {
			return notModifiedError()
		}
	} else if ifModifiedSince != nil {
		if !modified.After(ifModifiedSince.Truncate(time.Second)) {
			return notModifiedError()
		}
	}

	return nil
}

// etagConditionMatches evaluates an If-Match or If-None-Match value, which may
// be a "*" wildcard or a comma-separated list of entity tags.
func etagConditionMatches(condition, etag string) bool {
	for _, candidate := range strings.Split(condition, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}
