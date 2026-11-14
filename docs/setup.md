# Repository setup hooks

`bench setup` reads `.bench/config.toml` in the registered checkout. Configured
hooks also run automatically before `run` and before a cooperative `lease` is
returned. `create` registers the checkout; warming during creation remains
subsequent work. Existing socket operations use the same implementation and
request replay protection, without a new request field or operation.

```toml
[setup]
once = "pnpm install --frozen-lockfile"
each = "pnpm install --offline --frozen-lockfile"
```

```sh
bench setup --state /absolute/state --data /absolute/data \
  --cgroup-root /delegated/cgroup --holder preparation task
```

The commands are trusted repository shell code, executed as the service user
with `/bin/sh -e`. Each command runs in a separate subshell rooted at the
checkout. An `exit` or `cd` in `once` cannot skip or change the working directory
of `each`. The usual shared cache environment and output budgets apply. Only
Linux cgroup-v2 hosts support fenced setup; cooperative lease/checkpoint commands
remain available elsewhere.

Configuration is selected inside the lease-acquisition transaction. It must be
UTF-8, at most 64 KiB, in a physical `.bench` directory with a regular, nonlinked
`config.toml`; each command is bounded to 16 KiB without NUL. A pinned TOML parser
rejects duplicate keys, invalid types and unknown fields. Parser errors do not
echo shell text. The proposed `[caches]`, `[quota]` and `[archive]` settings are
not implemented here and are rejected rather than silently ignored.

With no configured hooks, setup retains its frozen-lockfile behavior: exactly
one supported pnpm/npm lockfile, clone imports for pnpm, and the host's shared
store/cache paths. Missing or ambiguous lockfiles refuse setup. Custom commands
own their package-manager flags. For custom hooks, `--offline` exports
`BENCH_SETUP_OFFLINE=1` (otherwise `0`); it does not rewrite shell text or claim
network isolation.

## Successful preparation and recovery

The first successful preparation runs `once`, then `each`. Later acquisitions or explicit setup calls
skip `once` only when the exact configuration bytes match a successful receipt
for that bench. `each` still runs before handoff. Changing any configuration bytes, including
comments, invalidates that match. Receipts are scoped to the bench and store
only a configuration digest, execution ID and completion time; they contain no
shell text or lease token. At most one current success is retained per bench,
with a 10,000-receipt admission cap.

Before executing a different configuration, the acquisition transaction revokes
the old receipt. A failed alternate preparation cannot leave the old receipt
valid if that old configuration is later restored. Frozen fallback setup also
revokes a prior custom-hook receipt. Hook-free ordinary run/lease acquisition
also revokes it, so restoring an older configuration cannot reuse stale setup.
An acquisition rollback restores the old
receipt because no new hook was launched.

Success is recorded only after the entire setup exits zero, its scope is fenced,
and the output log is sealed. The configuration must still match. The success
receipt commits while the stopped lease is held, before checkpoint/release. A
checkpoint failure therefore preserves known preparation success and keeps the
lease for inspection. Setup never commits unknown files or removes dirty output
to make a checkpoint pass.

Hook failure, cancellation, changed configuration or a receipt-save failure
leaves the lease held. Inspect history/output and stop/reconcile/checkpoint/
release explicitly. A failed `each` on a bench without a matching success means
the next attempt can run `once` again. A crash before the success receipt commits
can also require rerunning it. Hooks must be idempotent; external shell effects
are not an exactly-once transaction. Unregistered/held log protection and service
request receipts retain their existing rules.

State schema 7 added setup receipts; schema 8 protects the preparation chain and
retained execution IDs. Stop older supervised executions with the installed
binary before migration and back up state as part of upgrade review. Older
binaries refuse the new state. Migration preserves existing history and pinned
quota metadata; it does not rerun host allocation.

## Automatic preparation and handoff

With configured hooks, `run` first acquires ownership and performs setup in a
separate bounded, fenced execution. After successful preparation, the same token
moves directly to the requested command. There is no release/reacquire gap. The
configuration is checked again before command launch. Setup success persists if
the requested command later fails; a subsequent acquisition can skip `once` but
must run `each` again.

Preparation and command have separate history/log IDs. The command result names
the command's execution; `bench history` also shows preparation. Successful setup
logs remain attached to the held lease and cannot be collected while it is held,
including after main-command failure or a cooperative handoff. Final release
marks every retained execution released in the same transaction that clears the
lease. Closed setup captures no longer reserve unused output capacity, while
existing bytes still count toward the total log budget. Each stage requires its
own log admission; exhaustion during handoff retains the stopped lease.

A private durable continuation authorizes only the exact pending handoff. Both
handoff and stop use the execution gate. A successful public stop revokes that
continuation, including a reserved lease whose setup has not launched, so a late
supervisor cannot start another stage afterward. It also records the stop so a
late normal completion cannot automatically release the held lease; explicit
operator checkpoint/release remains available. Status redacts the continuation;
the local socket DTO never exposes it. Cancellation and service shutdown use the
same stop/fencing rules. A daemon crash retains ownership and its request receipt;
restarting never redispatches the interrupted request automatically.

`lease` prepares with cgroup fencing before handing back a cooperative lease.
The returned lease has no active supervised execution. Commands the caller then
starts outside SuperBench are still cooperative and must be stopped by that
caller before release. Preparation does not confer process isolation on them.
On hosts without Linux fencing, configured automatic preparation refuses; plain
cooperative acquisition remains available for repositories without hooks.
Ordinary acquisition first refreshes its [registered mirror](mirrors.md).
`--offline` selects dependency setup behavior; mirror connectivity is still
required. The local repair lease is the explicit outage/maintenance path.
Repositories without hooks do not automatically install dependencies during
`run` or `lease`; use explicit `setup` for frozen-lockfile installation.

To repair broken configuration without executing its hooks, the local operator
can explicitly acquire a maintenance lease:

```sh
bench lease-repair --state /absolute/state --data /absolute/data \
  --holder configuration-repair task
```

Use the returned token while editing/committing, then checkpoint and release.
This bypass exists only in the private local administrative CLI, not the socket
API. It remains cooperative and cannot replace an existing holder. It neither
releases a failed setup nor silently resets its receipt. Ordinary `lease` always
honors configured preparation. Explicit `setup --offline` remains available.

## Verification and remaining work

The required race suite checks TOML bounds, unknown/duplicate fields, linked and
special files, offline environment, separate subshells, durable once behavior,
configuration mutation, hook failure and checkpoint failure. Linux integration
uses actual cgroups and the Unix daemon to verify schema migration, persisted
once, alternate-config invalidation, held failure evidence and cancellation of
hook descendants. The disposable systemd drill also verifies preparation and
once reuse under the unprivileged service uid. The extracted execution backend keeps its existing wire/state
shape and is exercised by the execution failure drill.

The parser is pinned to [go-toml v2.4.3](https://github.com/pelletier/go-toml/releases/tag/v2.4.3)
in the configuration adapter only. [Creation warm-up](creation.md) is verified
on Linux. Laptop preparation, cache/quota/archive configuration, suite pilots and
broader collection remain Phase 1 work.

Recorded results: [Linux setup and execution failure drills](setup-linux-output.txt)
and [frozen setup/storage regressions](setup-storage-output.txt).

Automatic preparation results: [Linux setup, handoff, stop and crash checks](preparation-linux-output.txt).
