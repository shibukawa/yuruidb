#!/bin/sh
# Run from the repository root. All tests can run without cgo.
set -eu
if [ "$(go env CGO_ENABLED)" = 1 ]; then
  go test -race ./...
  (cd integration && go test -race ./...)
else
  go test ./...
fi
go vet ./...
(cd integration && CGO_ENABLED=0 go test ./... && go vet ./...)
python3 scripts/check-builds.py
go run ./examples/basic
