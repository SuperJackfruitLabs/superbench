#!/bin/sh
set -eu
unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then printf 'Run gofmt on:\n%s\n' "$unformatted"; exit 1; fi
# Zero-warning ratchet: any vet diagnostic fails; there is no grandfathered baseline.
go vet ./...
go test -race ./...
go build -o /tmp/superbench-check-bin ./cmd/bench
go build -o /tmp/superbench-check-quota ./cmd/bench-quota

go build -o /tmp/superbench-check-daemon ./cmd/benchd
