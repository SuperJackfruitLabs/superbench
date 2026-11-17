---
title: Supervised runs and setup
description: On Linux, bench run and bench setup fence a command's whole process tree in a delegated cgroup before checkpoint and release. Fencing, not isolation.
status: wet paint
---

`bench run [flags] KEY -- COMMAND [ARGS]` acquires a lease and runs one noninteractive command in
the bench's checkout, fenced in its own cgroup. `bench setup` runs the repository's preparation
the same way.

```sh
bench run --holder reviewer --checkpoint=false --cgroup-root /delegated/cgroup fix-login -- sh ./verify.sh
```

**Linux only.** The runner needs a writable, delegated cgroup-v2 subtree (`--cgroup-root`), a
stable machine identity, `clone3` with `CLONE_INTO_CGROUP`, and `cgroup.kill`. On macOS and
unsupported Linux hosts a fenced run refuses before it takes a lease; there is no silent
process-group fallback. The CLI does not create a delegation under the system root by itself.

## What a run does

1. State records a random scope ID, a stable host hash, the boot identity, a starting phase and the
   holder token, before launch.
2. The command is born inside its scope rather than moved there afterwards. The lease token is
   removed from its environment. Output goes to a private run log in state storage.
3. Stop marks the execution stopped only after the kernel reports no remaining members in the
   scope and the empty scope can be removed. Detached `setsid` descendants stay in the cgroup and
   are fenced too.
4. **On success** the command is fenced, then its existing committed HEAD is checkpointed and the
   lease released. There is no implicit `git add` or commit. `--checkpoint=false` keeps a stopped
   lease even on success.
5. **On failure** (a nonzero exit, dirty files, a rejected push, an ambiguous save) the lease stays
   held for inspection. A nonzero exit status propagates through the CLI once fencing completed.

```sh
bench stop --token "$BENCH_LEASE_TOKEN" fix-login
bench stop --admin fix-login
```

Killing the supervisor does not free the lease; the cgroup stays recorded so another local process
can fence the surviving tree. A different host is refused, while a new boot of the same host proves
its old processes cannot still be running. Copying state between hosts is not a supported handoff.

## Fencing is not isolation

This is the caveat that travels with every claim about supervised runs: **it is process-tree
fencing, not a sandbox.** No per-bench uid, mount or network isolation is implemented. Commands
have the launching user's authority, so a deliberate cgroup escape by that user is outside the
boundary. Only trusted repositories are suitable. A payload run under a separate user is not done;
see [Payload identity and the fixed helpers](/concepts/payload-identity/).

## Setup hooks

`bench setup` reads `.bench/config.toml` in the checkout:

```toml
[setup]
once = "pnpm install --frozen-lockfile"
each = "pnpm install --offline --frozen-lockfile"
```

The hooks are trusted repository shell code, run as the account running `bench` or `benchd`, with
`/bin/sh -e`, each in
its own subshell rooted at the checkout. `once` runs on the first successful preparation; later
acquisitions skip it only when the exact configuration bytes match a successful receipt for that
bench. `each` runs before every handoff. Changing any byte of the configuration, comments included,
invalidates the match. Hooks must be idempotent: shell side effects are not an exactly-once
transaction.

With no hooks, setup uses a frozen install from exactly one supported pnpm or npm lockfile; a
missing or ambiguous lockfile refuses. `--offline` selects offline frozen setup, and for custom
hooks exports `BENCH_SETUP_OFFLINE=1`; it does not claim network isolation.

Configured hooks also run before `run` and before a cooperative `lease` is returned, and
`create` and `recover` prepare the checkout before reporting it ready. With hooks, `run` prepares in
a separate fenced execution, then moves the same lease straight to the command, with no release
and reacquire gap. Setup failure, cancellation or a changed configuration leaves the lease held.

## Preparing without Linux

Supervised preparation needs Linux. Whenever the repository has setup hooks or a supported pnpm or
npm lockfile, warm `create`, `recover`, `lease` and `setup` prepare the checkout in a fenced
execution and so need Linux cgroup delegation; on macOS they refuse rather than fall back. The
laptop sequence is:

```sh
bench create-cold --repo /absolute/path/to/remote.git --branch card/example example
bench lease-repair --holder first-writer example
bench prepare-local example
bench acknowledge-local-preparation --digest REVIEWED_DIGEST --acknowledge-stopped-writers example
bench checkpoint example
bench release example
```
 On macOS and Linux, `bench prepare-local` runs the same
preparation under an explicit cooperative lease, for trusted operators. It does not fence
processes or prove that descendants stopped. Review the result, stop every writer yourself, then
acknowledge the exact attempt digest with
`bench acknowledge-local-preparation --digest REVIEWED_DIGEST --acknowledge-stopped-writers KEY`
before checkpoint and release.
