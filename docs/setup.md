# Repository setup hooks

`bench setup` reads `.bench/config.toml` in the registered checkout. This
checkpoint adds explicit setup; automatic preparation during `create`, `run` or
cooperative `lease` remains subsequent work. The existing socket `setup`
operation uses the same implementation and request replay protection.

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

The first successful setup runs `once`, then `each`. Later explicit setup calls
skip `once` only when the exact configuration bytes match a successful receipt
for that bench. `each` still runs. Changing any configuration bytes, including
comments, invalidates that match. Receipts are scoped to the bench and store
only a configuration digest, execution ID and completion time; they contain no
shell text or lease token. At most one current success is retained per bench,
with a 10,000-receipt admission cap.

Before executing a different configuration, the acquisition transaction revokes
the old receipt. A failed alternate preparation cannot leave the old receipt
valid if that old configuration is later restored. Frozen fallback setup also
revokes a prior custom-hook receipt. An acquisition rollback restores the old
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

State schema 7 adds the separate setup receipt table. Stop older supervised
executions with the installed binary before migration and back up state as part
of upgrade review. Schema-6 binaries refuse the new state. Migration preserves
existing history and pinned quota metadata; it does not rerun host allocation.

## Verification and remaining work

The required race suite checks TOML bounds, unknown/duplicate fields, linked and
special files, offline environment, separate subshells, durable once behavior,
configuration mutation, hook failure and checkpoint failure. Linux integration
uses actual cgroups and the Unix daemon to verify schema migration, persisted
once, alternate-config invalidation, held failure evidence and cancellation of
hook descendants. The extracted execution backend keeps its existing wire/state
shape and is exercised by the execution failure drill.

The parser is pinned to [go-toml v2.4.3](https://github.com/pelletier/go-toml/releases/tag/v2.4.3)
in the configuration adapter only. Automatic hooks on acquisition, cache/quota/
archive configuration, suite pilots and broader collection remain Phase 1 work.

Recorded results: [Linux setup and execution failure drills](setup-linux-output.txt)
and [frozen setup/storage regressions](setup-storage-output.txt).
