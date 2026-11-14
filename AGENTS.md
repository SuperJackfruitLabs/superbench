# SuperBench prototype

- Engine in internal/bench; cmd/bench parses CLI flags and renders JSON only.
- State stays outside bench data. Never expire a lease just because time passed.
- Leases coordinate trusted callers; do not claim filesystem isolation.
- Checkpoints require clean committed work and remote confirmation. Never force-push,
  auto-commit unknown files, or reset/delete work to make recovery succeed.
- No suite consumer contracts are implemented here. Shared contracts need treaty and
  charter review before integration. No production deployment or tags.
- Run `sh scripts/check.sh` before publishing. The sole external runtime dependency is the pinned SQLite driver, imported only by sqlite.go.
