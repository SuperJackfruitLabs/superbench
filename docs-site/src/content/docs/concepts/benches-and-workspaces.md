---
title: Benches and workspaces
description: A bench is one task's git worktree on one host. A workspace is a bench across several repositories, with one lease and one checkpoint vector.
status: wet paint
---

## A bench

A **bench** is one task's git worktree, identified by a **key** (letters, digits, `_` and `-`, up
to 80 characters, starting with a letter or digit). It lives under the data directory at
`benches/<key>` and works on a task branch, `bench/<key>` unless `--branch` names another, cut from
a base branch (`main` by default).

Benches share git objects through a bare **mirror** per repository. A fetch updates only
remote-tracking refs, never a branch another bench has checked out. Do not prune mirrors or alter
their refs by hand.

`bench create` records a durable **creation intent** (task, repository, branch, target path and
the pinned source commit) and syncs it before the worktree is made. If create stops after making
the worktree but before registering it, repeat the command with exactly the same arguments: the
retry adopts only the exact clean checkout the intent describes. Dirty, changed or conflicting
checkouts are preserved for review, never reset.

Creation and recovery also prepare the checkout before they report it ready (see
[Supervised runs and setup](/concepts/supervised-runs/)). `create-cold` and `recover-cold` are local
administrative commands that register without setup; they are never a fallback from a failed warm
create.

## Lifecycle

A bench moves through create, lease, work, checkpoint, release, and in the end archive and reap:

1. `create` (or `recover`) registers and prepares the checkout.
2. `lease` grants it to one holder. See [Leases](/concepts/leases/).
3. The holder commits as usual. `run` executes one supervised command instead, on Linux.
4. `checkpoint` pushes the existing HEAD and confirms it. See [Checkpoints](/concepts/checkpoints/).
5. `release` frees the bench for the next holder.
6. `archive` freezes a released, clean, confirmed bench read-only; `plan-reap` and `reap` delete it
   after review. See [Archive and reap](/concepts/archive-and-reap/).

`status` is read-only at every step and never migrates state.

## A workspace

A **workspace** is a bench that spans several repositories: 1 to 32 members, each with a logical
name, its repository, and optionally a branch and base. One member is the **checkpoint
repository**. The whole workspace has one lease, one total quota allocation and one checkpoint
vector across every member.

Workspaces are reached through the v2 API and `bench workspace OP`; the scalar top-level commands
keep their single-repository behavior, and v2 mutations refuse scalar records. A member list is a
JSON array:

```json
[
  {"name":"app","repository":"/absolute/path/to/app.git"},
  {"name":"library","repository":"/absolute/path/to/library.git"}
]
```

```sh
bench workspace create --socket /run/superbench/api.sock \
  --repositories members.json --checkpoint-repository app \
  --request-id create-task-1 task-1
bench workspace run --socket /run/superbench/api.sock \
  --holder run-1 --repository-name app --request-id run-task-1 \
  task-1 -- go test ./...
bench workspace status --socket /run/superbench/api.sock task-1
```

Workspace creation and recovery always initialize every member and confirm a first checkpoint (the
**genesis**) on the remote before reporting ready. Recovery creates a fresh workspace with distinct
task refs from an exact historical seal. Moving original task refs to a new workspace is a separate
reviewed transfer.

The grouped request and response shapes are proposed contract data. They are implemented and
tested here, but no suite consumer uses them yet.
