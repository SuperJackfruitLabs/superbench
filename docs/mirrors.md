# Mirror refresh before acquisition

Ordinary `lease`, `run` and explicit `setup` refresh their registered shared bare
mirror before granting ownership or launching task code. An automatic setup/main
handoff keeps its existing lease and does not fetch a second time. The fresh
creation/recovery path still publishes a complete staged clone; retries of an
existing creation intent retain its original pinned source.

Refresh first verifies the physical mirror path, its bare-repository identity
and its single registered origin URL. Acquisition also checks that the checkout
is a physical directory, shares that mirror's Git common directory and still
names its registered task branch. A missing mirror is refused, not recreated
under an existing worktree. Changed, detached, linked or replaced checkouts stay
untouched for operator reconciliation.

## Ref updates and preservation

The fetch explicitly updates/prunes only `refs/remotes/origin/*`, with an atomic
ref transaction and an empty configured ref mapping. It overrides tag pruning,
does not follow tags, recurse into submodules or launch automatic maintenance.
Local branches, local tags, HEAD, the index, dirty/untracked files and ignored
install/build output are preserved. Custom `remote.origin.fetch` mappings cannot
redirect the refresh into local task branches. This follows Git's documented
[explicit ref mapping and atomic fetch options](https://git-scm.com/docs/git-fetch).

Refresh exposes newer remote commits; it does not pull, merge, rebase or reset a
task branch. If another writer advanced the remote task branch, reconcile it
explicitly while holding ownership. Existing clean-work, non-forced push and
remote-confirmation checks still govern checkpoint/release.

Managed admission verifies shared store/mirror budgets and the bench allocation
before refresh. New Git objects remain charged to the shared mirror project.
Disabling automatic maintenance does not implement mirror garbage collection;
reviewed collection of unused mirror/store data remains separate work.

## Deadlines and failure

```sh
bench lease --state /absolute/state --data /absolute/data \
  --mirror-timeout 2m --holder next-run task
benchd --state /absolute/state --data /absolute/data --mirror-timeout 2m
```

The default mirror budget is two minutes, covering validation plus clone/fetch
as applicable. Zero selects that default; positive values up to one hour are
accepted. `BENCHD_MIRROR_TIMEOUT` configures the Linux wrapper. Socket callers
use the daemon setting; a local CLI flag cannot override it through a request.
The state-lock wait is outside this mirror budget. Other lifecycle Git operations
retain their existing timeout limitations.

Cancellation interrupts the Git invocation. A two-second output-pipe wait bound
handles a command that exits while inherited descriptors remain open, using
Go's [command context and WaitDelay](https://pkg.go.dev/os/exec#Cmd). Mirror
stdout is bounded to 64 KiB; stderr is drained into a bounded diagnostic buffer
and never echoed in the returned error. Mirror subprocesses exclude ambient Git
repository/command overrides and `BENCH_LEASE_TOKEN`, while retaining ordinary
authentication configuration.

Failed, canceled or expired refresh refuses ordinary acquisition. It creates no
task history or lease and does not launch the requested command. Already held
leases are never expired by this deadline. A mirror timeout is not proof that a
task writer stopped; supervised task fencing remains the separate cgroup rule.
As before, an interrupted socket request is not automatically dispatched again.

`--offline` on explicit setup controls dependency setup, not Git connectivity.
During a remote outage, the local operator can use `lease-repair` to inspect or
repair an existing checkout without refresh or hooks. That cooperative exception
does not replace a held lease and is absent from the socket API. Restoring normal
acquisition requires fixing the mirror/checkout or connectivity; repair never
silently reclones, resets or discards work. Checkpoint/release still requires a
confirmed remote branch.

## Verification

The required race suite uses actual bare repositories and worktrees. It verifies
remote branch advancement/deletion, custom ref mappings and tag-pruning settings,
unpublished commits, dirty/untracked/ignored files, changed checkout identities,
missing/linked mirrors, origin drift, bounded diagnostics, cancellation and
deadlines. Public acquisition tests verify refusal without ownership and the
network-independent repair path on both local test platforms.

The disposable Linux drill advances the remote separately before CLI setup,
daemon setup, `run` and cooperative acquisition. It also tests a failed remote,
canceled/expired refresh and a marker proving task code never launched. The
unprivileged systemd and managed XFS drills verify the updated wrapper, staged
clone publication and existing quotas/frozen setup behavior. No production
service or policy is installed by these tests.

Recorded results: [Linux acquisition, service and storage verification](mirrors-linux-output.txt).
