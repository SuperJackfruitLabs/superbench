---
title: What SuperBench is
description: One task, one git worktree on a host, kept warm between holders, with checkpoints the remote confirms. SuperBench v0.0.2 is an experimental single-host release.
status: wet paint
---

SuperBench gives each task its own git worktree on a host, called a **bench**, and keeps it warm
between the agents or people who take turns on it. One holder works in a bench at a time. Before
the bench can pass to the next holder, the work has to be committed, pushed to the task branch and
confirmed on the remote.

It is a Go command-line tool, `bench`, and an optional local daemon, `benchd`, that serves the same
operations over a Unix socket. It has no TCP listener (read from the source; no test asserts it, see
the [Security model](/start/security-model/)) and depends on no other suite product. Output is JSON.

## Why a bench

A coding task usually needs a checkout, installed dependencies and a build cache before any useful
work starts. When several agents take turns on one task, each starting from a fresh clone is slow,
and sharing one checkout without rules loses work. A bench is the middle ground:

- **One task, one checkout.** `bench create` makes a worktree for the task from a shared git
  mirror, on its own task branch (`bench/<key>` by default), and runs the repository's setup.
- **Kept warm.** The checkout, its ignored build output and its caches stay on the host between
  holders, so the next holder starts where the last one stopped.
- **One holder at a time.** `bench lease` grants the bench to one holder and refuses other `bench`
  acquisitions until the holder releases it. See [Leases](/concepts/leases/) for what that does and does
  not stop.
- **Checkpoints the remote confirms.** `bench checkpoint` pushes the existing HEAD without force
  and fails unless the remote branch is exactly that commit. `bench release` refuses until that is
  true. See [Checkpoints](/concepts/checkpoints/).
- **Recoverable, in part.** Because the committed work is on the remote, `bench recover` can rebuild
  a bench from its remote-confirmed task branch into a separate, empty state and data pair.
  Recovery into existing state is not supported, and no second-host recovery drill exists yet; see
  [Status](/start/status/).

The basic loop, from the repository README (flags precede the task key):

```sh
bench create --repo /absolute/path/to/remote.git fix-login
bench lease --holder first-agent fix-login
# Save Lease.Token from the response in BENCH_LEASE_TOKEN and cd to Path.
# Edit files, git add, and git commit as usual. Then:
bench checkpoint fix-login
bench release fix-login
bench lease --holder second-agent fix-login
bench status fix-login
```

The loop above shows what the commands do. To get the commands, [Install v0.0.2](/start/install/)
downloads and verifies the release archive; it is not a supported installation.

The loop as written needs Linux. Whenever the repository has setup hooks or a supported lockfile,
warm `create`, `recover`, `lease` and `setup` prepare the checkout in a fenced execution, which
needs Linux cgroup delegation. On a laptop the cooperative sequence is `create-cold`, then
`lease-repair`, then `prepare-local`, then `acknowledge-local-preparation`; see
[Supervised runs and setup](/concepts/supervised-runs/#preparing-without-linux).

## What it is not

- **Not production software.** SuperBench v0.0.2 is an experimental single-host release for Linux
  amd64. There is no supported installation, and nothing here is in production use.
- **Not a sandbox.** Leases are cooperative, and supervised runs on Linux are fenced in a cgroup,
  which is process-tree fencing; there is no user or mount isolation. SuperBench is for trusted
  repositories. See the [Security model](/start/security-model/).
- **Not multi-host.** It runs on one host. Moving a bench to another host is a reviewed
  administrative procedure, not network coordination.
- **Not integrated with the rest of the suite.** Suite adapters, remote authentication and a web
  interface are not built.

## Where to go next

- [Status](/start/status/): what works in v0.0.2, what is partial, what is not built.
- [Install v0.0.2](/start/install/): download, verify and stage the release.
- [Benches and workspaces](/concepts/benches-and-workspaces/): the two shapes a bench takes.
- [bench CLI](/reference/bench/): every command and flag as the source defines them.
