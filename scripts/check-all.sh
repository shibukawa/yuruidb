#!/bin/sh
# Run from the repository root. SQLite integration tests require cgo.
set -eu
go test -race ./...
go vet ./...
(cd integration && go test -race ./... && go vet ./...)
python3 scripts/check-builds.py
go run ./examples/basic
