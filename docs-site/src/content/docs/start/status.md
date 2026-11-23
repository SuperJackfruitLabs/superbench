---
title: Status
description: What SuperBench v0.0.2, an experimental single-host release, does, what is partial and what is not built, from the repository's changelog and implementation plan.
status: wet paint
---

SuperBench v0.0.2, tagged on 2026-11-23, is the second release, after v0.0.1. It is an
experimental single-host release, not production software, and the changelog calls it a
prototype: nothing in it has been proven beyond what the repository's docs describe. The
[GitHub release](https://github.com/SuperJackfruitLabs/superbench/releases/tag/v0.0.2) carries one
archive for Linux amd64 holding `bench`, `benchd`, `bench-quota` and `bench-payload`, with
`SHA256SUMS` and a build-provenance attestation; [Install v0.0.2](/start/install/) shows how to
verify and stage it. There is no build for macOS or arm64, no package, no promise of
compatibility between releases and no support commitment.

Over v0.0.1, v0.0.2 adds two things. A checkpoint or release refused for a dirty tree now names up
to five of the paths to clean, relative to the workspace root, with a count of the rest; the
`409 conflict` code and status are unchanged. And the docs explain
[repositories chosen per card](/concepts/benches-and-workspaces/#repositories-chosen-per-card):
SuperBench creates a workspace with the members it is given and never changes an existing
workspace to another set. Neither changes a limit on this page.

This page follows the phase table in the repository's
[`docs/implementation.md`](https://github.com/SuperJackfruitLabs/superbench/blob/main/docs/implementation.md),
which is the source of truth; when the two disagree, that file wins and this page is out of date.

"Verified" below means what the repository records: tests in its check suite, or a dated drill
whose output is committed under `docs/`. Several drills ran on disposable Linux hosts, not on a
host anyone relies on. Passing single-host drills are not evidence that a whole phase is done.

## The phases

| Phase | Outcome it requires | Where it stands |
| --- | --- | --- |
| 0 | Host disk alert, a reviewed cleanup dry-run, branch and checkpoint rules for code stages | Partial. A host collector and capacity warning were checked; notification delivery and recovery, real handoff enforcement and an operator-reviewed cleanup remain unverified. |
| 1 | Standalone CLI and host service; durable state and leases; checkpoints and recovery; quotas, shared mirrors and stores; verified archive and reap | In progress. Most of the single-host core is built and tested (below). The acceptance still to prove is listed under [Phase 1 acceptance still to prove](#phase-1-acceptance-still-to-prove). |
| 2 | Suite adapters, a card panel, locality scoring, witness events; a read-only web view, terminal attach and MCP | Not built. Contracts come before consumers, and visual mockups before UI. Static mockups exist; no viewer ships. |
| 3 | Remote editor launch or browser IDE; a second-host recovery drill; per-bench isolation (none yet); Salaam layout and execution-host integration | Not built. |

## Works in v0.0.2, with tests or recorded drills

- **Create, lease, checkpoint and release a bench**, with cooperative leases and remote-confirmed
  checkpoints. See [Leases](/concepts/leases/) and
  [Checkpoints](/concepts/checkpoints/).
- **Durable state**: versioned SQLite outside bench data, with creation intents journaled before a
  worktree is made, so an interrupted create can be retried with the same arguments.
- **Supervised runs on Linux**, fenced in a delegated cgroup-v2 scope before checkpoint and
  release. See [Supervised runs and setup](/concepts/supervised-runs/).
- **Managed storage on Linux XFS**: per-bench and shared byte and inode caps through a fixed-policy
  helper, free-space admission, shared mirrors refreshed at acquisition, shared or checkout-local
  caches and bounded run logs. See [Quotas, mirrors and stores](/concepts/quotas-and-stores/).
- **Repository setup hooks** (`.bench/config.toml`), run before work and at creation.
- **The local daemon**, `benchd`, with kernel peer-credential authorization, durable request
  receipts and restart reconciliation. See [The local API](/api/overview/).
- **Grouped workspaces** (several repositories in one bench) through the v2 API and
  `bench workspace`, including complete-vector checkpoints and distinct-ref recovery.
- **Archive and reviewed reap**, with a persisted grace period and an exact approved manifest. See
  [Archive and reap](/concepts/archive-and-reap/).
- **Reviewed log and history collection**, and an automatic log retention policy that stays off
  until an operator approves it.
- **A staged Linux service installer**, checked with a disposable systemd unit. v0.0.2 publishes
  the four executables it installs. It is not a supported installation: use it on a disposable or
  staging host.

## Partial

- **Recovery.** Recovering a bench from its remote-confirmed task branch into a separate, empty
  state and data pair works. Recovery into existing state, missing-checkout and cross-host
  original-ref recovery are not complete, and a second-host recovery drill is Phase 3 work.
- **Payload identity.** The separate payload user and its fixed helper exist in source, but `main`
  does not install and activate the complete separate payload-user path. See
  [Payload identity and the fixed helpers](/concepts/payload-identity/).
- **Fresh allocations.** Fresh payload materialization stays disabled. Inspection and retirement of
  an absent Fresh allocation exist as direct-root maintenance; cleanup of nonempty allocations does
  not. See [Fresh allocations and recovery](/concepts/fresh-allocations/).
- **Laptop preparation.** Supervised setup needs Linux. A cooperative path for macOS and Linux
  exists, but it does not fence processes.
- **Moving a bench between hosts.** A reviewed administrative handoff exists. It is unsigned, adds
  no network coordination, and needs both operators to verify each other independently.
- **Contracts.** The v2 grouped request and response shapes are proposed contract data, not
  accepted contracts with consumers.

## Phase 1 acceptance still to prove

The repository's implementation plan lists these before Phase 1 can be called complete. Passing
single-host drills alone is not evidence that they are done.

- The standalone laptop preparation path (supervised hooks currently require Linux).
- The remaining complete multi-repository CLI and service lifecycle.
- Missing-checkout and cross-host original task-ref recovery.
- The automatic completion and archive lifecycle, and broader collection.
- The acquisition, prompt and checkpoint pilot on an opt-in board, after the contract boundaries
  are reviewed.

## Not built

- A supported installation: packages, builds for other platforms, an upgrade promise or support.
- Automatic lease expiry. By design, time alone never ends a lease.
- Remote authentication, a web interface and suite integrations.
- No payload isolation under a separate user yet, enforced end to end.
- More than one host working together.
- Collection of shared caches, mirrors and request receipts.
- The suite pilot on an opt-in board.
