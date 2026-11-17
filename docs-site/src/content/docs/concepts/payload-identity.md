---
title: Payload identity and the fixed helpers
description: The two fixed-policy privileged helpers, bench-quota and bench-payload, and the separate payload user that main does not yet install and activate end to end.
status: wet paint
---

SuperBench's daemon runs unprivileged. The few things that need root go through two small,
**fixed-policy** helpers that take logical requests, never arbitrary paths, limits or commands. On a
managed host the only sudo rule permits exactly these two executables.

## bench-quota: the quota helper

`bench-quota` (installed as `/usr/local/libexec/bench-quota`) applies and retires XFS project
quotas for benches, the shared store and the shared mirrors. It reads the root-owned policy
`/etc/superbench/storage.json`; paths, byte and inode ceilings and worker ownership all come from
that policy, never from the caller. It uses fixed absolute XFS tools and a fixed environment. A
policy override (`--policy`) is accepted only from a direct root caller, never through sudo.

Its retirement authority is narrow: it removes only an empty logical bench directory, after XFS
reports zero blocks and zero inodes for that allocation. It never deletes files recursively and is
not a tree-deletion interface. See [Quotas, mirrors and stores](/concepts/quotas-and-stores/).

## bench-payload: the payload helper

`bench-payload` (installed as `/usr/local/libexec/bench-payload` on managed hosts) is the boundary for running a task's payload as a separate **payload user**. It is
a dedicated helper, never a daemon:

- It must be a **static** Linux executable (built with cgo off); the installer refuses one with a
  dynamic interpreter or shared-library dependency. On other platforms it refuses with
  `unsupported`.
- It accepts **no command-line arguments**; sudo permits it only with the explicit empty argument
  string. It reads one bounded request frame from standard input.
- It validates its fixed policy, the caller, the cgroup scope and the private payload layout, then
  drops credentials and executes. A credential drop may be irreversible, so on any error it exits
  rather than retrying or restoring privileges, and it prints only a closed refusal code.
- The payload's environment is rebuilt, not inherited: a fixed `PATH`, `HOME` and `TMPDIR` from the
  validated private payload layout, git's global and system configuration turned off, and only a
  small allowlist of locale and CI variables. Daemon credentials, loader hooks, shell startup
  programs and sudo metadata do not pass through.

## What is and is not active on `main`

This is the caveat that travels with the payload identity: **`main` installs the trusted
service-account execution model; it does not yet install and activate the complete separate
payload-user path.** The helper, the private payload policy and service registry it requires, the
fresh registered layout, and the routing of run, setup and git through it must all land and be
verified together before that boundary can be claimed. The packaging checks alone do not prove
payload isolation.

Until then, supervised commands run with the launching account's authority, inside a fenced
cgroup. That is the boundary described in [Supervised runs and setup](/concepts/supervised-runs/) and
the [Security model](/start/security-model/): lifecycle fencing for trusted commands, with no uid,
mount or network isolation per bench.
