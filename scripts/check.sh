#!/bin/sh
set -eu
if [ "$(id -u)" -eq 0 ]; then
  echo "Ordinary checks require a non-root user; the CI conductor is scripts/check-ci.py" >&2
  exit 1
fi
# Proposed reader codec/model/mockup checks are development-only, inside the ordinary CI suite.
node -e 'const min=require("node:fs").readFileSync(".node-version","utf8").trim().split(".").map(Number), actual=process.versions.node.split(".").map(Number); if(actual.findIndex((n,i)=>n!==min[i])>=0 && actual[actual.findIndex((n,i)=>n!==min[i])]<min[actual.findIndex((n,i)=>n!==min[i])]) throw new Error("use Node >= "+min.join("."))'
node --test web/*.test.mjs
unformatted=$(gofmt -l cmd internal)
if [ -n "$unformatted" ]; then printf 'Run gofmt on:\n%s\n' "$unformatted"; exit 1; fi
# Zero-warning ratchet: any vet diagnostic fails; there is no grandfathered baseline.
python3 -m unittest discover -s deploy/linux -p '*_test.py'
go vet ./...
go test -race ./...
go build -o /tmp/superbench-check-bin ./cmd/bench
go build -o /tmp/superbench-check-quota ./cmd/bench-quota

go build -o /tmp/superbench-check-daemon ./cmd/benchd
