---
title: benchd
description: The local daemon's flags as cmd/benchd defines them, and what it does on start, restart and shutdown.
status: wet paint
---

`benchd` serves the [local API](/api/overview/) on one Unix socket, from one private state store.
It also runs the bounded background loops for automatic archive and, once an operator approves a
policy, automatic log retention. A lifetime lock stops a second daemon from serving the same
state. This page is checked against `cmd/benchd` when the docs build.

```sh
benchd --state /absolute/private/state --data /absolute/data \
  --socket /absolute/runtime/benchd.sock --cgroup-root /sys/fs/cgroup/superbench \
  --operator-uids 1000,0 --reader-uids 1001
```

Replace the example uids with real local accounts. All paths must be absolute. The state directory
must be private (0700), owned by the daemon and outside bench data.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state` | `<config dir>/superbench/state` | Private authoritative state directory. |
| `--data` | `<cache dir>/superbench/data` | Bench data directory. |
| `--socket` | `<state>/benchd.sock` | Absolute Unix socket path. |
| `--cgroup-root` | `/sys/fs/cgroup/superbench` | Delegated cgroup-v2 root. |
| `--operator-uids` | the daemon's uid and `0` | Comma-separated local operator uids. |
| `--reader-uids` | (none) | Comma-separated local read-only uids. |
| `--max-active` | `8` | Maximum active operations, 1 to 64. |
| `--mirror-timeout` | `2m0s` | Deadline for mirror clone, validation and fetch. |
| `--retention-interval` | `1m0s` | Automatic retention check interval; inactive until a policy is approved. |
| `--archive-interval` | `1m0s` | Archive inspection interval; only a committed `branch-merged` repository policy opts in. |

`benchd version` prints the build's version.

## Running alongside the CLI

Keep host configuration consistent with the state, data and cgroup paths the daemon uses. Do not
run competing direct writers while `benchd` serves that state: direct core commands do not enforce
the socket's uid policy. The core's OS lock still serializes git and state mutations. `migrate`,
`configure-host`, `archive`, `plan-reap` and `reap` stay local administrative commands.

## Restart and shutdown

- A disconnected client does not cancel work the daemon admitted.
- On restart, unfinished requests become `operation_in_doubt` and are never run again
  automatically.
- SIGTERM stops admission and cancels supervised runs, which fence before keeping their lease.
  HTTP shutdown has a ten-second grace period; the state lock is kept until admitted handlers
  finish, including slow git work.
- SIGKILL can leave descendants alive. Restart, then `bench stop --admin` fences the recorded scope.

## On Linux under systemd

The repository's staged installer (used in drills on disposable hosts, not a supported installation)
runs `benchd` as a stable `superbench` account with `Delegate=yes`, reads `/etc/superbench/benchd.env`
for `BENCHD_OPERATOR_UIDS`, `BENCHD_READER_UIDS`, `BENCHD_MAX_ACTIVE`, `BENCHD_RETENTION_INTERVAL`
and `BENCHD_MIRROR_TIMEOUT`, and serves `/run/superbench/benchd.sock`. It never starts or enables
the service by itself. Systemd stops the whole service cgroup on shutdown, with 45 seconds for
reconciliation; never infer release from systemd status alone.
