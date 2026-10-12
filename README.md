# bares3

`bares3` is a small dependency-free Go client for S3-compatible object storage.

## Creating a client

`NewClient` validates the endpoint and returns an error when configuration cannot
be initialized:

```go
client, err := bares3.NewClient(bares3.ClientParams{
    Endpoint:  "https://s3.example.com",
    Region:    "us-east-1",
    AccessKey: "access-key",
    SecretKey: "secret-key",
})
if err != nil {
    return err
}
```

Remote endpoints must be absolute URLs with a scheme and host and must not
contain a path prefix. A `file://` endpoint opens a local filesystem root; an
error opening that root is returned by `NewClient`.

A `file://` client holds an open directory handle, so call `Close` when the
client is no longer needed. `Close` is a no-op for remote clients and safe to
call more than once. Bodies already returned by `GetObject` stay readable
afterwards.

A client is safe for concurrent use except for `SetBucket`, which mutates
shared state; set the default bucket once, before the client is shared.

For deterministic signing tests, `ClientParams.Now` may be supplied. It is not
needed in normal applications.

## Timeouts

The default HTTP client sets no whole-transfer timeout, because
`http.Client.Timeout` bounds the entire exchange and would abort any legitimate
large upload or download. Stalls are bounded instead by transport-level limits:
a 10s dial timeout, a 10s TLS handshake timeout, and a 60s
`ResponseHeaderTimeout`. Total time for a request is bounded by the context you
pass:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
defer cancel()
obj, err := client.GetObject(ctx, bares3.GetObjectInput{Bucket: bucket, Key: key})
```

Supply your own `ClientParams.HTTPClient` to change any of this.

## `PutObject` payload length and streaming

`PutObjectInput.ContentLength` controls request framing:

- `Body == nil` or `ContentLength == 0`: sends an empty body.
- `ContentLength > 0`: streams exactly that many bytes and sets the HTTP
  `Content-Length` header. Extra bytes are rejected while streaming.
- `ContentLength < 0`: streams an unknown-length body using HTTP chunked
  transfer and SigV4 `UNSIGNED-PAYLOAD`.
- `ContentSHA256 != ""`: uses the supplied hash in SigV4 and verifies the
  streamed bytes against it without buffering the object.

Uploads are never copied into an in-memory buffer. For unknown-length sources,
use `ContentLength: -1`. Supplying `ContentSHA256` allows a streamed upload to
use a normal signed payload hash; the hash is checked as the request body is
consumed. If no hash is supplied, the remote backend uses `UNSIGNED-PAYLOAD`.
The local backend accepts the same input and writes the body directly to its
file.

Once a `PutObject` passes validation, both backends consume the body and close
it if it implements `io.Closer`; do not reuse it. A request rejected during
validation (unknown bucket, malformed `ContentSHA256`) returns without
touching the body.

## Range requests and conditional reads

`GetObject` and `HeadObject` accept the same read options on every backend.

A single byte range is served as a partial result: `ContentLength` reports the
size of the returned slice, `ContentRange` reports `bytes <start>-<end>/<total>`,
and `AcceptRanges` is `bytes`. Ranges are clamped at the end of the object, so
`bytes=6-100` on an 11 byte object returns the final 5 bytes. A range that
starts past the end of the object is not satisfiable and fails with
`ErrInvalidRange` (HTTP 416). Following RFC 9110 and Amazon S3, a range the
server cannot parse as a single byte range, such as `bytes=abc`, a non-`bytes`
range unit, or a multi-range request, is ignored and the whole object is
returned.

Conditional reads follow the precedence documented by Amazon S3: `If-Match`
overrides `If-Unmodified-Since`, and `If-None-Match` overrides
`If-Modified-Since`. Conditions are evaluated before the range, so a failed
condition short-circuits the request. `If-Match` and `If-None-Match` accept a
`*` wildcard and a comma-separated list of entity tags. A failed `If-Match` or
`If-Unmodified-Since` returns `ErrPreconditionFailed` (HTTP 412); a matched
`If-None-Match` or `If-Modified-Since` returns `ErrNotModified` (HTTP 304).

Because HTTP 304 is not a success status, both backends report it as an error.
Code that previously received an empty successful object for a conditional read
must now check for `ErrNotModified`.

## Local backend

The local backend is intended for development and deterministic tests, not as a
full filesystem replacement for S3. It currently provides bucket and object
operations, sorted listing, `StartAfter`, `MaxKeys` and `ContinuationToken`
paging, byte-range reads, and conditional reads. Object keys are represented as
filesystem paths. The local backend rejects absolute keys and keys containing
`..` path components to prevent filesystem escapes.

Listing a bucket that does not exist returns `ErrNoSuchBucket`, matching the
remote backend. A truncated page sets `NextContinuationToken` to the last key on
the page; pass it back as `ContinuationToken` to continue. `ContinuationToken`
is echoed back on every page.

Object writes are atomic: the data goes to a temporary file that is renamed
into place, so a failed write leaves no partial object and never truncates a
pre-existing one. A process killed mid-write can leave its `.put-*` temporary
file visible in listings; such a file is the leftover of an interrupted write
and is safe to delete. Empty directories also appear in listings as common
prefixes, which S3 never does.

The local backend does not persist or emulate the remaining S3 features. The
following inputs are accepted and ignored: user metadata, tags, ACLs, storage
class, website redirect location, SSE settings and `SSECustomer*` headers,
`VersionId`, the `response-*` content overrides, and checksum fields. Object
ETags are the MD5 of the whole file and are computed by re-reading it on every
read. Ignored inputs are not reported as errors: the backend serves the stored
object without the requested attribute, so client code must not depend on them
locally.

Remote integration tests are skipped by default. Set `REMOTE_TEST` and provide
the untracked `config.json` file to run them. Do not commit credentials.
