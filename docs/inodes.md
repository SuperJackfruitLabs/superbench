# Inode quotas and migration

Managed XFS policies bound file counts as well as bytes. Per-bench, shared-store
and shared-mirror projects each have an inode hard limit. Reservations include
both shared caps, every unfinished active bench allocation and an inode reserve;
new assignments refuse overcommit against the filesystem's reported capacity.
These are logical reservations, not extra physical capacity from reflinks.

The root-owned policy requires explicit positive counts (`1..2^40`):

```json
{
  "BenchInodes": 250000,
  "StoreInodes": 2000000,
  "MirrorInodes": 1000000,
  "ReserveInodes": 250000
}
```

These fields accompany the byte/root/ownership settings in [storage.md](storage.md).
Choose values for the actual dedicated filesystem and workload. Omitted/zero
counts are refused; there is no silent unlimited default. Root policy owns the
numbers. A worker can request only its fixed logical project, not another cap.

The helper journals each assignment before setting XFS byte/inode hard limits,
then reads both back. Lease/run admission rechecks the target bench's pinned
limits, alongside the shared stores; a healthy store quota alone does not prove
the bench is bounded. Status includes `QuotaInodes` (`quota_inodes` in socket
responses). Unmanaged hosts do not claim per-project inode enforcement.

An inode-full bench cannot create another zero-byte file even if bytes remain.
A different project can still use its own reservation. This does not give a run
additional identities, a sandbox or authority to write another workspace.

## Upgrade a byte-only managed host

Stop workers and reconcile active supervised executions with the installed
binary before changing policy/helper/state. Back up authoritative state and the
privileged ledger. Deploy the reviewed new helper at the same root-owned helper
path; the fresh installer is not an unattended binary updater.

Add reviewed counts to the root-owned policy. Inspect current XFS usage first.
Normal `apply` refuses existing ledger entries without inode metadata, rather
than silently adopting new limits. Direct root can explicitly migrate them:

```sh
bench-quota --policy /etc/superbench/storage.json migrate-inodes
```

Policy overrides and inode migration are refused to sudo/worker callers. The
command keeps byte caps, paths and project IDs fixed; it changes only inode caps.
It refuses a cap below current usage and refuses total inode overcommit. Retired
projects stay retired with zero reservations. Each target is journaled before
kernel effects; rerunning verifies/reapplies every active target, including any
that a prior interrupted attempt already recorded. A partially migrated ledger
is not a success claim. Normal growth fails closed until its allocations agree
with the reviewed policy. Do not run competing workers/migrations during upgrade.

If a candidate cap is too small, preserve the files and revise the root policy;
never delete unknown data or erase the ledger just to make migration pass. A
reviewed direct-root rerun also supports later inode-cap changes without changing
byte caps or IDs. Normal allocation requests cannot make that policy change.

After helper migration succeeds, run the new core binary as the service owner:

```sh
bench migrate --state /var/lib/superbench/state --data /srv/superbench-data
```

State schema 5 pins inode limits for existing managed benches after matching the
original ID/byte cap and verified helper result. An unconfirmed allocation or an
active old supervisor refuses the upgrade. Reaped historical records and retained reaping intents need no new allocation.
Their legacy inode cap can remain unrecorded (zero); archived/reaping records
cannot acquire another writer, and reap still verifies its retained approval and
privileged retirement. This preserves restart recovery after a lost retirement
reply instead of trying to recreate a retired project. Earlier core binaries refuse schema 5; older helpers reject the new
policy fields. Do not roll back just the executable or policy against upgraded
state. Restart the reviewed service only after checking host status and retained
leases. No migration expires or releases a lease by time.

## Reconciliation and retirement

Known allocations can still be enforced and reconciled when the filesystem is
low. Releasing a bench reservation still requires empty approved data and zero
XFS block **and inode** usage. Open-but-unlinked zero-byte files retain inodes;
reap preserves their reservation until they close. A retired ledger entry records
both `Bytes: 0` and `Inodes: 0`, and its ID/key is never reused. See [archive and
reap](archive.md) for approval and restart rules. Automatic retention is separate.

## Verification

Local tests cover mandatory/bounded policy counts, ledger drift, explicit legacy
inspection, pinned bench admission, state upgrade and conflicting allocation
refusal. The disposable Linux drill in `scripts/test-inodes.sh` and
`scripts/test-inodes.py` builds on the previous byte-only checkpoint and verifies:

- Explicit resumable migration preserves IDs and refuses undersized candidates;
  old binaries and sudo callers cannot silently migrate the host.
- Zero-byte files hit actual per-bench and both shared-project hard limits.
- Lease/run admission restores and reads back a removed target limit before work.
- Total inode overcommit refuses before adding a project to the ledger.
- Reviewed reap retires both reservations and recovery gets a new bounded ID.

The test uses a private mount namespace, a disposable 512 MiB XFS image and a
unique cgroup subtree. It never changes an installed service or production volume.

Recorded kernel results: [inodes-linux-output.txt](inodes-linux-output.txt).
The full required suite also passes on macOS and Linux, including real Unix
sockets, race checks, state migrations and installer safety tests.

The pre-existing [storage regressions](inodes-storage-regression.txt) also pass
with inode budgets: frozen setup/offline reuse, byte caps, allocation/clone
interruption, filesystem identity and low-space behavior. Archive crash and
open-inode regressions are included in the final kernel output above.
