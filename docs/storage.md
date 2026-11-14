# Host storage management

Managed hosts use a dedicated XFS filesystem with reflinks and enforcing project
quotas. SQLite, creation intents, run logs and the privileged allocation ledger
stay outside it. The local CLI still works without managed storage; on such
hosts it applies free-space admission and bounded logs, without claiming per-bench
quotas. No filesystem is formatted or mounted by the quota helper.

## Fixed-policy helper

`bench-quota` accepts only `apply bench KEY [BENCH_BYTES]`, `apply shared store`, or
`apply shared mirrors`. The `release bench KEY PROJECT_ID` operation removes
only an empty allocated directory and confirms zero usage before retiring its
reservation. Paths, maximum/default byte caps and worker ownership come from a
root-owned policy. A [repository request](quotas.md) may select a smaller new
bench cap within that ceiling. The policy and its ancestors cannot be
replaced through writable non-sticky directories or symlinks. Data parents stay
root-owned; the worker owns only assigned bench/cache/mirror directories.
The ledger is root-owned and private. The helper uses fixed absolute XFS tools
and a fixed environment; there is no arbitrary quota-command interface.

The root must be a dedicated XFS filesystem root, not a bound subdirectory. The helper verifies project accounting
and enforcement, sets the project/inheritance and byte/inode hard limits, then reads both back. Allocation IDs start at 10000 and are never silently reassigned.
Shared projects use fixed IDs. The ledger binds to the filesystem UUID, so loop
device renumbering is tolerated but a different filesystem at the same path is refused. Every allocation is journaled before filesystem
effects, so retry after a lost reply uses its original reservation. A filesystem
anchor prevents a second ledger from allocating the same filesystem separately.

Example `/etc/superbench/storage.json` (choose sizes and the actual service uid/gid
for the provisioned host; the example requires a suitably sized dedicated volume):

```json
{
  "Root": "/srv/superbench-data",
  "Ledger": "/var/lib/superbench-quota",
  "OwnerUID": 995,
  "OwnerGID": 995,
  "BenchBytes": 8589934592,
  "StoreBytes": 4294967296,
  "MirrorBytes": 4294967296,
  "ReserveBytes": 2147483648,
  "BenchInodes": 250000,
  "StoreInodes": 2000000,
  "MirrorInodes": 1000000,
  "ReserveInodes": 250000
}
```

Total bench hard caps plus both shared caps and the filesystem reserve cannot
exceed filesystem capacity, for bytes or inodes. Unfinished allocations still count. Reservations are
conservative logical quota bytes; physical reflink sharing does not increase the
admission budget. [Reviewed reap](archive.md) reclaims a logical reservation
only after approved data is removed and XFS reports zero blocks/inodes. Project
IDs and keys are retired permanently; no data is deleted automatically.

Install the helper root-owned and non-writable by the service account, with sudo
permission only for that fixed executable. Do not grant generic shell/XFS-tool
sudo access. Policy overrides are accepted only for direct-root administration,
not setuid or sudo callers. The [Linux installer](install.md) supplies the service account and the fixed
sudo rule in managed mode. It never installs a privileged daemon or starts a
service automatically.

## Configure a fresh managed host

Pass the same state/data paths on every CLI call:

```sh
bench configure-host --state /var/lib/superbench/state --data /srv/superbench-data \
  --quota-helper /usr/local/libexec/bench-quota \
  --min-data-free 1073741824 --min-state-free 536870912 \
  --max-log-bytes 4194304 --max-logs-bytes 268435456 --max-log-files 4096
bench host-status --state /var/lib/superbench/state --data /srv/superbench-data
```

State and data must be on different filesystems. Managed configuration requires
fresh state and refuses adoption of existing unmanaged benches. Repeating the
same configuration is idempotent; changing budgets requires an explicit migration
rather than silently shrinking an allocation. Keep the mount root and `benches`
parent root-owned; do not recursively chown the whole data mount to the worker.

Create/recover, new leases and supervised runs check free-space reserves before
starting work. Managed admission also rechecks quota enforcement via the helper.
Checkpoint, stop and release remain available for reconciliation under low space.
Default unmanaged free-space floors are 64 MiB for data and 1 MiB for state;
managed hosts should set reserves appropriate to their actual workload.
These are admission checks, not control over other host processes.

## Setup and shared caches

`bench setup --holder installer KEY` detects exactly one supported lockfile:

- `pnpm-lock.yaml`: frozen install, one host store, forced clone imports.
- `package-lock.json`: native `npm ci`, one host npm cache, audit/fund off.

`--offline` requests offline reuse. Multiple supported lockfiles or no supported
lockfile require an explicit `bench run` setup command. Repository lifecycle
scripts run with the same fenced execution and authority as other trusted
commands; the setup operation never rewrites a frozen lockfile to make it pass.
A failed install retains its stopped lease for inspection. Successful setup
checkpoints the caller's existing clean HEAD and releases, without auto-committing.

Supervised commands receive host paths for npm, Bun, Playwright browsers and Go
module/build caches under `data/store`. Bun and browser environment support
follows [Bun's cache setting](https://bun.sh/docs/pm/global-cache) and
[Playwright's browser configuration](https://playwright.dev/docs/browsers).
Only pnpm/npm frozen setup is automatically selected here; actual browser,
Bun and toolchain installation are not claimed by the cache-path configuration.
The repository's proposed TOML setup hooks remain subsequent work.

Mirrors are cloned into quota-accounted temporary directories and published by
rename only after clone completes. No worktree borrows from a temporary clone.
Interrupted clone directories remain reserved under the mirror cap until a
reviewed collection operation; retry can build a new complete mirror. Mirror
origin and bare-repository identity are checked before reuse, and fetch updates
remote-tracking refs rather than checked-out branches. Local clone hard links are
disabled, including across quota projects.

## State/log budgets and schema upgrades

Run output is drained through a pipe into a bounded capture. The command cannot
stall just because its log reached the cap; subsequent bytes are discarded and a
truncation marker/result flag is emitted. Stdout/stderr descriptors do not hold
command wait open after a parent exits; descendant fencing precedes finishing
the capture. Log write failure retains the stopped lease.

The default per-run cap is 1 MiB, aggregate retained/reserved cap 64 MiB, and file
count cap 4096. Admission reserves outstanding held executions' remaining log
capacity and the new run's full cap against both the aggregate budget and actual
free state space. A failed held lease retains that reservation until reconciliation.
Budget exhaustion refuses new work instead of deleting logs. Automated retention
and per-bench history remain part of archive/service work. The limits bound
supervised output, not arbitrary filesystem writes by the launching uid.

Schema 5 additionally pins inode caps; [explicit inode migration](inodes.md)
upgrades byte-only hosts. Schema 4 protects archive/reap state from older binaries. Schema 3
introduced quota and log-limit metadata. Schemas 1/2/3/4
can upgrade through `bench migrate` or a successful mutating operation. Upgrading
under an active old supervised execution is refused: stop it with the installed
binary first. Read-only status never changes schema. Stronger uid/mount isolation,
TOML hooks and automatic cache collection remain pending.
[Inode quotas](inodes.md) are now enforced for managed allocations.
Archive/reap is available through a reviewed local administrative plan. A separately verified service installer is documented in [install.md](install.md).

## Verification

Local checks include policy/ledger conflicts, unsafe paths/budgets, admission,
managed empty-directory replay, frozen setup selection, mirror origin/temporary
isolation, outstanding log reservations, bounded output and active-upgrade refusal.
The staged root integration uses two disposable 512 MiB XFS images, one initially
without quota enforcement, plus temporary cgroups. It exercises actual hard caps,
shared reflink extents, frozen setup/build/offline reuse, policy refusal, total
budget refusal, assignment SIGKILL/replay and clone SIGKILL/republication. A prior
cgroup integration rerun covers descendants, full-state cancellation and recovery.
Remount/device-renumbering and wrong-volume refusal are also exercised.
The final results are recorded with the implementation checkpoint; no production
volume, existing bench, service or host configuration was modified.
