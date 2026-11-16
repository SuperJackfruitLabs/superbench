# Absent Fresh reservation retirement

`internal/freshretirement` is the storage primitive for retiring a failed Fresh
allocation after its fixed leaf is absent. It has no command, service or sudo
route. Fresh materialization remains disabled until the operator recovery owner
also binds original bootstrap provenance, permanent operation denial, actual
stopped-controller authority and a durable reviewed approval under the existing
operation lock.

The primitive requires direct root and takes the existing allocation lock. It
validates the complete canonical format-3 ledger and policy binding, then pins
the selected assignment and its exact Fresh marker separately from immutable
storage authority. An approval records the original allocation and the fixed
benches parent's device, inode, owner, group and mode. The parent must belong to
the dedicated data mount; a child mount cannot hide the real leaf. Only leaf
ENOENT below that existing pinned parent establishes absence.

Both XFS block and inode reports must uniquely identify zero usage. Nonzero
usage, open-but-unlinked files, malformed or duplicate rows, missing output,
changed nonzero hard caps and nonzero soft caps refuse retirement. An explicit
zero hard cap can represent interrupted limit initialization; it is accepted
only with a real unique zero-usage row. A never-initialized project that produces
no row remains parked for separately verified recovery. No missing record is
treated as a zero-usage proof.

Retirement changes only the selected row's byte and inode reservation to zero.
It retains the key, project ID, Fresh marker, next project counter and unrelated
allocations. An exact lost-reply retry uses the retained original approval,
rechecks current authority, absence and usage, and returns the same tombstone.
Legacy and Fresh allocation paths cannot reuse the retired key or project ID.
No pathname is deleted and no kernel quota is changed by this primitive.

The privileged lower fixture selector is
`TestFreshIndexedMaterializerLowerFixture` with build tag `freshstoragefixture`;
the added assertion is `LOWER_FRESH_ABSENT_RETIREMENT_AND_RETRY_PASS`. It tests the
storage layer under the actual indexed control lock on disposable XFS. It does
not establish bootstrap provenance for its synthetic failing initializer or
prove the future operator maintenance command or booted systemd gate. Cleanup
of nonempty allocations and recovery without sufficient retained evidence remain
separate unfinished work.
