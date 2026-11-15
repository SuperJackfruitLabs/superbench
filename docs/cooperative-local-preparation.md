# Cooperative preparation on a laptop

The standalone CLI can run repository preparation on macOS and Linux under an
explicit cooperative lease. This mode coordinates trusted operators. It does
not fence processes, establish descendant quiescence, isolate another process
with the same UID, or provide managed execution evidence. The supervised Linux
`run`, `setup` and initialized creation APIs keep their existing requirements.

Use the current CLI for every operation on this state directory. Older binaries
do not know the local preparation journal and cannot enforce its handoff guard.
A process writing directly to the checkout is also outside this cooperative
protocol. A socket timeout, successful command leader or elapsed time never
means its descendants stopped.

## Local workflow

Create a cold registration and acquire the local administrative cooperative
lease. These commands do not bypass setup on the service API:

```sh
bench create-cold --repo /absolute/path/to/remote.git --branch card/example example
bench lease-repair --holder first-writer example
```

Keep the returned token private, preferably in `BENCH_LEASE_TOKEN`. Run
`bench prepare-local example`. Preparation uses the existing strict repository
`.bench/config.toml`, once/each commands, frozen dependency commands, and fixed
shared or checkout-local cache settings. Offline setup is selected with
`--offline`. Checkout-local caches must remain untracked. Commands receive the
existing cache environment without the lease-token environment variable.

The command retains ownership and prints a versioned `cooperative` observation
with an attempt digest. Output goes to a bounded private local preparation log. Acknowledged attempt
metadata is retained beside its log when a later attempt replaces the active
journal.
Neither exit code zero nor that observation authorizes automatic release. Review
the result and stop every cooperative writer, including detached children. Only
then acknowledge that exact digest:

```sh
bench acknowledge-local-preparation --digest REVIEWED_DIGEST --acknowledge-stopped-writers example
bench checkpoint example
bench release example
```

The acknowledgement is an operator assertion, not a measured fence. Checkpoint
still requires committed, clean work and confirms the remote task branch;
ignored build artifacts stay local. The next writer acquires a fresh token with
`lease-repair`, then runs `prepare-local` again. An acknowledged successful once
hook with identical inputs is reused; each hooks run again. No supervised setup
receipt, history execution, private continuation or managed-ready grant is
created by this workflow.

`local-preparation-status` reads the current owner's local observation. All three
new verbs are local only and reject socket flags, including an explicit empty
socket. State/data overrides and flags precede the key as in the other CLI verbs.

## Interrupted or changed work

Preparation durably records its intent before cache or command effects. It holds
the per-key execution lock while the original command leader is active. Current
CLI and daemon checkpoint/release paths reject an unresolved journal; acquisition
and archive also retain the existing held lease. Another operator cannot
acknowledge the attempt using a different token.

Cancellation kills only the known command leader and bounds pipe waiting. It
cannot prove detached children stopped. Controller death, cancellation, failed
capture and changed inputs keep the lease and preserve unknown evidence. There
is no automatic redispatch after restart. Once all writers are independently
stopped, an operator may acknowledge an unknown attempt to reconcile ownership;
this never turns it into successful preparation. An unknown once hook additionally
requires an explicit `--rerun-once` approval before it can run again. Inspect its
possible partial effects first; do not assume rerunning is safe.

Malformed, future-version or mismatched journals refuse handoff. Do not delete a
journal to clear ownership. Local preparation logs are separate from supervised
history and are never automatically collected: at most 1,000 retained log/observation files and 64 MiB of
reserved/retained evidence admit new preparation. Exhaustion requires separate
review and preservation of that evidence. Acknowledgement remains available
without allocating another log. This feature does not claim managed Darwin
execution, suite pilot acceptance, or cross-host fencing.
