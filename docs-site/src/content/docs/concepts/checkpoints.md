---
title: Checkpoints
description: A checkpoint pushes the bench's existing HEAD without force and confirms the remote has exactly that commit. Release and recovery depend on it.
status: wet paint
---

A **checkpoint** makes a bench's committed work durable on the remote.

```sh
bench checkpoint fix-login
```

It pushes the existing HEAD to the task branch (`HEAD:refs/heads/<branch>`, where `<branch>` is
`bench/<key>` unless `--branch` named another at creation) with no force option, then reads the remote branch back with `ls-remote` and fails unless it is exactly the
checkpointed commit.

## What it guarantees

- **It never makes a commit.** Commit with git as usual; a checkpoint publishes what is already
  committed. There is no implicit `git add`.
- **It never force-pushes.**
- **A dirty or untracked tree is refused.** Untracked files block checkpoint and release; ignored
  build files do not.
- **A rejected push fails the checkpoint and keeps the lease**, and release stays blocked.
- **Release requires it.** `bench release` needs a clean worktree, the current HEAD checkpointed,
  and the remote still matching.

The repository's tests cover a dirty or untracked tree being refused, a wrong token being refused,
release requiring a checkpoint, a fresh host recovering the same commit from the remote branch, and
a remote that rejects the push. The remote match itself is enforced in code; no test forces a
mismatch, so that refusal is read from source rather than exercised.

## What it does not guarantee

- The guarantee is about the pushed commit, not the remote's other branches.
- Git and state updates are not one transaction. If saving local state fails after the push, the
  checkpoint may need retrying.
- Ignored files are not part of a checkpoint, so they do not recover.

## Recovery from a checkpoint

On a replacement host with empty state and data directories:

```sh
bench recover --repo /absolute/path/to/remote.git --branch bench/fix-login fix-login
```

Only remote-confirmed committed files recover. The implementation does not rebuild a lost checkout
in an existing state store or reset a conflicting local branch. Keep the old files and state
intact, stop old writers, and use a separate replacement state and data pair.

## Workspace checkpoints

A [workspace](/concepts/benches-and-workspaces/) checkpoints its whole member vector as one
**generation**. Each member's task branch is pushed without force and confirmed, then pinned with a
lightweight tag:

```text
refs/tags/superbench/pins/<bench_id>/<generation_id>/<member>
```

After every task branch and pin is confirmed, a commit whose tree holds only a generated
`checkpoint.json` seals the generation on a metadata branch:

```text
refs/heads/superbench/checkpoints/<bench_id>
```

Only a normal fast-forward push with exact remote confirmation completes the seal. This is a
completion boundary across remotes, not an atomic cross-repository push: earlier members' pushes
stay in place after a later failure, and the generation stays incomplete. Pins are never
overwritten or deleted. Control pushes skip local pre-push hooks, which could otherwise start
unfenced writers; server receive hooks still apply.
