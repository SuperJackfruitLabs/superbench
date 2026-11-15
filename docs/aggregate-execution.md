# Aggregate acquisition and execution

The internal store now supports ordinary multi-repository acquisition, member
preparation, supervised payload execution, whole-vector checkpoints and release.
The CLI and local service still expose v1; these methods are not a published v2
consumer API.

`AcquireWorkspaceContext` validates all registered worktrees and mirrors, checks
admission and the one pinned quota, and refreshes existing mirrors under the host
lock before committing ownership. Refresh changes remote-tracking refs only;
local task branches, dirty files and build output stay in place. A failed refresh
grants no lease. A busy request cannot retrieve or replace another holder's token,
including when its holder name matches.

The new owner has a fresh token and private continuation. Prior preparation proofs
are cleared, but input-bound once receipts and the last complete checkpoint are
preserved. Every member is then prepared under the same lease. Each hooks rerun
for the new acquisition; once hooks rerun only when their input fingerprint
changes. Partial preparation failure retains ownership, successful member proofs
and all sealed stage evidence for explicit retry.

`RunWorkspaceContext` accepts the held token and an optional registered member
name. An empty selector runs at the aggregate root, with fixed shared cache
settings. A member uses its own cache settings. Full current preparation and
owned successful sealed proof are checked before reserving the payload and again
at launch. Payload history is distinct from preparation proof. The supervised
runner fences descendants, closes capture and seals history before returning;
the stopped lease remains held on success or failure. Another payload can retain
a sealed prior outcome under the same continuation. Explicit stop or cancellation
revokes continuation and prevents another automatic stage.

Checkpoint and release remain explicit. Checkpoint journals the complete clean
HEAD vector before remote publication. Release independently checks the stopped
writer, exact clean vector, complete remote seal/pins and current task refs.
History release and lease removal commit together. A command may legitimately
change preparation inputs, so release does not require their old fingerprints to
match. The next payload reparses changed inputs before running.

Schema 16 distinguishes ordinary acquisition from the previous initialization
schema. `ready` requires a complete checkpoint; it can carry partial historical
preparation after a stopped repair is released. It does not itself prove current
preparation or availability: held ownership and pending checkpoints still block
acquisition. Initial readiness continues to require every member's preparation.
Known schema 15 state migrates without changing identity, sources, checkpoint or
ownership; active old supervisors must be stopped with their installed binary
before upgrade. Older binaries refuse schema 16.

This checkpoint does not provide fresh-workspace recovery, aggregate archive/reap,
external agent writer fencing, native supervised laptop preparation, v2 service
responses or the board pilot. Linux cgroup fencing does not provide filesystem
isolation between trusted callers sharing a user account.
