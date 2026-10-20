# yuruidb

[English](README.md) | [日本語](README.ja.md)

A Go 1.27+ record storage library with interchangeable, statically imported backends. One `Store` owns a connection; generic methods select the registered data type and collection. JSON data uses `Record[T]` and `Page[T]`. IDs may be validated names or generated UUIDv7 strings. Listing uses ascending ID byte order.

Cloud drivers use standard-library HTTP, without cloud SDK dependencies. The core has no external imports. bbolt is an optional driver dependency; cgo SQLite tests live in the separate [integration module](integration).

The module path is `github.com/shibukawa/yuruidb`.

## Usage

```go
import (
    "context"
    "github.com/shibukawa/yuruidb"
    _ "github.com/shibukawa/yuruidb/drivers/bbolt"
)

type Job struct { Text string `json:"text"` }
type Waiting Job
type Done Job

ctx := context.Background()
store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{"Path": "spool.db"})
// Handle errors in application code.
defer store.Close()
err = store.Bind[Waiting]("jobs")
err = store.Bind[Done]("done")

id, err := yuruidb.NewID()
record, err := store.Insert(ctx, id, Waiting{Text: "hello"})
record, err = store.Get[Waiting](ctx, id)
record, err = store.Update(ctx, id, Waiting{Text: "updated"}, record.Revision())
page, err := store.List[Waiting](ctx, yuruidb.ListOptions{Limit: 100})
err = store.Delete[Waiting](ctx, id, record.Revision())
// record.ID(), record.Revision(), record.Data.Text
```

See [examples/basic](examples/basic/main.go) for runnable error handling. Drivers also expose `Open(ctx, Config)` through named imports. `Bind` validates registrations without I/O; the first operation creates its internal collection view. Unregistered types return `ErrInvalid`. Rebinding a type to another name or binding a different type to the same name is rejected. Pointer and value forms share a binding. `type Done Job` defines a distinct type; `type Done = Job` is an alias and shares Job's identity. Defined types retain fields/tags, but do not inherit Job's own methods.

## Data and concurrency

`T` can be any JSON-compatible struct, pointer, map, slice, scalar, `json.RawMessage`, or custom JSON type. Writes marshal and unmarshal before mutation, returning `ErrInvalid` for unsupported values. Use compatible schemas across clients. Returned data does not alias ordinary caller-owned maps/slices. Metadata is read-only in Go; record JSON retains `id`, `revision`, and `data`.

- IDs contain 1–256 ASCII letters, digits, `.`, `_`, or `-`; `.`, `..`, and Firestore-reserved `__…__` names are rejected. Case is preserved. `NewID()` remains a process-monotonic UUIDv7 generator. UUIDv7 order is not exact cross-process commit order; names sort by bytes (`10` before `2`). Updates preserve IDs. Pages have no snapshot guarantee.
- `Insert` never overwrites a live ID. It attempts conditional creation first, reading only on conflict. `Update` and `Delete` require a retrieved opaque revision. Revisions bind collection, ID, random write nonce, and native CAS token; current tokens avoid a pre-write GET. Backends that cannot return native tokens use readback when needed. Conflicts never adopt a newer revision automatically.
- `Delete` uses conditional physical deletion on capable backends. Others retain conditional tombstones. An ID may be recreated; earlier record revisions remain stale. Existing tombstones can be removed with `store.PurgeTombstones[T](ctx)` on capable backends. Bucket version history and bbolt file compaction are separate storage concerns.
- Value-bearing listing avoids per-item reads on bbolt, memory, SQL/D1, DynamoDB, and Firestore. S3/R2/GCS enumerate keys and read records; Firebase needs individual ETags. Listing may observe concurrent changes.
- bbolt shares a DB per normalized file path, including symlink resolution, with reference-counted close. Five types can use five buckets in one file. Values use a compact binary envelope without base64 wrapping. It remains a local file backend, not a cross-process remote store.

## Unknown outcomes

A conditional mutation may commit even if its response is lost. The record layer attempts a readback when context and consistency permit. If it finds the attempted nonce, it returns success. Otherwise, `errors.Is(err, yuruidb.ErrOutcomeUnknown)` remains true and `errors.As` exposes `*yuruidb.MutationError` with the operation, collection, ID, nonce, expected revision, and attempted revision.

```go
var attempt *yuruidb.MutationError
if errors.As(err, &attempt) {
    current, readErr := store.Get[Waiting](recoveryCtx, attempt.ID)
    if readErr == nil && attempt.MatchesRevision(current.Revision()) {
        // This exact attempted write is currently stored.
    }
}
```

A mismatch does not prove failure: another writer may already have superseded it. On eventually consistent KV, a matching read confirms visibility of that version, not global current state. Absence after physical deletion confirms the desired state, not which deletion committed. If the original context expired, reconcile with an explicit new context.

## Backends and authentication

| Driver | Main connection settings | Conditional record mutations |
| --- | --- | --- |
| `memory` | none | in-process |
| `bbolt` | `Path`, `Timeout` | local file transactions |
| `sql` | `DB`, `Dialect`, `AutoMigrate`, `CloseDB` | SQLite/PostgreSQL SQL dialects |
| `s3` | `Bucket`, `Region`, optional `Endpoint`, `AddressStyle` | ETag conditions |
| `dynamodb` | `Table`, `Region` | conditional PutItem/DeleteItem |
| `gcs` | `Bucket`, `Token` | generation conditions |
| `firestore` | `Project`, `Database`, `Token` | updateTime conditions |
| `firebase` | `Endpoint`, `Token` | ETag conditions, including deletion |
| `r2` | `Endpoint`, `Bucket`, `Credentials` | verified S3 conditions |
| `d1` | `AccountID`, `DatabaseID`, `Token`, `AutoMigrate` | conditional SQL |
| `cfkv` | `AccountID`, `NamespaceID`, `Token` | unsupported; explicit unconditional Set/Remove only |

Connection configs omit `Collection` for the typed Store; `OpenRaw` can still select one collection. HTTP clients and authorizers are injectable. AWS's default credential provider checks environment credentials, web identity/STS (EKS IRSA), container credentials (ECS/EKS Pod Identity), then EC2 IMDSv2, caching and refreshing temporary credentials. Profiles and SSO remain caller-injected. Region order is explicit config, `AWS_REGION`, `AWS_DEFAULT_REGION`, then `us-east-1`; R2 defaults to `auto`.

AWS S3 uses virtual hosting by default for suitable bucket names; dotted names use path style for TLS compatibility. `AddressStyle` can be `auto`, `virtual`, or `path`. Custom endpoints default to path style. Opening a custom S3/R2 endpoint creates temporary probe keys, verifies conditional creation/replacement, concurrent CAS, and conditional deletion, then cleans up. IAM must allow these probe operations. Ignored PUT conditions fail `Open`; unsupported/ignored DELETE conditions select tombstones. Set `VerifyConditions: true` to probe AWS endpoints as well. `s3.Verify`/`r2.Verify` expose a diagnostic report. Probes are bounded observations, not a proof of every service guarantee.

Transient HTTP 429/500/502/503/504 responses receive bounded backoff with jitter and fresh authorization. Only conditional mutations are replayed, with unchanged body and condition. Unconditional KV writes and D1 mutations are not blindly retried. `Retry-After` is respected; long minimum delays return the error. JSON responses are limited to 32 MiB; provider limits and encoding overhead can be smaller. Oversized S3/GCS record writes are rejected before upload.

## Streaming documents

`s3.OpenBlobs`, `r2.OpenBlobs`, and `gcs.OpenBlobs` open a separate, explicitly named collection for immutable documents. `Insert(ctx, id, io.Reader)` spools input to a temporary file for bounded-memory signing/replay; `Open(ctx, id)` returns a streaming reader the caller must close. The JSON response limit does not apply. Store the returned ID/version reference in a small typed record. Use fresh IDs; native blob ETags can repeat for identical bytes, unlike random record revisions. Conditional blob `Delete` requires its returned version and backend support. Document and record operations do not form a transaction.

```go
blobs, err := s3.OpenBlobs(ctx, s3.Config{Bucket: "spool", Collection: "documents"})
meta, err := blobs.Insert(ctx, documentID, sourceReader)
reader, err := blobs.Open(ctx, meta.ID)
defer reader.Close()
```

## Static imports and build tags

Import chosen drivers directly, or import `_ "github.com/shibukawa/yuruidb/backends"` with profiles:

| Tags | Drivers |
| --- | --- |
| `aws` | s3, dynamodb |
| `gcp` | gcs, firestore, firebase |
| `cloudflare` | r2, d1, cfkv |
| individual driver names | the selected drivers |

`firebase` also imports Firestore. Tags combine without duplicate registration. `lambda` alone selects no backend. `go run -tags aws ./examples/drivers` prints included drivers. Tags select imports, not runtime credentials or endpoints.

## Extensions and testing

[adapter.New](adapter/adapter.go) wraps an atomic backend with record semantics; call `Bind[T](config.Collection)` before use. Optional `ItemWriter`, `Scanner`, `Deleter`, and `CollectionTransport` interfaces enable native tokens, value listing, physical deletion, and shared collection views. `OpenRaw` and `Wrap(raw)` retain the byte-oriented extension boundary.

Firebase hex-encodes physical IDs with a nonnumeric prefix to support names and byte ordering. SQL cursors/revisions cannot distinguish different injected databases unless a distinct `Identity` is supplied.

```sh
go test -race ./...
go vet ./...
(cd integration && go test -race ./...)
python3 scripts/check-builds.py
```

The root module has no cgo requirement. The integration module alone depends on mattn/go-sqlite3. Tests exercise actual bbolt/SQLite and independent HTTP fixtures, including response loss, stale writers after recreation, multi-collection access, 2000-to-10 record deletion, cloud credential flows, compatibility probes, and 34 MiB streaming. Live cloud services, actual PostgreSQL, IAM, and production performance remain unverified. See the [implementation guide](docs/implementation.ja.md) and [validation history](docs/implementation-validation.md).
