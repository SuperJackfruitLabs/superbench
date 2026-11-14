# SuperBench prototype

- Engine in internal/bench; host quota policy in internal/hoststorage; CLI front
  doors in cmd/bench and cmd/bench-quota. The helper accepts fixed-policy logical
  allocations only, never arbitrary filesystem paths, limits or XFS commands.
- State stays outside bench data. Never expire a lease just because time passed.
- Leases coordinate trusted callers; do not claim filesystem isolation.
- Checkpoints require clean committed work and remote confirmation. Never force-push,
  auto-commit unknown files, or reset/delete work to make recovery succeed.
- No suite consumer contracts are implemented here. Shared contracts need treaty and
  charter review before integration. No production deployment or tags.
- Run `sh scripts/check.sh` before publishing. External runtime imports are the pinned SQLite driver in storage adapters and
  x/sys/unix in the macOS peer-credential adapter. Protocol/client/service layers
  follow the edges in the architecture test; the engine never imports the service.

- Linux payloads live in deploy/linux. The installer stages files, preserves changed
  configuration and never starts/enables a service. Managed sudo permits only the
  fixed helper; keep state/ledger separate from bounded data. Check scripts require
  Python 3 as well as Go; privileged installation drills use disposable hosts only.
