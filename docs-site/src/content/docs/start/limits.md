---
title: Limits
description: The deliberate limits of the SuperBench prototype, its bounds and what it refuses to do, from the repository README and docs.
status: wet paint
---

These are the limits the repository states. Some are deliberate and will stay; others are
unfinished work. [Status](/start/status/) says which is which.

## Deliberate

- **Leases never expire because time passed.** No process is reassigned by time. A crashed holder
  keeps its lease; stop and inspect it before using its saved token to checkpoint or release. If a
  supervised holder's token is lost, `bench stop --admin` fences the recorded scope and returns the
  retained lease.
- **Nothing is auto-committed, force-pushed, reset or deleted to make recovery succeed.**
  Conflicting files are preserved for review.
- **Untracked files block checkpoint and release; ignored build files do not.** Ignored files stay
  in the bench but are not recoverable from the remote.
- **Recovery restores only remote-confirmed committed files.** Ignored build products, untracked
  files and unpushed commits do not come back. Recovery needs the remote task branch to exist.
- **State stays outside bench data.** The state and data directories cannot overlap, including
  through symlinks. Use local filesystems, and the same paths on every invocation.
- **All commands serialize** under one host-wide OS file lock, including their git calls.
- **Git and state updates are not one transaction.** Journaled worktree creation can be resumed;
  a pushed checkpoint may need retrying if saving state failed afterwards.

## Bounds

| What | Bound | Source |
| --- | --- | --- |
| API request body | 64 KiB | `docs/service.md` |
| API response | 1 MiB (read-only responses); durable growth responses 64 KiB on v2 | `docs/service.md`, `docs/workspace-api.md` |
| Concurrent connections | 64 | `docs/service.md` |
| Active operations | 8 by default, 1 to 64 | `benchd --max-active` |
| Request receipts | 64 KiB reserved per pending result, at most 10,000 rows and 64 MiB of replies | `docs/service.md` |
| Repositories in a workspace | 1 to 32 | `internal/protocolv2` |
| Run arguments | 1 to 128, each at most 8192 bytes | `internal/protocolv2` |
| `.bench/config.toml` | 64 KiB, each command at most 16 KiB | `docs/setup.md` |
| Reap manifest | 100,000 entries, 16 MiB encoded | `docs/archive.md` |
| Archive grace | 0 to 365 days, seven days by default | `docs/archive.md` |
| Mirror deadline | two minutes by default, up to one hour | `docs/mirrors.md` |
| Captured output per command | 1 MiB by default (`--max-log-bytes`) | `bench` flags |

Receipt exhaustion refuses new growth (create, recover, lease, run, setup) until reviewed
retention exists, but stop, checkpoint, release and archive stay available. Do not delete receipts
by hand to retry uncertain work. There is no pagination yet: an overlarge status fails, so query
one key.

## Unfinished

- Supervised runs and setup need Linux with a delegated cgroup-v2 root. On macOS and unsupported
  Linux hosts, a fenced run refuses before it takes a lease. There is no silent fallback.
- Quotas are enforced only on managed Linux hosts with a dedicated XFS mount. Elsewhere a quota
  request is recorded and not enforced.
- Shared-cache, mirror and receipt collection are not built.
- A response schema and SDK for the local API are still pending; the shapes are proposed contract
  data.
- Remote authentication, a UI and suite integrations are not built.
