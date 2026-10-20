# SQLite integration tests

This separate Go module contains actual SQLite and D1 fixture tests. Only this module depends on the cgo driver `github.com/mattn/go-sqlite3`. The main library and its test suite do not require cgo.

```sh
cd integration
go test -race ./...
go vet ./...
```

The module replaces `github.com/shibukawa/yuruidb` with the parent checkout. Both SQLite clients use a real database file, and the D1 fixture executes SQL against real SQLite. Cloud fixtures do not establish live service compatibility.
