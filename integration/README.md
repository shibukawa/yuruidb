# SQLite integration tests

This separate Go module contains actual SQLite and D1 fixture tests, using the pure-Go `modernc.org/sqlite` driver. Its dependencies stay outside the main library module. Neither test suite requires cgo.

```sh
cd integration
CGO_ENABLED=0 go test ./...
go vet ./...
```

With cgo available, also run `go test -race ./...`. The Go race detector itself requires cgo.

The module replaces `github.com/shibukawa/yuruidb` with the parent checkout. Both SQLite clients use a real database file, and the D1 fixture executes SQL against real SQLite. Cloud fixtures do not establish live service compatibility.
