---
title: Files and paths
description: Where SuperBench keeps state, bench data, mirrors, caches, logs and its git refs, and the paths the staged Linux installer uses.
status: wet paint
---

## State and data

SuperBench keeps two directory trees apart: **state** (authoritative, private) and **data**
(mirrors and checkouts). They cannot overlap, including through symlinks. Use local filesystems and
the same `--state` and `--data` on every invocation. On a managed host they must be on different
filesystems, and the data tree is the dedicated XFS mount.

| Path | What it holds |
| --- | --- |
| `<state>/bench.db` | Versioned SQLite state, with FULL synchronous commits. Status reads it without migrating. |
| `<state>/service.db` | The daemon's durable request receipts. |
| `<state>/benchd.sock` | The daemon's socket, when `--socket` is not given. |
| `<state>/run-<execution id>.log` | A supervised run's private, bounded output log. |
| `<state>/reap-<key>.json`, `<state>/archive-freeze-<key>.json` | Reap approval and archive permission journals. |
| `<data>/benches/<key>` | A bench's checkout. |
| `<data>/mirrors/` | Shared bare git mirrors, one per repository. |
| `<data>/store/` | Shared caches: `pnpm`, `npm`, `bun`, `playwright`, `go-build`, `go-mod`. |
| `<checkout>/.bench/config.toml` | The repository's [settings](/reference/repository-settings/). |
| `<checkout>/.bench/cache/` | Checkout-local caches, when a repository chooses them. |

The defaults are `<config dir>/superbench/state` and `<cache dir>/superbench/data`, from Go's user
config and cache directories (see [bench CLI](/reference/bench/)). Earlier prototype JSON state is
imported once on the next successful write and kept as a historical snapshot; SQLite is
authoritative from then on.

## Git refs

| Ref | What it is |
| --- | --- |
| `refs/heads/bench/<key>` | A bench's task branch, unless `--branch` names another. Checkpoints push here without force. |
| `refs/tags/superbench/pins/<bench_id>/<generation_id>/<member>` | A workspace checkpoint's pin for one member. Never overwritten or deleted. |
| `refs/heads/superbench/checkpoints/<bench_id>` | A workspace's checkpoint seal: commits holding only `checkpoint.json`. |

Mirror refreshes update only `refs/remotes/origin/*`.

## The staged Linux installer's paths

The repository's Linux installer stages these for review and is used in drills on disposable hosts.
It is not a supported installation.

| Path | What it is |
| --- | --- |
| `/usr/local/bin/bench`, `/usr/local/bin/benchd` | The CLI and the daemon. |
| `/usr/local/libexec/bench-quota` | The fixed-policy quota helper (the `--quota-helper` default). |
| `/usr/local/libexec/bench-payload` | Managed hosts only: the static payload helper. |
| `/usr/local/libexec/superbench-start` | The root-owned wrapper that starts `benchd` with its delegated cgroup. |
| `/etc/superbench/storage.json` | Root-owned managed storage policy: data root, ledger, owner uid and gid, byte and inode caps, reserves. |
| `/etc/superbench/benchd.env` | Root-owned daemon settings: operator and reader uids, active limit, intervals. Written once by the installer, then the operator's: validated, never replaced, kept across upgrades. |
| `/etc/sudoers.d/superbench` | Managed hosts only: sudo for the quota helper and the argument-free payload helper, nothing else. |
| `/var/lib/superbench/state` | The service account's private state. |
| `/run/superbench/benchd.sock` | The service socket (parent 0750, socket 0660). |
| `/sys/fs/cgroup/superbench` | The default delegated cgroup root. The installed wrapper finds the service's real cgroup instead. |
| `<ledger>/payload-retirements/<key>.json` | A reviewed Fresh retirement plan, root-owned 0600. |

The storage policy's example uses `/srv/superbench-data` for the data mount and
`/var/lib/superbench-quota` for the privileged ledger; both are choices made at provisioning, not
fixed paths. Keep the ledger and state outside the bounded data filesystem.
