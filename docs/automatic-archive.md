# Repository archive policy

Repositories can opt into automatic read-only archival:

```toml
[archive]
when = "branch-merged"
```

Omission defaults to `manual`; explicit `manual` is also accepted. Empty values,
other modes, unknown keys and wrong types are rejected by the bounded setup
configuration reader. A policy change also changes the setup configuration digest.

## What proves completion

At a successful remote-confirmed checkpoint, SuperBench can record that the
checkpoint was **not yet included** in the remote's advertised default branch.
This observation is optional and bounded by the mirror timeout. It never turns
an otherwise valid repair checkpoint into a failure. A missing observation leaves
automatic archival unavailable. Repeating the same checkpoint preserves an
existing observation; publishing another checkpoint clears stale evidence.

The daemon later checks for all of the following under the bench state lock:

- The bench has no lease and has a checkpoint after its recorded creation source.
- The checkpoint's committed configuration opts into `branch-merged`, and its
  digest matches the physical configuration. Index flags cannot substitute an
  uncommitted policy for the committed one.
- The same checkpoint/configuration/default branch has an earlier unmerged
  observation. Starting a task at default history or fast-forwarding to newer
  default history does not establish completion.
- The registered checkout and mirror still have their expected identities, the
  checkout is clean and unlocked, and the remote task branch still names exactly
  the checkpoint.
- A fresh remote default-branch advertisement matches the fetched target commit,
  and Git proves the checkpoint is now its ancestor.

The archive's merge evidence, timestamp, grace deadline and exact freeze intent
commit together before permissions change. New acquisitions then refuse the
archive. Interrupted freezing resumes that durable intent without reinterpreting
completion or choosing a new deadline. The task branch must remain remotely
confirmed during freeze reconciliation and reviewed reap.

This proves an observed transition in Git history. It does not promise that a
remote ref will never be rewritten afterward. There is no remote ref lock.
Squash/rebase integration without checkpoint ancestry, deleted or moved task
branches, a changed default branch, unknown provenance and unavailable or
inconsistent remote evidence do not trigger automatic archival. Use explicit
manual archival after reconciling the task branch in those cases. SuperBench
does not treat patch similarity, branch deletion or a settled work run as proof.
Suite card completion remains a separate future adapter path.

## Operation

`benchd` inspects at most 100 registered benches per cycle and carries a
round-robin cursor across cycles. `--archive-interval` defaults to one minute
and accepts one second through 24 hours. This loop is independent of approved
log retention; a retention policy never grants authority to archive a bench.
Blocked candidates leave their data intact and emit a daemon diagnostic.

For explicit standalone inspection without the daemon:

```sh
bench migrate --state /absolute/state --data /absolute/data
bench archive-run --state /absolute/state --data /absolute/data --limit 100
# If Next is nonempty, continue with that exact key:
bench archive-run --state /absolute/state --data /absolute/data --limit 100 --after-key TASK_KEY
```

The local report contains per-key outcomes and a `Next` cursor. This command is
local administration; it adds no socket operation. `bench archive` remains the
explicit manual path regardless of repository mode.

Automatic archival keeps the normal seven-day grace. It does not delete files,
remove worktrees, collect logs, retire quota reservations or approve reap. The
existing exact reviewed deletion plan is still required after the grace period.
Read-only permissions coordinate trusted callers; they are not isolation from the
owner, root or already-open descriptors.

## Compatibility and evidence

State schema 12 records immutable creation provenance and the unmerged/merged
observations. Existing schema-11 registered benches retain unknown provenance
and stay manual; migration never fabricates task history. Pending creation
journals can retain their actual source. Recovery starts a new bench at its
recovered commit, so an unchanged already-merged recovery is not immediately
archived. Older binaries refuse schema 12.

Required checks passed on macOS and Linux. Real Git/Store integration and
corruption cases cover the trigger and its rejection paths. The actual Linux
periodic daemon/CLI drill passed, including genuine schema-11 migration,
old-binary refusal and SIGKILL/restart during a permission change across a large
tree. Schema-8 grace and schema-9 permission recovery regressions also passed.
See [complete runtime evidence](automatic-archive-output.txt) for commands,
artifact provenance and normalized output. This is producer verification; a live
suite pilot and production rollout remain separate.
