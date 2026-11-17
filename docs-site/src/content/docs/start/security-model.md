---
title: Security model
description: Who SuperBench trusts and what it enforces. Root, the daemon account and the payload user; cooperative leases; trusted repositories on one host.
status: wet paint
---

SuperBench coordinates **trusted** callers working on **trusted repositories** on **one host**. It
is not a sandbox, and the code does not isolate untrusted code beyond what this page says. Read
this before anything else on the site.

## The assumptions

- **Trusted repositories.** Repository setup hooks are shell code, run as the service user. Local
  git configuration and hooks apply. Only trusted repositories are suitable.
- **Cooperative callers.** A lease stops other `bench` acquisitions. It does not stop direct edits,
  an arbitrary shell, or another process running as the same user.
- **One host.** State, leases and checkpoints are local. There is no network lease coordination.
- **Trusted operators.** An operator on the local API is trusted to run arbitrary commands as the
  daemon's user.

## The accounts

| Account | Owns or does | Notes |
| --- | --- | --- |
| root | Installed binaries and configuration, the storage policy `/etc/superbench/storage.json`, the privileged quota ledger, and the parents of managed data | The staged unit runs `benchd` as the unprivileged `superbench` account; `benchd` itself does not refuse root (its default `--operator-uids` includes 0). Reap refuses root. |
| The daemon account (`superbench` in the staged installer) | Private state (mode 0700) and runtime, the bench, cache and mirror directories it is assigned | A stable account, so lease and recovery ownership survive restarts. This is a trusted coding-agent account, not tenant isolation. |
| The payload user | A separate identity for running a task's payload, entered through the fixed payload helper | Present in source. `main` does not install and activate the complete separate payload-user path, so payloads today run with the launching account's authority. |

Managed hosts get one sudo rule, `/etc/sudoers.d/superbench`. It permits only the fixed quota
helper, which takes its logical allocation arguments and refuses `--policy` from sudo callers, and
the dedicated payload helper, pinned to no command-line arguments. There is no generic shell or XFS
sudo, and a sudo caller cannot select another policy. See
[Payload identity and the fixed helpers](/concepts/payload-identity/).

## What is enforced

- **Socket authorization by kernel peer credentials.** `benchd` reads the connecting process's uid
  from the kernel (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS). Identity headers cannot grant
  authority. Operators can mutate; readers can only query `status` and `host_status`. The socket is
  mode 0660 and its parent must not be group- or world-writable.
- **No TCP listener.** The only listener in the non-test source is a Unix socket. This is read from
  the source; no test asserts the absence of a TCP socket, so it holds only as long as the code
  does.
- **Lease tokens are holder capabilities.** A token is returned only on acquisition, on an
  administrative stop (`stop --admin`) or on exact replay of a completed request's receipt. Status
  and checkpoint output redact it, and supervised commands do not receive it in their
  environment.
- **Checkpoints never rewrite history.** A checkpoint pushes the existing HEAD without force and
  confirms the remote has exactly that commit. Nothing in that path makes a commit.
- **Process-tree fencing on Linux.** A supervised run is born inside its own delegated cgroup-v2
  scope, and stop kills the whole scope (`cgroup.kill`), including detached `setsid` descendants,
  before checkpoint or release.
- **Byte and inode caps on managed Linux XFS hosts**, through a fixed-policy root helper that
  accepts logical allocations only.
- **Archived trees are read-only to ordinary writes**, and deletion needs an exact reviewed
  manifest after a grace period.

## What is not enforced

- **No uid, mount or network isolation per bench.** Neither cooperative nor supervised mode
  provides it. A supervised command has the launching user's authority, so a deliberate cgroup
  escape by that user is outside the boundary. The repository calls this lifecycle fencing for
  trusted commands, not a sandbox.
- **Direct local commands bypass the socket's uid policy.** A `bench` command run without
  `--socket` on the daemon's state does not go through the socket's operator and reader checks. It
  is bounded only by the state directory being 0700 and owned by the daemon account.
- **Cooperative leases do not fence anything.** A shell or editor opened after `bench lease` is
  outside SuperBench's control. Only `run` and `setup` create supervised scopes.
- **No automatic lease expiry.** A crashed holder keeps its lease until someone inspects it.
- **The archive permission boundary is cooperative.** The trusted owner can `chmod` an archived
  tree and root can bypass mode bits. Already-open writable handles are not revoked.
- **Cross-host handoff files are unsigned.** Their digests bind bytes, not machines or operators.
- **Git and network calls can block** without an application deadline, and no timeout proves a
  writer stopped.
- **The managed sudo path is retained on purpose.** Adding `NoNewPrivileges` or a capability
  filter would break the helpers; do not treat the service unit as a hardened sandbox.

## Secrets

Treat saved state and acquisition output as private: they hold lease tokens. Prefer the
`BENCH_LEASE_TOKEN` environment variable to `--token`, which exposes the token in process
arguments. Repository credentials belong in the service account's own credential mechanism, never
in a repository, a tracked environment file or a prompt.
