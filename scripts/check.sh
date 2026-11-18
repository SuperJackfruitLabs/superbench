#!/bin/sh
set -eu
if [ "$(id -u)" -eq 0 ]; then
  echo "Ordinary checks require a non-root user; the CI conductor is scripts/check-ci.py" >&2
  exit 1
fi
# Proposed reader codec/model/mockup checks are development-only, inside the ordinary CI suite.
node - <<'NODE'
const minimum = require("node:fs").readFileSync(".node-version", "utf8").trim().split(".").map(Number);
const actual = process.versions.node.split(".").map(Number);
const different = actual.findIndex((part, i) => part !== minimum[i]);
if (different !== -1 && actual[different] < minimum[different]) throw new Error("use Node >= " + minimum.join("."));
NODE
node --test web/*.test.mjs
unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then printf 'Run gofmt on:\n%s\n' "$unformatted"; exit 1; fi
# Zero-warning ratchet: any vet diagnostic fails; there is no grandfathered baseline.
python3 -m unittest discover -s deploy/linux -p '*_test.py'
python3 -m unittest discover -s scripts -p '*_test.py'
go vet ./...
go test -race ./...
go build -o /tmp/superbench-check-bin ./cmd/bench
go build -o /tmp/superbench-check-quota ./cmd/bench-quota

go build -o /tmp/superbench-check-daemon ./cmd/benchd
# The privilege-boundary executable deliberately has no cgo implementation.
GOOS=linux CGO_ENABLED=0 go build -o /tmp/superbench-check-payload ./cmd/bench-payload
# Every command must build for both shipped Linux architectures.
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/...
