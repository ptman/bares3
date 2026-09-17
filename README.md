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

For deterministic signing tests, `ClientParams.Now` may be supplied. It is not
needed in normal applications.

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

## Local backend

The local backend is intended for development and deterministic tests, not as a
full filesystem replacement for S3. It currently provides bucket and object
operations, sorted listing, `StartAfter`, and bounded `MaxKeys` listing. Object
keys are represented as filesystem paths. The local backend rejects absolute
keys and keys containing `..` path components to prevent filesystem escapes.
Local object metadata such as user metadata, tags, and encryption settings is
not persisted.

Remote integration tests are skipped by default. Set `REMOTE_TEST` and provide
the untracked `config.json` file to run them. Do not commit credentials.
