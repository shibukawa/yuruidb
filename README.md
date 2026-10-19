# yuruidb

[English](README.md) | [日本語](README.ja.md)

A Go record storage library with interchangeable backends. It provides CRUD by ID and creation-ordered listing using UUIDv7 keys. Every backend is statically imported; `aws`, `gcp`, and `cloudflare` build tags enable groups of those imports.

Cloud drivers use the standard library's HTTP client without cloud SDK dependencies. The core also imports only standard library packages. The bbolt driver depends on bbolt, and the SQLite Go driver is used only in tests.

The module path is `github.com/shibukawa/yuruidb`. The module declares Go 1.23; verification was performed with Go 1.27.

## Usage

Import the driver you need. A blank import registers its factory; `Open` creates the store explicitly.

```go
import (
    "context"
    "github.com/shibukawa/yuruidb"
    _ "github.com/shibukawa/yuruidb/drivers/bbolt"
)

ctx := context.Background()
store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{
    "Path": "records.db",
    "Collection": "notes",
})
// Handle each error in application code.
defer store.Close()

id, err := yuruidb.NewID()
record, err := store.Insert(ctx, id, []byte("hello"))
record, err = store.Update(ctx, id, []byte("hello again"), record.Revision)
record, err = store.Get(ctx, id)
page, err := store.List(ctx, yuruidb.ListOptions{Limit: 100})
err = store.Delete(ctx, id, record.Revision)
```

For a runnable example with error handling, see [examples/basic](examples/basic/main.go) and run `go run ./examples/basic`. Each driver also exposes a constructor and a typed `Config` for use through a named import.

## Provider build tags

Add `import _ "github.com/shibukawa/yuruidb/backends"` to the application and build with a tag such as `go build -tags aws`. Tagged Go files statically import the selected drivers.

| Tag | Registered driver names |
| --- | --- |
| None | None |
| `aws` | `s3`, `dynamodb` |
| `gcp` | `gcs`, `firestore`, `firebase` |
| `cloudflare` | `r2`, `d1`, `cfkv` |
| Individual tags | `s3`, `dynamodb`, `gcs`, `firestore`, `firebase`, `r2`, `d1`, `cfkv`, `bbolt`, `sql`, `memory` |

The `firebase` tag imports both Firestore and Realtime Database. The driver name `firebase` refers to Realtime Database; the `firestore` tag selects Firestore alone. Tags can be combined without duplicate registration.

```sh
go run -tags aws ./examples/drivers
go run -tags 'gcs,r2' ./examples/drivers
```

The shared `yuruidb.Config` map lets tagged applications configure a driver without importing its package just to reference its configuration type. For example, an AWS application can pass:

```go
store, err := yuruidb.Open(ctx, "s3", yuruidb.Config{
    "Bucket": "my-bucket",
    "Collection": "notes",
    "Region": "ap-northeast-1",
})
```

Lambda is an intended execution environment. The `lambda` tag alone selects no storage backend; `-tags 'lambda,s3'` includes only S3. The application supplies its Lambda handler and runtime.

## Backends

| Driver | Main configuration fields | Conditional CRUD |
| --- | --- | --- |
| `memory` | `Collection` | Supported within one process |
| `bbolt` | `Path`, `Collection`, `Timeout` | Supported for a local file |
| `sql` | `DB`, `Dialect`, `Collection`, `AutoMigrate` | SQLite and PostgreSQL dialects implemented |
| `s3` | `Bucket`, `Region`, `Collection` | Conditional PUT; general purpose buckets |
| `dynamodb` | `Table`, `Region`, `Collection` | Conditional PutItem |
| `gcs` | `Bucket`, `Collection`, `Token` | Generation preconditions |
| `firestore` | `Project`, `Database`, `Collection`, `Token` | updateTime preconditions |
| `firebase` | `Endpoint`, `Collection`, `Token` | Realtime Database conditional PUT with ETags |
| `r2` | `Endpoint`, `Bucket`, `Collection`, `Credentials` | S3-compatible conditional PUT |
| `d1` | `AccountID`, `DatabaseID`, `Collection`, `Token`, `AutoMigrate` | SQL guarded by revision conditions |
| `cfkv` | `AccountID`, `NamespaceID`, `Collection`, `Token` | Reads, listing, and explicit unconditional mutations only |

HTTP drivers also accept injected `Client`, `Authorize`, and `Endpoint` values. Schema setup, authentication, limitations, and verification scope are described in the [implementation guide (Japanese)](docs/implementation.ja.md).

## Storage contract

- Values are `[]byte`. IDs are canonical lowercase UUIDv7 strings, allocated before `Insert`. Updates preserve the ID.
- Listing follows ascending ID order. The default limit is 100; supported limits are 1–1000. Continue until the returned `Cursor` is empty.
- UUIDv7 IDs do not establish exact generation or commit order across independent processes. Pagination does not provide a snapshot across pages.
- `Update` and `Delete` require the retrieved `Revision`. A stale revision returns `ErrConflict`; the library does not retry with a newer revision automatically.
- `Delete` writes a revision-bearing tombstone. A deleted ID can be recreated, and its earlier revisions remain invalid. Physical garbage collection is not implemented.
- `ErrOutcomeUnknown` on a remote mutation means the write may have committed. Read the same ID to reconcile the result.
- The CF KV driver's `Insert`, `Update`, and `Delete` return `ErrUnsupported`. Its concrete `Set` and `Remove` methods are unconditional operations.

## Extension and verification

Custom drivers implement `yuruidb.Store` and register a factory through `yuruidb.Register`. For atomic KVS backends, the public [adapter package](adapter/adapter.go) reuses the shared revision, tombstone, and cursor handling.

```sh
go test -race ./...
go vet ./...
python3 scripts/check-builds.py
```

bbolt and SQLite are tested against real local databases. Cloud drivers are tested against local HTTP protocol fixtures for CRUD, conditional writes, and pagination. The S3 path also has a separate-process contention test. AWS signatures are checked against published reference vectors. Live cloud services, real PostgreSQL, performance, pricing, and cold-start behavior remain unverified.

The [requirements (Japanese)](docs/requirements.ja.md) record the design basis. Structured requirements and implementation observations are maintained under `.knowledge/concepts/`.

## License

Copyright 2026 yuruidb contributors.

Licensed under the [Apache License 2.0](LICENSE).
