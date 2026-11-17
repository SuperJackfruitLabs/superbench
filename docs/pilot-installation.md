# Fresh Linux installation and checkpoint pilot configuration

This is the installation and configuration handoff for a supervised staging
rehearsal. The coordinator-grant design is accepted, subject to its gates before
enablement. Acceptance does not provision a service principal or prove a working
pilot. SuperBench remains standalone; the suite calls it, and SuperBench does not
call suite products. All suite checkpoint switches remain off by default.

**Current boundary:** on a managed host the installer creates the separate
`superbench-payload` account, the private payload policy and the public service
identity the payload helper requires, and an activation file with every v2 switch
off. benchd turns on the Fresh payload layout, finite I/O, receipt lookup,
cancellation and registration observation only from that root-owned file (see
[Activation](#activation)). With the file absent or all switches off, the daemon
behaves exactly as before. Do not make Node-private worker contexts readable to the
payload account to work around an adapter refusal. Installation success and matching
hashes are not evidence of a successful card or writer fencing.

## Current gaps

The following must be closed or verified before the rehearsal can establish an
end-to-end card result:

- **No activated run on a booted host yet.** Creating a Fresh bench and running a
  real task through the payload helper, with all five switches on, is covered by
  tests and fixtures but has not been run on an installed, booted host.
- **Harness configuration in the payload home.** The payload helper sets `HOME`
  from each bench's Fresh layout. Nothing yet places a harness's provider
  configuration there, and the harness adapter refuses to provision credentials.
  This needs a design decision (a shared read-only profile directory, a bench
  setup step or a keyless provider endpoint) before a provider-backed card can run.
- **Fresh recovery stays manual.** An interrupted or cancelled Fresh bootstrap is
  recovered by the direct-root steps in
  [Retained Fresh bootstrap evidence](fresh-bootstrap-recovery.md); a
  `transferring` tree stays parked. Turn `fresh_payload` on only with those steps
  at hand. Changing modes cannot adopt an incomplete legacy checkout.
- **AgentPod release and pins.** The Node needs an operator-approved AgentPod
  release that contains the coordinator wiring, the harness adapter and a
  compatible worker. Re-pin the installed Node and worker binaries and the registry
  after that upgrade (section 2).

Keep checkpoint flags off until the enablement gates and actual lifecycle checks
pass. Configuration, matching hashes and a successful install are not evidence
that a card can complete.

## 1. Prepare and install the standalone service

Use a fresh, disposable staging Linux host with systemd, delegated cgroup v2,
Python 3, Git and sudo. Managed storage requires XFS tools and a separately mounted
XFS filesystem with enforcing project quotas. Keep SQLite state and the quota
ledger outside the bounded data filesystem. The installer neither formats nor
mounts filesystems. Use the same architecture for the build and target host.

From the reviewed SuperBench source revision, with Go 1.26.4:

```sh
mkdir -p /absolute/build/bin
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench ./cmd/bench
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/benchd ./cmd/benchd
GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench-quota ./cmd/bench-quota
CGO_ENABLED=0 GOMAXPROCS=8 go build -p 8 -o /absolute/build/bin/bench-payload ./cmd/bench-payload
python3 deploy/linux/install.py --bin-dir /absolute/build/bin \
  --root /absolute/private/staging --managed
```

These are the four binaries required by the managed installer. The payload
helper must be a static Linux executable for the target architecture; do not
omit it or substitute a dynamically linked build. Review the staged files,
owners, modes, systemd unit, wrapper and sudo rule before applying anything.
Staging writes only below the supplied physical absolute root.

### Mount the data volume persistently

The quota state, `/var/lib/superbench-quota`, must be on an ext4, XFS or btrfs
filesystem with a UUID. The helper recognises that filesystem by its UUID; on
tmpfs, overlay or a filesystem without one, `initialize-indexed` refuses before
it writes a ready record. Other records still pin device numbers, so the
supported setup is an ext4 or XFS partition for the state (see [Reboot and device
numbers](#reboot-and-device-numbers)).

Mount the dedicated XFS filesystem at `/srv/superbench-data` with project quotas
before installing, and make the mount survive a reboot. The installer creates
the directory only later, so create the mountpoint first, as root:

```sh
mkdir -p /srv/superbench-data
```

With a block device, add an `/etc/fstab` line using the filesystem's UUID
(`blkid -s UUID -o value DEVICE`):

```text
UUID=FILESYSTEM-UUID  /srv/superbench-data  xfs  defaults,prjquota  0  2
```

For a filesystem image on a staging host, use a root-owned image outside the
state and data directories, attached at a **fixed** loop device by its own unit
rather than by the fstab `loop` option, which takes whichever loop device is free
at boot (see [Reboot and device numbers](#reboot-and-device-numbers)). As root,
write `/etc/systemd/system/superbench-data-loop.service`:

```ini
[Unit]
Description=Attach the SuperBench data image at a fixed loop device
DefaultDependencies=no
RequiresMountsFor=/var/lib/superbench-data.img
Before=local-fs.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/losetup /dev/loop7 /var/lib/superbench-data.img
ExecStop=/usr/sbin/losetup -d /dev/loop7

[Install]
WantedBy=local-fs.target
```

It runs before other loop users such as snaps start at boot. Pick a loop device
whose node exists (`ls /dev/loop7`) and that nothing else attaches. Enable and
start it with `systemctl enable --now superbench-data-loop.service`, read the
filesystem's UUID with `blkid -s UUID -o value /dev/loop7`, and mount by that
UUID once the loop device is attached:

```text
UUID=FILESYSTEM-UUID  /srv/superbench-data  xfs  defaults,prjquota,x-systemd.requires=superbench-data-loop.service  0  0
```

Instead of fstab, a systemd mount unit named `srv-superbench\x2ddata.mount`
(`systemd-escape -p --suffix=mount /srv/superbench-data`) with `What=`, `Where=`,
`Type=xfs` and `Options=prjquota`, enabled with `WantedBy=local-fs.target`, does
the same. After `systemctl daemon-reload`, mount it (`mount /srv/superbench-data`
or `systemctl start 'srv-superbench\x2ddata.mount'`) and check it with
`findmnt /srv/superbench-data` and `xfs_quota -x -c 'state -p' /srv/superbench-data`
(project accounting and enforcement both `ON`). Do not add `nofail` or `noauto`,
and do not mount it with `nouuid`: the installer and the quota helper refuse it.

After `daemon-reload`, `systemd-fstab-generator` may log `Quota was requested for
..., but not supported, ignoring: xfs`. That is harmless: systemd only declines to
start its own quota check and quota-on services, which XFS does not use. XFS
applies `prjquota` itself at mount time, which the `findmnt` and `xfs_quota`
checks above confirm.

The service unit has `RequiresMountsFor=/srv/superbench-data`, so systemd mounts
the volume before benchd and does not start benchd without it. The quota ledger is
bound to the filesystem UUID: mount the same filesystem every time. Keep the
device number the same across reboots too: some records still pin it (see
[Reboot and device numbers](#reboot-and-device-numbers)). The installer's `--storage`
refuses to write the storage policy unless `/srv/superbench-data` is a separate
mounted XFS filesystem root with enforced project quotas (`prjquota`, not
`pqnoenforce`), without `nouuid` and, on a first install, empty.

### Reboot and device numbers

A device number (`st_dev`) is not a stable name: a loop image attached with the
fstab `loop` option gets whichever loop device is free at boot, LVM minors follow
activation order, and btrfs gives each subvolume an anonymous number that can
change at boot. SuperBench identifies the quota ledger's state filesystem and the
managed data volume by their UUIDs for the quota ledger and authority, storage
proofs in transfer approvals, storage retirements, the root checks of
missing-source freezes and Fresh allocation recovery.

These records still pin device numbers in this release. After a reboot that
renumbers the device they are on, each fails closed: the operation is refused,
nothing is adopted or changed, and the record is kept:

- quota helper operation identities (the daemon state directory's device), so
  cancelling an operation interrupted before the reboot is refused;
- the Fresh payload bootstrap intent and the registered payload layouts, so
  resuming a bootstrap or running a registered payload is refused;
- archive freeze and reap trees, and worktree registrations in a reap plan;
- sealed run history and log captures;
- worktree transfer and missing-source quarantine evidence;
- mirror clone stages;
- any storage record on an unmanaged host, which has no UUID.

The supported host setup therefore keeps device numbers stable:

- put `/var/lib/superbench` and `/var/lib/superbench-quota` on an ext4 or XFS
  partition, not on btrfs or an LVM volume; initialization refuses a state
  filesystem without a UUID (tmpfs, overlay);
- put the data volume on a partition, or attach its image at a fixed loop device
  with the unit in [Mount the data volume
  persistently](#mount-the-data-volume-persistently) or `losetup` with an
  explicit `/dev/loopN`, never with the fstab `loop` option.

If a device was renumbered anyway, restore the original numbering (attach the
image at the same loop device again) before retrying; do not delete the records.

### Install on the host

Write the reviewed byte **and** inode budgets for the dedicated data volume to a
root-owned file, for example `/root/superbench-budgets.json` (the eight fields of
[the storage policy example](storage.md), and nothing else):

```json
{"BenchBytes": 8589934592, "StoreBytes": 4294967296, "MirrorBytes": 4294967296,
 "ReserveBytes": 2147483648, "BenchInodes": 250000, "StoreInodes": 2000000,
 "MirrorInodes": 1000000, "ReserveInodes": 250000}
```

Every bench reserves its full `BenchBytes`, on top of the two shared caps and the
reserve, so size the volume for the number of benches you expect: an allocation
that would exceed the filesystem is refused. On a fresh host, with the data volume
mounted, direct Linux root (a root login, not `sudo`) then runs:

```sh
python3 deploy/linux/install.py --bin-dir /absolute/build/bin --root / \
  --managed --prepare --identity --activation \
  --storage /root/superbench-budgets.json
```

This prepares the stable `superbench` account, the separate `superbench-payload`
system account (nologin, its own group, no members, and no other account's
primary group) and the directories, and reloads systemd; it does not start or
enable the service. Existing conflicting files, unsafe paths, an existing account
of either name that does not match that description, and an active installation
refuse before anything is written. This is not an upgrade or
legacy-adoption procedure. See [installer details](install.md) for preflight and
recovery.

`--identity` then writes two root-owned documents from the accounts' actual
numeric IDs:

| File | Mode | Content |
| --- | --- | --- |
| `/etc/superbench/payload-policy.json` | 0600 | `{"DaemonUID":…,"DaemonGID":…,"PayloadUID":…,"PayloadGID":…,"DataRoot":"/srv/superbench-data","StateRoot":"/var/lib/superbench/state","CgroupRoot":"/sys/fs/cgroup/system.slice/superbench.service"}` |
| `/etc/superbench/service-identity.json` | 0644 | `{"Version":"superbench.service-identity.v1","LayoutVersion":"superbench.payload-layout.v1","Policy":{…the same policy…},"PolicySHA256":"…"}` |

Both are the canonical compact JSON the helper and the daemon verify; the
registry's `PolicySHA256` is the SHA-256 of the exact policy bytes. `CgroupRoot`
must be the cgroup the unit is delegated, which `superbench-start` passes to
benchd: `/sys/fs/cgroup` followed by `systemctl show -p ControlGroup --value
superbench.service`. Pass `--cgroup-root` if the unit runs elsewhere. The accounts
must exist (they do after `--prepare`); identity documents found without them,
or documents that differ from what the accounts produce, refuse rather than being
replaced. To change one, remove it deliberately and review the new file.

`--activation` writes `/etc/superbench/activation.json` with every switch off.
Rerunning the installer accepts identical files and refuses changed ones, so do
not pass `--activation` again after editing the file.

### Indexed storage

`--storage` sets up the storage the Fresh payload layout needs. The payload
helper's `bench-quota bootstrap-payload` only accepts a storage policy that selects
the indexed operation journal, `"OperationJournal": "indexed-v2"`, with a ledger
that `bench-quota initialize-indexed` created on an empty filesystem. With any
other policy every Fresh `create` stops at `materializing` with `operation_failed`.
`--storage`:

1. checks that `/srv/superbench-data` is the root of a mounted XFS filesystem
   with enforced project quotas, and empty on a first install, before it writes
   anything ([above](#mount-the-data-volume-persistently));
2. writes root-owned `/etc/superbench/storage.json` (mode 0644) from the budgets
   file, the `superbench` account's numeric IDs, the data root, the ledger
   `/var/lib/superbench-quota/ledger` and `"OperationJournal": "indexed-v2"`;
3. masks the service (a `/dev/null` link in place of its unit file) and runs
   `bench-quota initialize-indexed` once, as direct root. That command only
   initializes: it refuses unless the service is masked and stopped, nothing
   runs as either SuperBench account, the data filesystem is empty with no quota
   projects, and the ledger does not exist yet;
4. checks that the helper can open the journal (`bench-quota operation-info`,
   read-only), which it can only once `initialize-indexed` has written its ready
   record, then installs the unit file in place of the mask and reloads systemd.

The ready record pins the helper's storage authority: the storage policy, the
ledger header and its directory, the quota owner anchor, the data root's inode,
owner and mode, and the mount's filesystem UUID, type, root (`/`), target and
`prjquota`. It does not pin the mount ID, propagation peer group or the data
volume's device number, which differ between mount namespaces or can change at
boot. So the record written by the installer in the host's mount namespace also
verifies in the private namespace systemd gives the unit (`PrivateTmp`,
`ProtectHome`, `ProtectSystem`), and it survives a reboot that mounts the same
filesystem again. A different filesystem, a bind of a subdirectory, a stacked
mount, a mount without enforced project quotas or with `nouuid` refuses. Keep the
ledger on the host's persistent state filesystem, as the installer does: the record
also pins that directory's device and inode.

Run it as part of the first install, before `configure-host` and before the
service ever starts. Rerunning the same command is safe: a host whose ledger is
already initialized (its ready record is present) skips step 3, and a run that
stopped after step 3 finishes step 4. If `initialize-indexed` fails before it
writes anything, the service stays masked without its unit file; fix the reported
cause and rerun the same command. If it stopped partway, see
[Recover a partial initialization](#recover-a-partial-initialization). The
installer refuses, before it changes anything, when:

- `/etc/superbench/storage.json` exists without the indexed journal, or
  `/var/lib/superbench-quota` holds a legacy ledger (`assignments.json`, `lock`,
  `operations`). In-place conversion is not supported;
- `/var/lib/superbench-quota` holds an indexed ledger without its ready record
  (`ledger.format-v2` lacks its second line). An interrupted initialization is
  never adopted or retried, and the service stays masked;
- `/etc/superbench/storage.json` exists but differs from what these budgets and
  accounts produce;
- the unit file is already installed from an earlier run without `--storage`;
- `SUDO_UID` is set: `initialize-indexed` refuses `sudo` callers.

#### Recover a partial initialization

`initialize-indexed` writes, in order: the format intent
(`/var/lib/superbench-quota/ledger.format-v2`, one line), the ledger directory,
its `operations.lock` and `assignments.json`, the quota owner anchor
`/srv/superbench-data/.superbench-quota-owner.json`, the journal database, and
last the ready line appended to `ledger.format-v2`. Only that line makes the
journal usable. If the command stopped before it, nothing has been allocated and
no workspace exists, but the records cannot be adopted or completed. With the
service still masked (do not unmask or start it):

1. back up `/var/lib/superbench-quota` and the error output;
2. remove `/var/lib/superbench-quota/ledger` and
   `/var/lib/superbench-quota/ledger.format-v2`, leaving the directory itself;
3. recreate the data filesystem with project quotas (`mkfs.xfs` on its device or
   image), update the fstab or mount unit if it names the filesystem UUID, and
   mount it again; the first install needs an empty filesystem with no quota
   projects;
4. rerun the same installer command, `--storage` included. It keeps the
   identical `storage.json` and the mask, initializes again and then unmasks.

benchd also checks the journal at start: with `fresh_payload` on, it refuses to
start unless the helper's `operation-info` opens the indexed journal.

#### Reinitialize the indexed ledger

A ready record written by an earlier SuperBench revision pinned the data mount's
mount ID, which is different inside the service unit's private namespace and
after a reboot, or the state filesystem's device number, which btrfs and LVM can
change at boot. The second fails with `the ledger was initialized by an earlier
SuperBench revision that pinned the state filesystem's device number`, and the
installer refuses it the same way. With it, benchd refuses to start with `fresh_payload` on, and
`bench-quota operation-info` fails with `quota operation journal authority
changed: the ready record pins a storage authority that ... no longer match`. The
record is never adopted or rewritten. Reinitializing recreates the data
filesystem and so deletes every workspace on the volume: do it only on a host
whose work is disposable or already checkpointed and released, and otherwise keep
the earlier revision installed. To reinitialize the storage as a first install:

1. keep `fresh_payload` off, then stop and disable the service and confirm no
   `superbench` or `superbench-payload` process remains;
2. back up `/etc/superbench`, `/var/lib/superbench` and `/var/lib/superbench-quota`;
3. remove `/var/lib/superbench-quota/ledger` and
   `/var/lib/superbench-quota/ledger.format-v2`, leaving the directory itself;
4. remove the installed unit file `/etc/systemd/system/superbench.service` and
   run `systemctl daemon-reload`, so the installer masks the service and
   initializes again instead of refusing an installed unit;
5. unmount the data volume (`umount /srv/superbench-data`), then recreate the
   data filesystem as in step 3 of
   [Recover a partial initialization](#recover-a-partial-initialization) and
   mount it again: its quota owner anchor belongs to the removed ledger, and
   `mkfs.xfs` must never run on a mounted filesystem;
6. rerun the same installer command, `--storage` included, from the new revision.
   Leave out `--activation` if you have edited `/etc/superbench/activation.json`;
7. rerun `bench configure-host` as the service account, with the same options as
   in the first install (below): the new filesystem has none of the shared
   projects it applied.

The same refusal on a host initialized by the current revision means the storage
really changed: another filesystem is mounted, or the policy, ledger or anchor
differs. Do not reinitialize a host whose data you still need; restore the
original volume and files instead.

**An existing host with a legacy ledger** cannot turn on `fresh_payload`; benchd
refuses to start with it (see [Activation](#activation)). Either keep the switch
off, or rebuild the host's storage: checkpoint and release the work you want to
keep, stop and disable the service, keep a backup of `/etc/superbench`,
`/var/lib/superbench` and `/var/lib/superbench-quota`, then remove the installed
files, the state, the ledger and the storage policy, recreate the data
filesystem (old quota projects on it also refuse initialization) and install
again with `--storage`. Do not delete a ledger to get past a refusal on a host
whose data you still need.

On an indexed host the helper still applies the two fixed shared projects for
`configure-host` and admission, but it refuses an unscoped bench allocation or
release: every bench effect needs its operation's identity. Preserve reserve
space; do not recursively chown the volume. Configure fresh state as the
service account:

```sh
sudo -u superbench /usr/local/bin/bench configure-host \
  --state /var/lib/superbench/state --data /srv/superbench-data \
  --quota-helper /usr/local/libexec/bench-quota \
  --min-data-free 1073741824 --min-state-free 536870912
```

Edit root-owned `/etc/superbench/benchd.env`:

```ini
BENCHD_OPERATOR_UIDS=0
BENCHD_READER_UIDS=
BENCHD_MAX_ACTIVE=8
BENCHD_RETENTION_INTERVAL=1m
BENCHD_MIRROR_TIMEOUT=2m
```

Add the actual Node process UID to the comma-separated operator allowlist when
it is authorized to mutate benches. Separately give that process filesystem
access through the service group. The socket parent is 0750 and socket 0660;
group membership alone grants no API authority. The daemon UID is not implicitly
an operator. Keep daemon state private. Supply repository credentials through the
approved service-account credential mechanism, never a worker prompt, repository,
tracked environment file or Node handoff.

The rehearsal operator starts the service explicitly:

```sh
systemctl enable --now superbench.service
/usr/local/bin/bench host-status --socket /run/superbench/benchd.sock
systemctl status superbench.service
journalctl -u superbench.service --no-pager
```

Clients receive only the static error their protocol defines, such as
`operation_failed`. benchd writes the cause of every failed operation, and of
every outcome it could not record, to its journal as one line:

```text
operation failed: api=v2 operation="create" request_id="fresh1-create" key="fresh-1" phase="materializing" code=operation_failed cause="fixed-helper operation failed: exit status 1: \"bench-quota: …\""
```

`phase` is the workspace phase the response reports (`-` when there is none). The
cause is the error chain, at most 1 KiB, with the quota helper's own refusal line
and with repository URL credentials and queries removed. A line never holds the
request body, lease tokens, command arguments or run output. Find one with
`journalctl -u superbench.service --grep 'operation failed'`.

The installed wrapper discovers the delegated cgroup from the running service.
Keep `Delegate=yes` and the reviewed managed helper boundary. Do not change the
unit's privilege restrictions independently of the helper installation. Require
an actual Linux managed host-status response; service activity alone is not
managed readiness or proof that a previous execution has stopped.

### Activation

benchd reads the default-off v2 switches only from
`/etc/superbench/activation.json`. No request, environment variable, `benchd.env`
entry or command-line flag can set them. The file is a JSON object with the exact
version and any of five switches, each a literal `true` or `false`:

```json
{
  "version": "superbench.activation.v1",
  "fresh_payload": false,
  "finite_io": false,
  "receipt_lookup": false,
  "cancellation": false,
  "registration_observation": false
}
```

| Switch | Turns on |
| --- | --- |
| `fresh_payload` | Fresh payload layout for new and recovered workspaces, through the payload helper |
| `finite_io` | The finite-I/O profile on v2 `run` and receipt lookup ([finite I/O](finite-io.md)) |
| `receipt_lookup` | `POST /v2/receipts/lookup` ([receipt lookup](receipt-lookup.md)) |
| `cancellation` | v2 `cancel` and the operation reconciler ([cancellation](operation-cancellation.md)) |
| `registration_observation` | `POST /v2/registrations/observe` |

An omitted switch is off. An unknown, duplicated or non-boolean key, a missing or
different version, or trailing data refuses startup. `receipt_lookup` and
`cancellation` also move the socket to the private connection classes. Any of
`receipt_lookup`, `cancellation` and `registration_observation` makes each
connection carry one response (`Connection: close`); `registration_observation`
does this even on its own.

Some switches depend on others. benchd checks these at start, before it opens
the socket, and refuses a file that breaks one:

| Switch | Requires | Why |
| --- | --- | --- |
| `fresh_payload` | `cancellation` | Fresh materialization runs inside the operation scope that only the cancellation path claims. Without it every v2 `create` or `recover` would record a durable `materializing` workspace and then fail. |
| `fresh_payload` | the installed payload policy and service identity | benchd resolves `/etc/superbench/service-identity.json` (with `--identity`, above) for its own uid at start, for the same reason. |
| `fresh_payload` | a ready indexed quota operation journal | benchd reads `/etc/superbench/storage.json` at start and refuses unless it sets `"OperationJournal": "indexed-v2"` and the helper's read-only `operation-info` opens that journal, which needs the ready record of a finished `initialize-indexed` ([Indexed storage](#indexed-storage)). The payload helper refuses anything else, so every Fresh create would stop at `materializing`. |
| `finite_io` | `fresh_payload` | A finite run executes only on a Fresh payload tree. |
| `registration_observation` | `fresh_payload` | Observation reads only a Fresh payload tree. |
| `receipt_lookup`, `cancellation` | nothing | |

So the registered launch, which needs all five, sets every switch to `true`, and
`receipt_lookup` or `cancellation` may run alone.

While `fresh_payload` is on, benchd refuses a v1 `create` or `recover` before any
bench effect (no creation intent, allocation or bench record), with `409 conflict`
and the message `scalar benches cannot be created while fresh_payload is active;
use the workspace API`. The request's receipt is still recorded, so retrying the
same `request_id` replays the same 409. The indexed journal that `fresh_payload`
needs refuses the unscoped bench allocation a scalar create makes, so the create
would otherwise fail after recording its intent. Other v1
operations on existing scalar benches are unchanged. Separately, v1 has no
capacity code: a v1 create that would take the volume past its reservations (every
bench reserves its full `BenchBytes`) returns `operation_failed`, and only the
journal line says `capacity_exhausted`.

When the file exists, benchd refuses to start unless it is a single-link regular
file of at most 4 KiB, owned by root and not group- or world-writable, and every
directory from `/` to `/etc/superbench` is owned by root without group or world
write. Symlinks are never followed. A missing file, or a missing
`/etc/superbench`, keeps every switch off whatever those directories look like,
and the daemon behaves exactly as without this feature. benchd runs as the
`superbench` account, so it must be able to search `/etc/superbench` (the
installer creates it with mode 0755). If it cannot, benchd cannot tell whether
the file exists and refuses to start. At start
benchd logs one line with the effective switches, for example
`activation: fresh_payload=false finite_io=false receipt_lookup=false cancellation=false registration_observation=false`.

**Existing non-Fresh workspaces.** `fresh_payload` changes only new v2 `create`
and `recover`. A v2 workspace that completed in the non-Fresh layout before the
switch went on (for example one created with `cancellation` on and
`fresh_payload` off) keeps that layout and can still be leased while Fresh is on:
lease, setup, run, checkpoint, release and stop work as before. This is
intended, so that work in flight is not stranded, but that workspace gets none of
the Fresh guarantees: its commands run as the `superbench` account rather than
the payload account, without the payload helper, and finite I/O and registration
observation do not apply to it. It is never converted in place. To move the work
to the Fresh layout, checkpoint and release it, then create a new workspace from
the checkpointed branches. An incomplete non-Fresh workspace is not adopted
either (see [Current gaps](#current-gaps)).

**Cancellation and receipt lookup are API-only.** The `bench` CLI has no cancel
or receipt-lookup command: both exist for the coordinator, which sends them over
the socket. An operator who needs one uses an HTTP client on
`/run/superbench/benchd.sock`: `POST /v2/operations` with `"operation":"cancel"`
for [cancellation](operation-cancellation.md), or `POST /v2/receipts/lookup`
for [receipt lookup](receipt-lookup.md). Each names the original request ID and
the typed digest of the original request, so keep the exact original request
(the CLI's `--request-id` sets the ID). Cancelling a request ID that was never
dispatched still records the cancellation, so that ID cannot be used afterwards.

An activated daemon never migrates its state. After installing or upgrading
benchd, start it once with every switch off so it brings the state current, stop
it, then edit the file and start it again. Otherwise benchd refuses with
`activated paths need state migrated by a start without them` before it opens the
socket. Restart the service to apply any change. Turning a switch off later stops
new work on that path; it does not erase obligations already recorded.

## 2. Configure the AgentPod Node

Use an enrolled Linux Node with the intended station and current control pair.
Install its matching `agentpod-bench-worker-linux-amd64` or
`agentpod-bench-worker-linux-arm64` artifact for the host architecture. Record
SHA-256 digests of the **installed bytes** of both worker and running Node.
The coordinator implementation digest refers to the Node executable, not benchd.

Add the following key to the Node's existing private JSON configuration, retaining
its enrollment and other settings:

```json
{"proposedCheckpointRegistry":"/absolute/private/coordinator-registry.json"}
```

On Linux, the default Node configuration is
`$XDG_CONFIG_HOME/agentpod-node/config.json`, or
`$HOME/.config/agentpod-node/config.json` when XDG_CONFIG_HOME is unset, for the
actual Node service account. This is a JSON setting, not an environment switch.
Empty or omitted disables this adapter. Restart the Node to apply configuration.

The registry is a strict JSON object with these fields; there are no extra keys:

| Field | Value |
| --- | --- |
| `version` | `agentpod.private-coordinator-registry.v1` |
| `node_id`, `host_id` | Actual enrolled Node and configured host identities |
| `socket` | `/run/superbench/benchd.sock` for the installation above |
| `data_root` | `/srv/superbench-data` for the installation above |
| `state_root` | Private Node coordinator journal directory, **not benchd's SQLite directory** |
| `intent_root` | Separate private Node adapter intent directory |
| `context_parent` | Separate private Node worker-context directory |
| `worker_path`, `worker_sha256` | Physical installed worker executable and its exact SHA-256 |
| `coordinator_implementation_sha256` | SHA-256 of the actual Node executable |
| `profiles` | One to sixteen explicit local profiles described below |

Use physical absolute paths, private directory mode 0700 and file mode 0600;
symlinks are refused. The three private Node directories must be distinct and
outside `data_root`. Do not reuse SuperBench state or expose these directories to
payloads. The registry is at most 64 KiB. Hash its exact final bytes only after
writing it; that digest is the Hub target's `registry_sha256`.

Each local profile contains `id`, `tenant_id`, `board_id`, `agent_id`,
`station_id`, `checkpoint_repository`, `repositories`, `arguments` and
`environment`. Use actual assigned IDs. Repositories are sorted by logical
`name`; each contains `name`, private clone identity `repository`, `base` and
`object_format` (`sha1` or `sha256`). The checkpoint repository must name a member.
`arguments` is the trusted harness command with an absolute executable as its
first entry; it is not the separate worker executable. Environment keys are
limited to `PATH`, `LANG`, `LC_ALL`, `TZ`, `TERM`. No credentials belong here.

A registry pin does not confer authority. The Node still verifies current
occupancy, managed host state, installed executables and the exact request.
Changing a binary or registry requires corresponding reviewed pins and restart;
do not substitute guessed digests to obtain a readiness response.

## 3. Configure the AgentPod Hub

Only after the accepted decision's enablement gates have been met, configure:

```ini
PROPOSED_CHECKPOINT_COORDINATOR=true
PROPOSED_CHECKPOINT_COORDINATOR_PROFILE_FILE=/absolute/private/checkpoint-profiles.json
```

Only literal `true` enables selection. The profile file and parent must be
physical, owned by root or the Hub service user, and inaccessible to others
(0700 parent, 0600 file). The file is at most 64 KiB and has this envelope:

```json
{"version":"agentpod.checkpoint-profiles.v1","profiles":[]}
```

The empty array is a shape illustration and is **not valid configuration**: supply
one to sixteen strict profiles. Each contains all of:

- `version`: `agentpod.checkpoint-profile.v1`;
- `organizationId`, `sourceTenantId`, `sourceOrigin`, `sourceAudience`;
- `reporterPrincipalId`, `ownerPrincipalId`, `occupantPrincipalId`;
- `agentKey`, `profileKey`, positive `occupancyEpoch`;
- `target`, `repositories`, `checkpointRepository`.

The source origin and audience are exact HTTPS origins without trailing slashes,
paths, queries or fragments. The source tenant is SuperPipeline's tenant;
`target.tenant_id` is AgentPod's local tenant. Map both to the same issuer
organization rather than assuming their identifier strings are interchangeable.
Owner, occupant, station and epoch must describe the actual current control pair.

`target` contains `tenant_id`, `board_id`, `agent_id`, `station_id`, `node_id`,
`host_id`, `registry_sha256`, `coordinator_implementation_sha256`,
`execution_mode` = `linux-cgroup-v2`, `bench_protocol` = `superbench.v2`,
`worker_version` = `agentpod.superbench-worker.v2`, and `worker_sha256`.
Match the Node registry and installed artifacts exactly.

Each Hub repository mapping has `name`, `repositorySHA256`, `provider` (`forge`
or `github`), `repository` (`owner/repo`) and `objectFormat`. Sort by name.
`repositorySHA256` hashes the exact private clone identity in the Node registry;
the public provider/repository allowlist must describe that same repository.
`checkpointRepository` must match the Node and stage policy. Never publish the
private clone identity as checkpoint evidence.

Use the Hub's existing organization-plane issuer configuration and Hub-only
service credential. The selected reporter must be the actual issuer service
principal, authorized for the exact organization and SuperPipeline audience,
with `checkpoints:submit` and `checkpoints:read` and the required product
entitlement. Provisioning remains an operator gate; a profile does not create a
grant. Never copy the reporter credential to the Node, worker or payload.

Restart the Hub after changing or repairing the profile file. It takes an
immutable per-path snapshot, including a failed load. A malformed profile disables
dedicated selection and logs a closed refusal code once; unrelated ordinary
agents continue. Checkpoint-required work must not fall back to generic ACP.
See AgentPod's [checkpoint reporting guide](https://github.com/SuperJackfruitLabs/agentpod/blob/main/docs/contributor/bench-checkpoint-reporting.md).

## 4. Opt in one SuperPipeline board stage

Keep the existing issuer/tenant mapping valid. In the isolated rehearsal
deployment, set all three switches to literal `true`:

```ini
PROPOSED_CHECKPOINT_POLICY=true
PROPOSED_CHECKPOINT_SOURCE=true
PROPOSED_CHECKPOINT_SETTLEMENT=true
```

The switches are independent; none provisions credentials or enables another.
An issuer-authenticated workspace owner/admin applies a policy to the intended
stage through `PATCH /v1/boards/:id/stages/:stageKey`. Agents cannot select their
own reporter. Do not change policy while the stage has active work.

Example request body, with placeholders replaced by actual enrolled identities:

```json
{
  "checkpointPolicy": {
    "version": "superpipeline.checkpoint-policy.v1",
    "kind": "superbench",
    "allowUnchanged": false,
    "repositories": [
      {"name":"app","provider":"forge","repository":"example/app"}
    ],
    "checkpointRepository": "app",
    "reporterPrincipalId": "ACTUAL_ISSUER_SERVICE_PRINCIPAL",
    "nodeId": "ACTUAL_NODE_ID",
    "executors": [{"agentId":"ACTUAL_AGENT_ID","stationId":"ACTUAL_STATION_ID"}]
  }
}
```

Use the complete sorted repository vector matching Hub and Node. A code-producing
stage uses `allowUnchanged:false`; permit unchanged output only through an
explicit verification-stage decision. Never create empty commits to simulate a
change. Leave ordinary stages policy-free; omit `checkpointPolicy` from ordinary
contexts rather than emitting `checkpointPolicy:null`.

The dedicated AgentPod adapter negotiates `superpipeline.checkpoint-claim.v1`
and checkpoint-only selection. It retains source admission before execution and
settles through the service-only checkpoint routes. Generic run completion does
not satisfy a policy-required run. The service endpoints are, under
`/v1/boards/:boardId/runs/:runId`, POST/GET `checkpoint-source`, POST
`checkpoint-settlements` and GET `checkpoint`. Do not manually forge their bodies
to get a card through the gate.

## 5. Rehearsal evidence and stop conditions

The rehearsal owner records the selected source revisions, installed binary and
registry digests, managed host status, current enrollment/control pair, effective
flags and exact stage policy before choosing a real card. The repeatable rehearsal
must observe create, lease, actual harness execution, stopped/fenced writers,
checkpoint with remote confirmation, release and authenticated settlement against
the same claim. Source admission, a checksum or a parked ledger row alone is not
that proof. Report each integration gap instead of weakening a refusal.

On uncertain launch, missing receipt, changed owner, failed checkpoint or lost
reply, retain the exact journals and leases for reconciliation. Do not repeat the
child, delete state, expire a lease by elapsed time, or reset work to force success.
On shutdown, systemd kills the service cgroup; inspect durable recovery before
assuming release. Disabling flags prevents new dedicated work but does not erase
existing obligations. No production tag or deployment is part of this guide.
