# SQL integration tests

This separate Go module contains actual SQLite and D1 fixture tests using the pure-Go `modernc.org/sqlite` driver, plus optional live PostgreSQL Blob tests using `lib/pq`. Its dependencies stay outside the main library module. Neither SQL driver requires cgo.

```sh
cd integration
CGO_ENABLED=0 go test ./...
go vet ./...
```

With cgo available, also run `go test -race ./...`. The Go race detector itself requires cgo.

The module replaces `github.com/shibukawa/yuruidb` with the parent checkout. Both SQLite clients use a real database file, and the D1 fixture executes SQL against real SQLite. Cloud fixtures do not establish live service compatibility.

Set `YURUIDB_POSTGRES_DSN` to enable the live PostgreSQL Blob tests; otherwise those subtests skip. The database user must be able to create and drop tables. Tests use uniquely named tables and clean them up afterward.

```sh
YURUIDB_POSTGRES_DSN='host=127.0.0.1 port=5432 user=postgres dbname=postgres sslmode=disable' go test -race -run TestSQLBlob ./...
```

SQLite and PostgreSQL run the same Blob conformance as bbolt/S3/R2/GCS: streaming, concurrent `ReadAt`, listing and cursors, conditional and unconditional deletion, cancellation, and stale readers after ID recreation. Additional tests check native binary columns, arbitrary bytes and empty documents, failed input, and concurrent creation. SQLite also checks record/Blob isolation and injected DB ownership. PostgreSQL record conformance is not covered by these Blob tests.
