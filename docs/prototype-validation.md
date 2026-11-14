# Local CLI prototype validation — 2026-11-14

## Executed

- macOS arm64, Go 1.26.4: `scripts/check.sh` passed (gofmt, go vet,
  race-enabled tests, build).
- Linux x86_64 staging host, git 2.53.0: cross-compiled test binary passed all
  engine and architecture tests. This Linux run did not use the race detector.
  Sources and executable were transferred to a disposable directory, removed
  after the run. No service was installed or existing repository changed.
- Built macOS CLI exercised using a disposable local bare remote: create,
  lease, commit failing-test fixture, checkpoint, release, second-holder warm
  handoff, release, recovery into separate host state. Recovered content and
  commit matched the checkpoint. This demonstrates file handoff, not an actual
  invocation of two model harnesses.
- Planted a file over 1000 lines and confirmed the architecture guard failed;
  restored the file and reran vet. Go package size/import guards also passed.

## Regression coverage

- Twelve simultaneous in-process lease requests yield exactly one winner.
- Eight separate processes yield exactly one lease winner.
- Reopening the state store preserves the lease; no expiry/reassignment occurs.
- Old/wrong holder tokens are refused; status/checkpoint do not expose tokens.
- Checkpoint rejects dirty/untracked files; release requires a checkpoint.
- Rejected remote push retains the lease and blocks release/reacquisition.
- Fresh-host recovery uses the remote task branch; a missing branch fails.
- Invalid keys and overlapping state/data paths, including symlinks, fail.
- Malformed JSON state fails without overwriting the file.

## Not established

No remote CI has run. There is no hosted repository/PR, suite integration,
quota helper, package setup, archive command or benchd. JSON state is a provisional
substitute for SQLite. Direct filesystem writes and writers on another host are
not fenced. Automated stale-holder recovery and automatic crash reconciliation between git
and state need further design. The completion experiments below test actual CLI
state-write exhaustion and manual crash reconciliation.


## Spike completion checks — 2026-11-14

The staging scripts `scripts/spike-failures.sh` and `scripts/spike-failures.py`
ran against a Linux build of the CLI in a private mount namespace. They used a
512 MiB disposable XFS image, an 8 MiB package-store quota and a 16 MiB state
quota. All final assertions passed. No production state or existing repository
was touched; fixture branches were pushed only to a temporary local bare remote.

- An actual ENOSPC during checkpoint state-save, after a successful push, returned
  failure and preserved the previous state bytes and holder token. Freeing space
  and retrying checkpoint reconciled the remote SHA into local state.
- An actual ENOSPC during release state-save preserved the lease. Freeing space
  and retrying release succeeded.
- SIGKILL of the CLI process group after worktree creation left files intact but
  no registered bench. Retrying create refused to overwrite the orphan. With the
  old writer stopped, manually pushing the orphan branch and recovering into
  fresh state/data succeeded. Automatic orphan adoption is not implemented.
- SIGKILL after successful push left the old checkpoint and active lease in state.
  Another holder was refused. Checkpoint retry and release then succeeded.
- pnpm reported ERR_PNPM_ENOSPC when a 12 MiB fixture exceeded its 8 MiB store
  cap. A previously installed 4 MiB fixture retained its hash. Increasing only
  the disposable store quota allowed retry without manual store repair; the
  healthy fixture then installed offline with a frozen lockfile and matching hash.
- An exploratory fixture placed clients outside the store filesystem and failed
  with EXDEV under forced clone imports. The final passing run co-located them.

`spike-workload.sh` separately installs and builds a committed website snapshot
using its native package-lock.json and npm ci, then rebuilds in the warm checkout
and builds a second checkout using the same npm cache. It does not migrate the
real project to pnpm or establish pnpm savings on that real dependency graph.
Detailed timings and raw output are retained with the sketchbook spike record.

Reproduction on a disposable Linux host with root, XFS tools, pnpm, npm, Node,
Python, git and the cross-built CLI:

```sh
unshare --mount bash scripts/spike-failures.sh /absolute/path/to/bench-linux /absolute/path/to/scripts/spike-failures.py
git -C /path/to/website archive COMMIT | unshare --mount bash scripts/spike-workload.sh
```

The workload allocates a temporary 3 GiB image and checks at least 6 GiB of free
space first; installs/builds each have a 240-second timeout. The failure experiment
allocates 512 MiB and checks at least 2 GiB free first. Both remove their disposable
image/directories on exit. These checks use trusted source snapshots; arbitrary
repository lifecycle scripts are outside the prototype's isolation boundary.


Final representative-workload run: native npm install 13.324 s; first build 19.254 s;
warm dependency check 1.127 s; warm build 19.392 s; second offline install 10.670 s;
second build 20.280 s. Each build produced 11 pages. The second completed checkout
added 618,930,176 allocated filesystem bytes (about 590.3 MiB) over the warm-checkout
sample. Each finished checkout occupied about 602.1 MiB, including source. There is
no demonstrated build-speed improvement. This is one measured source snapshot/run,
not a performance guarantee or pnpm benchmark of the actual website dependency graph.

Disposition: feasibility spike complete; proceed to a constrained implementation.
Production blockers remain process fencing, filesystem-operation journaling, quota
and admission enforcement, transactional service storage, and shared-contract review.
The corrected proposal and full evidence are in sketchbook's SuperBench spike record.


## Phase 1 follow-up: journaled creation

This supersedes the spike's manual-orphan limitation for journaled, clean
worktree creation only. Intents are fsynced outside data before worktree mutation.
Tests reconstruct the interruption boundary and confirm exact clean adoption,
plus refusal/preservation of dirty files, changed HEAD, missing journal and
conflicting repository/base arguments. The Linux SIGKILL experiment is rerun
against this implementation; its assertion now requires automatic registration of
the same checkout on retry. Historical spike outputs remain unchanged.

Result: formatting, vet, architecture guards, build and macOS race tests passed.
The Linux failure/SIGKILL experiment also passed, including automatic registration
of the original journaled checkout after interruption. Its raw output is in
[phase1-failure-output.txt](phase1-failure-output.txt). Temporary staging data was
removed by the experiment's exit trap. No service was deployed.


## SQLite checkpoint

State schema v1 uses pinned modernc.org/sqlite through database/sql, with FULL
synchronous commits and the existing host-wide lock for filesystem/git mutation.
Legacy JSON imports on the next successful mutating operation, is retained intact,
and cannot overwrite SQL records after migration. Status opens SQL read-only and
does not create absent state directories/files. Future schema versions fail closed.

Formatting, vet, architecture checks, build and race tests passed locally. Added
checks cover preserved lease tokens on legacy import, rollback, malformed legacy
migration, future-schema refusal and read-only status. A CGO-disabled Linux binary
passed the quota/SIGKILL experiment; [sqlite-failure-output.txt](sqlite-failure-output.txt)
contains its output. A fixture post-receive hook exhausts state only after the
remote accepts the push, precisely testing push-success/state-save-failure.
Already-full state can instead prevent SQLite opening; that also fails closed.
Neither failure releases the holder. All disposable staging data was removed.

Creation journal records remain separate durable intents. SQLite does not yet
provide writer fencing, supervised execution, quotas or automatic reconciliation
for partial mirror clones. Those remain Phase 1 implementation work.
