---
title: bench CLI
description: Every bench command and flag as cmd/bench defines them on main. Flags precede the key; output is JSON.
status: wet paint
---

`bench` is the command-line front door. Every command takes its flags **before** the task key.
Output is JSON. With `--socket`, an operation goes to `benchd` and prints the versioned API result;
without it, the command works directly on the local state and data directories and prints the
engine's own format. This page is checked against `cmd/bench` when the docs build.

`bench version` prints `bench 0.0.1` for the v0.0.1 release build and `bench dev` for a build from a
branch. [Install v0.0.1](/start/install/) gets the release build; it is not a supported installation.

## Bench lifecycle

| Command | What it does |
| --- | --- |
| `create` | Register a task worktree from `--repo` on its task branch and prepare it. |
| `recover` | Rebuild a bench from its remote-confirmed task branch (`--repo`, `--branch`) into empty state. |
| `lease` | Grant the bench to `--holder` and print its token once. Refreshes the shared mirror first. |
| `checkpoint` | Push the existing HEAD without force and confirm the remote has exactly that commit. |
| `release` | Free the bench. Needs a clean, checkpointed, still-confirmed worktree. |
| `status` | Read one bench, or all of them. Read-only; never migrates. |
| `run` | `bench run [flags] KEY -- COMMAND [ARGS]`: lease and run one fenced command (Linux). |
| `stop` | Fence the holder's supervised execution; `--admin` stops it without the token (local operator). |
| `setup` | Run the repository's preparation in a fenced execution (Linux). |

## Local administration

These work on local state only, as the trusted service owner, with the same `--state` and `--data`
the daemon uses.

| Command | What it does |
| --- | --- |
| `create-cold` | Register a checkout without setup. Never a fallback from a failed `create`. |
| `recover-cold` | Recover without setup. |
| `lease-repair` | A maintenance lease that skips the mirror refresh. |
| `migrate` | Upgrade the state schema explicitly. |
| `configure-host` | Record host storage settings: quota helper, free-space minimums, log budgets. |
| `host-status` | Read host storage and admission state. |
| `archive` | Freeze a released, clean, confirmed bench read-only (`--archive-grace`). |
| `plan-reap` | Print the concrete deletion plan for an archived bench. |
| `reap` | Delete exactly the approved plan (`--plan`, `--approve`) after the grace period. |
| `archive-run` | One sweep of automatic archive candidates (`--limit`, `--after-key`). |
| `history` | Run history for one bench, or all. |
| `inspect-log`, `adopt-log`, `reconcile-log` | Inspect, adopt or reconcile a retained run log. |
| `plan-collect`, `collect`, `resume-collect`, `cancel-collect`, `collections` | Reviewed log collection. |
| `retention-preview`, `retention-enable`, `retention-disable`, `retention-status`, `retention-run`, `retention-ack-pending` | The automatic log retention policy, off until enabled. |
| `prepare-local`, `acknowledge-local-preparation`, `local-preparation-status` | Cooperative preparation without Linux fencing. |
| `handoff` | Reviewed cross-host transfer: `offer`, `inspect-export`, `begin-export`, `continue-export`, `import`, `continue-import`. |
| `mirror-clones` | Reviewed collection of unpublished mirror clones: `inspect`, `collect`, `inspect-root-removal`, `reconcile-root-removal`, `forget`. |
| `workspace` | Grouped workspace operations; see below. |
| `version` | Print the build's version. |

`--request-id` is accepted only by `create`, `recover`, `lease`, `run` and `setup`, the operations
the daemon replays.

## Flags

The scalar commands share one flag set. Defaults that depend on the OS use Go's user directories:
the config directory for state and the cache directory for data (on Linux `~/.config` and
`~/.cache` unless the XDG variables say otherwise; on macOS `~/Library/Application Support` and
`~/Library/Caches`).

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state` | `<config dir>/superbench/state` | Authoritative state, outside bench data. |
| `--data` | `<cache dir>/superbench/data` | Mirrors and checkouts. |
| `--socket` | (none) | Local benchd Unix socket; operation output uses the versioned API shape. |
| `--request-id` | (none) | Stable ID for replaying an accepted service request. |
| `--repo` | (none) | Git remote URL or absolute path. |
| `--branch` | `bench/<key>` | Task branch. |
| `--base` | `main` | Base branch. |
| `--holder` | (none) | Lease holder. |
| `--token` | `$BENCH_LEASE_TOKEN` | Lease token; prefer the environment variable. |
| `--cgroup-root` | `/sys/fs/cgroup/superbench` | Delegated Linux cgroup-v2 root for fenced execution. |
| `--checkpoint` | `true` | Checkpoint and release a successful supervised command. |
| `--admin` | `false` | Local operator: stop a supervised execution without its holder token. |
| `--offline` | `false` | Offline frozen dependency setup. |
| `--mirror-timeout` | `2m0s` | Deadline for mirror clone, validation and fetch; the socket uses the daemon's setting. |
| `--quota-helper` | `/usr/local/libexec/bench-quota` | Fixed-policy quota helper. |
| `--quota-policy` | (none) | Policy override for direct root testing and administration. |
| `--min-data-free` | `67108864` | Minimum free bytes on the data filesystem. |
| `--min-state-free` | `1048576` | Minimum free bytes on the state filesystem. |
| `--max-log-bytes` | `1048576` | Maximum captured output bytes per command. |
| `--max-logs-bytes` | `67108864` | Maximum retained and reserved run-log bytes. |
| `--max-log-files` | `4096` | Maximum retained run logs before admission stops. |
| `--archive-grace` | `168h0m0s` | Local archive retention before reviewed reap; zero explicitly waives it. |
| `--plan` | (none) | Private reviewed administrative plan JSON. |
| `--approve` | (none) | Exact `Digest` of the reviewed plan. |
| `--limit` | `100` | History and collection result limit. |
| `--after-key` | (none) | Archive sweep cursor from the previous report's `Next` field. |
| `--older-than` | `168h0m0s` | Minimum time since release and log seal. |
| `--metadata-older-than` | `720h0m0s` | Minimum time since prior log collection. |
| `--max-collect-bytes` | `67108864` | Maximum log bytes per automatic collection cycle. |
| `--enable-automatic` | `false` | Approve future automatic collections under the reviewed policy. |
| `--acknowledge-stopped-writers` | `false` | Operator confirms legacy log writers were stopped. |

## bench workspace

`bench workspace OP [flags] [key]` runs grouped operations. `--socket` selects the v2 API on the
daemon; without it, the command uses local state. The client never retries on its own or falls back
to v1.

Operations: `create`, `recover`, `lease`, `setup`, `run`, `checkpoint`, `release`, `stop`,
`stop_admin`, `archive`, `reclaim`, `status`, `host_status`, `plan-reap`, `reap`, the local root-only
`renew_admin`, and the reviewed local procedures `inspect-history`, `reconcile-history`, `inspect-initialization`,
`claim-initialization`, `continue-initialization`, `inspect-transfer`, `begin-transfer`,
`continue-transfer`, `inspect-missing-transfer`, `begin-missing-transfer`,
`continue-missing-transfer`, `inspect-root-removal` and `reconcile-root-removal`.

| Flag | Meaning |
| --- | --- |
| `--repositories` | File containing the canonical repositories JSON array. |
| `--checkpoint-repository` | Logical checkpoint coordinator member. |
| `--source` | File containing the exact recovery source JSON object. |
| `--repository-name` | Selected member; omitted, `run` uses the root and `setup` uses all members. |
| `--holder` | Workspace-wide lease holder. |
| `--token` | Holder token; ordinary operations accept `BENCH_LEASE_TOKEN`. |
| `--request-id` | Reuse this growth request ID after an interrupted reply. |
| `--socket` | benchd Unix socket; uses v2 only. |
| `--initialize` | Warm initialization is mandatory (default `true`). |
| `--checkpoint` | Checkpoint and release a successful payload (default `true`). |
| `--quota-bytes` | One total workspace budget, bounded by host policy. |
| `--offline` | Offline frozen setup. |
| `--archive-grace` | Local archive grace; zero explicitly waives retention (default `168h0m0s`). |
| `--plan` | Concrete reviewed local administration manifest file. |
| `--approve` | Exact reviewed plan `Digest`. |
| `--repair-id` | Explicit local initialization repair identity. |
| `--rerun-once` | Review uncertain once hooks for these comma-separated members. |
| `--transfer-id` | Explicit reviewed local transfer identity. |
| `--target` | Fresh destination key for reviewed local transfer. |
| `--disposition-id` | Fresh identity for explicit current empty-directory removal. |
| `--cgroup-root` | Delegated local cgroup root (default `/sys/fs/cgroup/superbench`). |
| `--mirror-timeout` | Local managed git deadline (default `2m0s`). |
| `--state`, `--data` | Local state and data, with the same defaults as above. |

### A stopped lease that cannot release

A run can stop with files the checkpoint refuses, such as stray untracked files at the tree root.
`release` then returns `409 conflict` ("workspace has uncommitted or untracked files"), and the
stopped owner cannot run again: a stop revokes its launch authority for good. As root on the host:

```sh
bench workspace stop_admin --socket SOCKET KEY   # fences the run; the lease stays held and stopped
bench workspace renew_admin --state STATE --data DATA \
  --cgroup-root CGROUP_ROOT --mirror-timeout TIMEOUT KEY   # prints grant_token and root_path
export BENCH_LEASE_TOKEN=NEW_TOKEN               # the grant_token renew_admin printed
bench workspace run --socket SOCKET --holder HOLDER KEY -- COMMAND
bench workspace release --socket SOCKET KEY      # only if the lease is still held
bench workspace reclaim --socket SOCKET KEY      # optional, for a Fresh workspace
```

`renew_admin` is a local verb, not a socket call: it opens state directly, so it has no `--socket`.
Without `--state` and `--data` it uses the CLI's own defaults under the caller's config and cache
directories, not the daemon's. Pass the same `--state`, `--data`, `--cgroup-root` and
`--mirror-timeout` that `benchd` runs with; on a packaged host the state is
`/var/lib/superbench/state` (see [Files and paths](/reference/files-and-paths/)) and the data root is
the one the storage policy names. A refusal of `unknown workspace` means there is no workspace for
`KEY` in the state it read: usually the wrong `--state`, otherwise a mistyped key.

`renew_admin` issues a new token, with fresh launch authority, to the same holder. It takes no flags
beyond `--state`, `--data`, `--cgroup-root` and `--mirror-timeout`, and it refuses with a fixed message
unless the caller's real and effective user IDs are 0, the workspace is ready with no archive,
transfer, initialization repair or pending checkpoint, its owner was stopped and its run is stopped,
and no daemon operation is active on it. The old token stays stopped and is refused afterwards.
`COMMAND` is the fix: commit the files or remove them. `run` checkpoints and releases on exit zero by
default; otherwise `checkpoint` and `release` with the new token finish the job. The other verbs here
go through the daemon with `--socket`; run locally instead, they need the same `--state` and `--data`.
