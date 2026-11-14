# Implementation checks

CI runs `scripts/check.sh` with Go 1.26.4: gofmt, installer unit tests, zero-warning
go vet, the full race suite and all three binary builds. Architecture tests cap
Go files at 1,000 lines and packages at 4,000 lines, including tests. They enforce
runtime import edges and prohibit reverse engine-to-CLI/service dependencies.

The engine owns lease/lifecycle transactions. `internal/output` owns bounded
capture and aggregate log admission; the engine supplies current reservations. `internal/execution` implements the
host process fence without importing the engine. `internal/setup` reads bounded
repository configuration and stores setup receipts through database/sql; the
engine alone decides when successful preparation is eligible to commit.
`internal/retention` and `internal/maintenance` separate exact-file collection
from approved policy scheduling. The root quota helper remains a fixed logical
allocator and empty-directory retire operation, never a recursive collector.

External runtime imports are pinned in go.mod/go.sum and restricted by adapter:

- modernc.org/sqlite v1.62.0: engine SQL adapter and service receipt adapter only.
- golang.org/x/sys/unix: Darwin Unix-peer credentials only.
- github.com/pelletier/go-toml/v2 v2.4.3: setup configuration decoder only.

Other runtime code uses the standard library. No dependency version floats in
CI. Schema 8 protects setup receipts and preparation handoffs; older supported state upgrades retain
history and pinned quota metadata. Unknown versions fail closed. Active older
supervisors must be stopped with their installed binary before migration.

Filesystem/Git tests cover dirty-work preservation, lease ownership, confirmed
remote checkpoints, handoff and recovery. Disposable Linux drills cover actual
cgroup execution, full-state failures, daemon interruption, XFS byte/inode limits,
reviewed archive/reap, retention and systemd delegation. The corresponding docs
state which behavior is verified and what remains outside each checkpoint.
