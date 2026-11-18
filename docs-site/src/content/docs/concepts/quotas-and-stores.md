---
title: Quotas, mirrors and stores
description: Per-bench byte and inode caps on managed Linux XFS hosts, shared git mirrors refreshed at acquisition, and shared or checkout-local package caches.
status: wet paint
---

## Quotas, on managed hosts only

On a **managed** Linux host, each bench gets its own byte and inode hard caps on a dedicated XFS
filesystem with enforcing project quotas, and the shared package store and shared mirrors get
their own caps. A root-owned policy, `/etc/superbench/storage.json`, sets the ceilings and the
reserve.

The caveat that travels with this: **managed hosts only.** Enforcement needs Linux, a dedicated XFS
mount provisioned by the operator, root-owned policy and the privileged quota helper. On macOS and
unmanaged hosts a quota request is **recorded and not enforced**. The test that exercises real XFS
enforcement is skipped unless a disposable managed XFS fixture is present, so the evidence for
enforcement is the repository's recorded drills on loop-backed XFS images, not a host in use. Those
recorded runs are historical: at least one names an uncommitted working snapshot of a feature
branch, so they are not runs of the current commit.

Project quotas alone do not hold against a file's owner. The owner can move its own files to
another project, or clear the project-inherit flag on its own directories, without any capability.
A Fresh bench's tree belongs to the payload account, so SuperBench confines that account. Every
process started through the payload helper runs under a seccomp filter. The filter refuses the
attribute-setting `ioctl` requests, `file_setattr` and io_uring, so those changes fail with
`EPERM`. Separately, the helper's read-only verify refuses a bench whose leaf directory has lost
its project ID or inherit flag. This holds only for processes that start through the helper. Before
you turn Fresh on, and after each upgrade, run `bench-quota scan-projects` as direct root: it walks
every bench's tree and reports any bench outside its project.

What managed storage does:

- **Admission.** Create, recover, new leases and supervised runs check free-space reserves on the
  data and state filesystems before starting work (`--min-data-free`, `--min-state-free`). Managed
  admission also rechecks quota enforcement through the helper.
- **Fixed-policy allocation.** The helper accepts logical allocations only: `apply bench KEY
  [BENCH_BYTES]`, `apply shared store`, `apply shared mirrors`, and `release bench KEY PROJECT_ID`.
  Every allocation is journaled before it touches the filesystem, so a retry after a lost reply
  reuses its original reservation. Project IDs start at 10000 and are never reassigned.
- **Conservative accounting.** Total bench caps plus the shared caps and the reserve cannot exceed
  the filesystem, for bytes or inodes, and unfinished allocations still count.
- **Bounded run logs**: per-command output (`--max-log-bytes`), the total retained and reserved log
  bytes (`--max-logs-bytes`) and the number of logs (`--max-log-files`).

A repository can ask for a smaller bench in `.bench/config.toml` with `[quota] size = "4G"`. The
request is read from the immutable source commit at creation, and the root policy's `BenchBytes`
is both the maximum and the default. Changing the request later does not resize a bench: ordinary
acquisition refuses the mismatch, while checkpoint, release and repair stay available. See
[Repository settings](/reference/repository-settings/).

## Shared mirrors

Benches start from a shared bare mirror per repository. Ordinary `lease`, `run` and explicit
`setup` refresh it before granting ownership or launching task code:

- The refresh first checks the mirror's physical path, its bare-repository identity and its single
  registered origin, and that the checkout still shares that mirror and names its task branch.
- The fetch updates and prunes only `refs/remotes/origin/*`, atomically. Local branches, tags, HEAD,
  the index, dirty and untracked files and ignored output are preserved. It does not pull, merge,
  rebase or reset a task branch.
- **A failed or cancelled refresh grants no ordinary lease.** A missing or changed mirror is
  refused, not recloned. `bench lease-repair` stays available because it skips the refresh.
- The deadline is `--mirror-timeout`, two minutes by default.

Partial mirror clones are staged and published atomically; abandoned staging directories are kept
under their own reviewed collection (`bench mirror-clones`).

## Shared and checkout-local caches

`.bench/config.toml` chooses, per tool, whether a cache is shared across benches or kept inside the
checkout:

| Setting | Shared, under data | Checkout-local | Environment |
| --- | --- | --- | --- |
| `pnpm` | `store/pnpm` | `.bench/cache/pnpm` | `npm_config_store_dir`; clone imports |
| `npm` | `store/npm` | `.bench/cache/npm` | `npm_config_cache` |
| `bun` | `store/bun` | `.bench/cache/bun` | `BUN_INSTALL_CACHE_DIR` |
| `playwright` | `store/playwright` | `.bench/cache/playwright` | `PLAYWRIGHT_BROWSERS_PATH` |
| `go` | `store/go-build`, `store/go-mod` | `.bench/cache/go-build`, `.bench/cache/go-mod` | `GOCACHE`, `GOMODCACHE` |

All five default to shared. A repository cannot name an arbitrary cache path. Setup hooks and
supervised commands inherit these paths; a shell opened after a cooperative lease does not.
Checkout-local caches count against the bench's cap and should be ignored in `.gitignore`
(`.bench/cache/`), or their files will block checkpoint and release.

The shared package store is part of the managed XFS setup above and shares its limits. Collecting
old shared caches and mirrors is not built.
