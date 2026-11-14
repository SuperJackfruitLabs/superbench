# Multi-repository checkpoint foundation

`internal/checkpointset` implements the portable manifest and remote publication
foundation for multi-repository benches. It is not wired into create, lease,
run, release, archive or the local service API. Those operations still operate
on single-repository benches. The complete group lifecycle remains Phase 1 work.

## Authority and selected contract

The accepted charter gives bench lifetime, checkpoint and recovery to
SuperBench. The proposed generation extension is pinned from treaty commit
`250ca4f7d4380cfe236abe577bb4e608fa6fdf46`, published in
[treaty PR #32](https://forge.superjackfruit.com/SuperJackfruitLabs/treaty/pulls/32).
It adds no identity, issuer, network authorization or suite adapter.

The source manifest under `internal/checkpointset/testdata/` records canonical
paths and SHA256 hashes. `scripts/sync-checkpoint-contract.py` reads committed
Git objects, never uncommitted treaty files. Go's required tests consume every
shared manifest body by default and check the pinned bytes. Candidate paths can
be selected explicitly for development without changing the pin. The socket v1
schema and its previously pinned corpus remain unchanged.

The strict decoder accepts at most 256 KiB, rejects duplicate/unknown/case-shifted
keys, nulls, trailing documents and invalid Unicode, and validates the complete
sorted member set. Repository identities are an explicit allowlist input, never
credentials or a grant to discover new remotes. The external manifest digest is
SHA256 of exact raw bytes; it is not recomputed from reserialized JSON.

## Publication

The internal coordinator requires a lifecycle gate. A nil gate refuses work.
The eventual bench integration must hold its bench/mirror locks, verify the
single lease and stopped writers, and bind every target commit to its clean
checkout. The gate is checked again during publication. The coordinator's own
per-bench lock serializes its journal; that lock does not independently prove
writer fencing or replace the bench lease.

The caller must place coordinator state outside bounded checkout data. The
coordinator validates that its state directory is private, absolute and physical;
it has no data-root parameter and cannot itself establish filesystem separation.
Its explicit mirror registry must already authorize each repository. Both fetch
and push identities are checked against that registry.

Before any remote mutation, the coordinator fsyncs an exact generation journal
containing the raw vector, digest and fixed preparation timestamp. Reusing its
identity with a different vector refuses. A retry retains its original data.
Existing pin conflicts and incompatible predecessor layouts refuse before task
publication. New generations retain repository membership, branch identities,
object formats and the checkpoint-repository choice.

Each task branch is pushed without force and confirmed. Its generation pin is a
lightweight tag:

```text
refs/tags/superbench/pins/<bench_id>/<generation_id>/<member>
```

Normal tag pushes refuse replacement, even if the other target is an ancestor.
The coordinator never overwrites or deletes pins. Control pushes do not execute
local pre-push hooks: those could launch new unfenced writers after the lifecycle
gate. Intended validation must happen as fenced bench work before checkpointing.
Automatic following of unrelated annotated tags is disabled. Server receive
hooks remain active, and a server rejection keeps the generation incomplete.

After the whole task/pin vector is confirmed, Git plumbing builds a commit whose
tree contains only generated `checkpoint.json`. It never stages or commits user
checkout files. The predecessor and journal timestamp make this candidate
deterministic across process death and lost replies. The final metadata branch is:

```text
refs/heads/superbench/checkpoints/<bench_id>
```

Only a normal fast-forward push and exact remote confirmation return a completed
seal. Earlier participant successes remain intact after any failure. This is a
completion boundary across remotes, not an atomic cross-repository Git push.
The caller retains ownership on failure; the coordinator cannot clear its lease.

## Recovery

The internal recovery method takes an authorized repository identity, expected
bench ID, exact metadata commit and raw manifest digest. It fetches the known
metadata branch first, so historical recovery does not depend on a server
accepting arbitrary unadvertised object requests. It checks ancestry, the exact
single-file tree, metadata parent, coordinator identity and manifest digest.

Every participant must already be registered. Recovery confirms and fetches each
exact tag pin, checks its object format and commit, and confirms it again. Local
objects cannot conceal a missing or changed remote pin. A partial newer task
push does not alter the older sealed vector selected for recovery.

This returns validated source selection. Materializing an aggregate bench and
reassigning an old writer remain separate lifecycle work. Recovery of an older
seal may need fresh task branches and a new bench identity; original branches
are never reset or force-pushed backward. The format does not promise that a
remote administrator cannot rewrite or remove refs.

## Verification and remaining integration

The shared corpus contains 15 valid and 75 invalid manifest bodies. The treaty
test suite also checks its schema address and corpus envelope. Go adds lexical
decoder and raw-digest cases. Real Git tests exercise task/pin/seal rejection,
same-generation retry, stable participant layout, explicit remote authorization,
missing pins and historical recovery after a partial newer generation.

Required macOS and Linux checks passed, including the mandatory corpus, race
tests, installer checks, vet and binary builds. The complete Linux execution
and contract-build evidence is in
[checkpoint-generations-output.txt](checkpoint-generations-output.txt).

The process drill kills an owned test process group after the second task push
has paused, then uses fresh processes to resume the original journal and replay
the same seal. Its fixture gate is deliberately synthetic; it proves durable
publication/recovery, not integrated production writer fencing.

Before multi-repository benches are usable, implement the versioned group
operations and fixtures, aggregate creation/recovery/setup, one bench-wide lease,
checkpoint-intent integration, release/archive/reap across every member, total
quota selection and end-to-end failure drills. Suite consumers follow the
dual-version producer. This checkpoint does not complete Phase 1.
