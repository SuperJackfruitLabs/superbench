---
title: Repository settings
description: The .bench/config.toml file a repository commits to configure setup hooks, caches, a quota request and automatic archive.
status: wet paint
---

A repository configures its benches with `.bench/config.toml`, committed in the repository. It is
read inside the acquisition transaction, and the same parsed file drives the command plan and the
cache environment. This page is checked against `internal/setup` when the docs build.

```toml
[setup]
once = "pnpm install --frozen-lockfile"
each = "pnpm install --offline --frozen-lockfile"

[caches]
pnpm = true
npm = true
bun = true
playwright = false
go = false

[quota]
size = "4G"

[archive]
when = "branch-merged"
```

The file must be UTF-8, at most 64 KiB, a regular, unlinked `config.toml` in a physical `.bench`
directory. A pinned TOML parser rejects duplicate keys, wrong types and unknown fields, and its
errors never echo shell text. Changing any byte, comments included, changes the configuration
digest and invalidates `once` reuse.

## `[setup]`

| Key | Meaning |
| --- | --- |
| `once` | Runs on the first successful preparation of a bench, then only when the configuration changes. |
| `each` | Runs before every handoff. |

Each command is trusted shell code, at most 16 KiB with no NUL, run as the account running `bench`
or `benchd`, with `/bin/sh -e` in its own subshell at the checkout. With no hooks, setup does a frozen install from
exactly one pnpm or npm lockfile. See [Supervised runs and setup](/concepts/supervised-runs/).

## `[caches]`

| Key | Default | `true` (shared, under data) | `false` (checkout-local) |
| --- | --- | --- | --- |
| `pnpm` | `true` | `store/pnpm` | `.bench/cache/pnpm` |
| `npm` | `true` | `store/npm` | `.bench/cache/npm` |
| `bun` | `true` | `store/bun` | `.bench/cache/bun` |
| `playwright` | `true` | `store/playwright` | `.bench/cache/playwright` |
| `go` | `true` | `store/go-build`, `store/go-mod` | `.bench/cache/go-build`, `.bench/cache/go-mod` |

Only booleans are accepted; a repository cannot name a cache path. Ignore `.bench/cache/` in
`.gitignore` when any cache is checkout-local. SuperBench never edits `.gitignore` for you.

## `[quota]`

| Key | Meaning |
| --- | --- |
| `size` | A smaller byte cap for new benches: a positive whole number of KiB, up to 1 PiB. `K`, `M`, `G`, `T`, `P` and `KiB` to `PiB` are binary; `B` or no unit means bytes, divisible by 1024. |

The request is read from the source commit at creation and enforced only on managed Linux XFS
hosts, within the root policy's ceiling. Elsewhere it is recorded and not enforced. It never resizes
an existing bench. See [Quotas, mirrors and stores](/concepts/quotas-and-stores/).

## `[archive]`

| Key | Meaning |
| --- | --- |
| `when` | `manual` (the default) or `branch-merged`: archive read-only automatically once a checkpoint recorded as unmerged is seen merged into the remote's default branch. |

Automatic archive never deletes anything; deletion still needs a reviewed reap plan after the grace
period. See [Archive and reap](/concepts/archive-and-reap/).
