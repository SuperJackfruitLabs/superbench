# Linux service installation

This installs the standalone local service. It does not format/mount a volume,
start production, create a release tag or configure suite consumers. Prepare a
reviewable staging image before applying the payload to a host. Python 3, git,
systemd with cgroup-v2 delegation, sudo and the selected package/toolchain tools
must already be available. Managed storage additionally needs XFS tools and a
dedicated mounted XFS filesystem with project quota enforcement.

## Review the payload

Build the Linux executables from the reviewed revision, using Go 1.26.4:

```sh
mkdir -p /absolute/build/bin
go build -o /absolute/build/bin/bench ./cmd/bench
go build -o /absolute/build/bin/benchd ./cmd/benchd
go build -o /absolute/build/bin/bench-quota ./cmd/bench-quota
CGO_ENABLED=0 go build -o /absolute/build/bin/bench-payload ./cmd/bench-payload
python3 deploy/linux/install.py --bin-dir /absolute/build/bin \
  --root /absolute/private/staging --managed
```

Omit `--managed` for an unmanaged standalone host. A staging installation writes
only beneath the supplied root, never creates a live account and never starts a
service. Its paths must be absolute and have no symlink/writable ancestors (a
root-owned sticky temporary directory is permitted). On macOS, `/tmp` itself is
a symlink; use its physical `/private/tmp` path when inspecting a Linux image.
The binaries must be 64-bit Linux ELF files. Build on the host or explicitly
cross-compile for its architecture; the installer does not download executables.
Managed packaging requires the dedicated `bench-payload` executable, built with
`CGO_ENABLED=0` for the same architecture. Its ELF program headers must contain
neither a dynamic interpreter nor a shared-library dependency. Unmanaged packaging
does not install that privileged executable or its sudo grant.

Inspect the service, environment, sysusers/tmpfiles rules, wrapper and, for a
managed host, sudo policy. Existing identical files are accepted. A different
file, unexpected mode/owner, symlink or unsafe parent refuses the entire preflight.
The installer does not overwrite operator configuration or implement upgrades.
New files are fsynced then published without replacing concurrent files. A failed
installation can be retried after reviewing any partial new-file publication.

## Prepare a fresh host

After reviewing the image and the host's data mount, direct Linux root can run:

```sh
python3 deploy/linux/install.py --bin-dir /absolute/build/bin --root / \
  --managed --prepare --identity --activation
```

This prepares the `superbench` system account, directories and systemd reload.
Managed installation also creates the separate `superbench-payload` system
account. `--identity` (live managed root only) writes the private payload policy
and public service identity from the two accounts' numeric IDs, and
`--activation` writes `/etc/superbench/activation.json` with every v2 switch off;
see [pilot installation](pilot-installation.md) for both formats.
It refuses an active existing product service. It **does not enable or start**
the service. Root owns installed binaries/configuration. The account owns private
state and runtime; managed data/ledger parents remain root owned. Unmanaged data
is owned by the service account. Do not use tmpfiles to convert an existing host
between modes: managed configuration requires fresh state and separately reviewed
storage policy. No recursive data-mount chown is performed.

For managed hosts, pass `--storage BUDGETS_JSON` with the reviewed byte and inode
budgets on the first install. It writes a root-owned `/etc/superbench/storage.json`
with the actual `superbench` IDs and the indexed operation journal, and initializes
the empty ledger once; see
[pilot installation](pilot-installation.md#indexed-storage) and
[storage provisioning](storage.md). Then configure fresh state as the worker:

```sh
sudo -u superbench /usr/local/bin/bench configure-host \
  --state /var/lib/superbench/state --data /srv/superbench-data \
  --quota-helper /usr/local/libexec/bench-quota \
  --min-data-free 1073741824 --min-state-free 536870912
```

Only managed installation supplies `/etc/sudoers.d/superbench`. It permits the
fixed quota helper executable; that helper validates its root-owned policy and
accepts logical allocations only. It also permits the dedicated payload helper
with **no command-line arguments**, using sudoers' explicit empty argument string.
That helper requires the matching private payload policy and public service
registry that `--identity` writes; the payload paths stay off until the
root-owned activation file turns them on. These packaging checks alone do not
prove payload isolation. No generic shell/XFS sudo is granted. A sudo
caller cannot select another policy. The service intentionally retains the sudo
path; do not add `NoNewPrivileges`, dynamic-user restrictions or a capability
filter that prevents that helper from doing its work without retesting managed
allocation. This remains a trusted coding-agent account, not tenant isolation.

Edit root-owned `/etc/superbench/benchd.env` with explicit operator/read-only uids.
The installer writes it only when missing and afterwards validates it without
replacing it, so the edit survives reruns and `--upgrade`.
Root is the only initial operator. Add approved socket clients to the service
group for filesystem access; group membership alone confers no API authority.
The socket parent is 0750 and socket 0660, while state stays private 0700.
Configure git credentials for the service account using the host's approved
credential mechanism; do not put credentials in the tracked environment template.
`ProtectHome` prevents reading arbitrary user home directories.

The operator can then start the reviewed local service:

```sh
systemctl enable --now superbench.service
/usr/local/bin/bench host-status --socket /run/superbench/benchd.sock
```

The root-owned wrapper finds the service's current cgroup-v2 path rather than
assuming a systemd slice. `Delegate=yes` makes child scopes available to the
unprivileged daemon. `ProtectControlGroups=no` preserves that delegation;
[systemd's execution reference](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html)
documents the related mount restrictions. The service protects system files,
uses private temporary storage, and permits writes to its data/state/ledger.
The installer supplies a stable account so lease/recovery ownership persists
across restarts.

## Restart and recovery

Systemd stops the entire service cgroup on shutdown, with 45 seconds for daemon
reconciliation. A clean daemon termination stops/fences admitted executions but
retains interrupted leases for inspection. If it must be killed, systemd also
kills descendants; core state and request receipts still require explicit
reconciliation on restart. Never infer release from systemd status alone. See
[service recovery](service.md). `Restart=on-failure` does not retry operations.

To upgrade, stop the service, reconcile outstanding executions with the installed
binary, back up state and review schema compatibility. A plain rerun refuses
differing installed files. `--upgrade --previous SHA256SUMS --sums SHA256SUMS --backup DIR`
replaces only the four executables, with the bytes the new build's `--sums` lists. Each must be the previous reviewed build, as a plain
root-owned file. It is backed up to the new directory and then replaced
atomically, while the unit is stopped. Every other installed file must stay
identical ([pilot installation](pilot-installation.md#upgrade-the-executables)).
Do not delete private state, receipts or quota ledgers to bypass a refusal.
Rolling back to a build without workspace reclaim fails once any bench is
`reclaiming` or `reclaimed`: the older daemon refuses to start on those records.

## Verification

The regular checks test conflict preflight, symlink/writable-parent refusal,
idempotent publication, private sudo policy mode and managed/unmanaged ownership
layout. `scripts/test-service-install.py` stages the real payload and creates a
unique disposable systemd unit/account. It exercises actual unprivileged Linux
execution, delegated fencing/checkpoint/release and the helper's sudo boundary,
then removes only its own unit, account, policy and data. The recorded run is
[service-install-output.txt](service-install-output.txt). Actual XFS quota effects
are covered by the separate storage drill; this service drill checks delegation
and helper privilege/refusal, not a live managed-volume pilot. It requires disposable
Linux root; never run it against a production installation.
