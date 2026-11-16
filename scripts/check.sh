#!/bin/sh
set -eu
if [ "$(id -u)" -eq 0 ]; then
  echo "Ordinary checks require a non-root user; the CI conductor is scripts/check-ci.py" >&2
  exit 1
fi
unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then printf 'Run gofmt on:\n%s\n' "$unformatted"; exit 1; fi
# Zero-warning ratchet: any vet diagnostic fails; there is no grandfathered baseline.
python3 -m unittest discover -s deploy/linux -p '*_test.py'
go vet ./...
go test -race ./...
go build -o /tmp/superbench-check-bin ./cmd/bench
go build -o /tmp/superbench-check-quota ./cmd/bench-quota

go build -o /tmp/superbench-check-daemon ./cmd/benchd
# The privilege-boundary executable deliberately has no cgo implementation.
GOOS=linux CGO_ENABLED=0 go build -o /tmp/superbench-check-payload ./cmd/bench-payload
