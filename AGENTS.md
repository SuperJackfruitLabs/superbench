# SuperBench prototype

- Engine in internal/bench; host quota policy in internal/hoststorage; CLI front
  doors in cmd/bench and cmd/bench-quota. The helper accepts fixed-policy logical
  allocations and empty-directory retirement only, never arbitrary filesystem
  paths, limits or XFS commands.
- State stays outside bench data. Never expire a lease just because time passed.
- Leases coordinate trusted callers; do not claim filesystem isolation.
- Checkpoints require clean committed work and remote confirmation. Never force-push,
  auto-commit unknown files, or reset/delete work to make recovery succeed.
- No suite consumer contracts are implemented here. Shared contracts need treaty and
  charter review before integration. No production deployment or tags.
- Run `sh scripts/check.sh` before publishing. External runtime imports are the pinned SQLite driver in storage adapters and
  x/sys/unix in the macOS peer-credential adapter. Protocol/client/service layers
  follow the edges in the architecture test; the engine never imports the service. The reap layer only snapshots/verifies
  approved directory entries; the engine owns lifecycle and intent authority.

- Linux payloads live in deploy/linux. The installer stages files, preserves changed
  configuration and never starts/enables a service. Managed sudo permits only the
  fixed helper; keep state/ledger separate from bounded data. Check scripts require
  Python 3 as well as Go; privileged installation drills use disposable hosts only.

- Archive/reap must preserve remote checkpoint evidence. Reap requires a concrete
  reviewed manifest; retry deletes only unchanged remaining approved entries.
  Quota release removes only an empty fixed logical directory and retires its ID
  after XFS reports zero blocks/inodes. Never recursively delete as root.

- Managed policy bounds bytes and inodes explicitly. Never adopt changed caps
  through normal worker allocation. Inode migration is direct-root only, preserves
  IDs/byte caps and verifies each target. State 5 pins inode metadata; old active
  supervisors must be stopped with their installed binary before upgrading.

- Run history contains no lease tokens or command arguments. Log collection is
  explicit reviewed file or bounded-policy approval, restricted to released/sealed owned files, and
  journals approval before unlink. Keep writer locks until capture closes.
  Canceling increments approval revisions; never revive a pruned/canceled plan.
  Legacy adoption is separate operator evidence, never a fabricated run outcome.
- Receipt IDs are replay protection. Log/history collection must never delete
  service receipts or make an interrupted command executable again.
