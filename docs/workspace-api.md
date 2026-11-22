# Grouped workspace API

The v2 producer and CLI are implemented and locally verified. They do not enable
suite consumers or claim that the Developer Experience board pilot has run. The
accepted host-local boundary remains in Charter; the grouped request and response
extensions are proposed treaty data. Existing scalar v1 callers remain supported.

The response schema, client/producer corpus and runtime obligation list are pinned
to treaty `83e51d583a71f668eec8c2093394613e002c1678`, extending the original
response proposal merged through [treaty #34](https://forge.superjackfruit.com/SuperJackfruitLabs/treaty/pulls/34)
with the reclaim vectors and phases of [treaty #49](https://forge.superjackfruit.com/SuperJackfruitLabs/treaty/pulls/49).
`scripts/sync-workspace-responses.py` reads committed Git objects; tests verify
the exact revision and five file hashes, and run the reclaim corpus. Request and scalar-v1 pins remain
independent. Merging a proposed contract does not establish consumer acceptance.

An outgoing original-ref transfer reports optional `transfer_state` as
`relinquishing` or `transferred`. Its initialization `phase` can remain `ready`,
but `ready_for_acquisition` is false and no source grant is allowed. Transferred
archive/reap inventory retains the same availability marker. Ordinary responses
omit it, preserving their existing bytes. Updated v2 clients accept both shapes;
older strict clients reject the added field safely. This proposed extension adds
no operation or network authority and does not enable a suite consumer.

## CLI

Grouped operations use `bench workspace OP`; existing top-level commands retain
their scalar behavior. Flags precede the workspace key. `--socket` selects v2 on
the daemon; omitting it uses local administrative state/data. API operations print
a validated response envelope, including an authorized retained grant on failure.
Growth request IDs are printed to stderr; use `--request-id` to preserve one
across an interrupted socket reply. The client never automatically retries or
falls back to v1.

For example, `members.json` contains the canonical repository array:

```json
[
  {"name":"app","repository":"/absolute/path/to/app.git"},
  {"name":"library","repository":"/absolute/path/to/library.git"}
]
```

```sh
bench workspace create --socket /run/superbench/api.sock \
  --repositories members.json --checkpoint-repository app \
  --request-id create-task-1 task-1
bench workspace run --socket /run/superbench/api.sock \
  --holder run-1 --repository-name app --request-id run-task-1 \
  task-1 -- go test ./...
bench workspace status --socket /run/superbench/api.sock task-1
```

Creation/recovery require warm initialization. Recovery takes `--source FILE`
with the exact repository, bench ID, seal commit and manifest digest, plus an
explicit repository allowlist. `--repository-name` selects a run/setup member;
omitted run uses the root and omitted setup prepares every member.
`BENCH_LEASE_TOKEN` supplies a held owner capability for subsequent commands.

`plan-reap` and `reap --plan FILE --approve DIGEST` are local administration only.
They print a concrete Plan and redacted workspace inventory respectively.
Review the complete plan and nonrecoverable bytes before approval. A local
`archive --archive-grace DURATION` can explicitly choose the initial retention
deadline; zero is an explicit waiver. It cannot shorten a previously persisted
deadline, and the socket does not accept a grace override.

Local direct execution does not provide the daemon's exact HTTP receipt replay
for lease/run/setup. Creation identity remains deterministic for the supplied request
ID; a later owner's inventory cannot become a new creation grant. Use the daemon
when durable response replay across client disconnects is required. Standalone
laptop preparation still needs its own supported execution backend.

## Transport and ownership

`POST /v2/operations` uses the same private Unix socket and kernel peer-UID
authorization as v1. Request bodies and durable growth responses are bounded to
64 KiB; read-only responses are bounded to 1 MiB. The proposed, default-off
[finite I/O profile](finite-io.md) is the only exception, for one run. A global inventory that cannot
fit returns unavailable rather than dropping registrations. V2 status includes
both scalar and grouped records with an explicit kind. V2 mutations refuse
scalar records; callers must use their original API for those records.

Responses expose compact lifecycle observations. They omit repository URLs,
private member paths, command arguments/environment, raw manifests, continuation
secrets and history. An authorized creation/recovery or newly granted owner can
receive the bounded workspace root. Status does not disclose lease tokens.

The operations adapter authorizes a grant separately from response projection.
Supplying a current token never causes it to be exported again. Successful
administrative stop can return the retained owner capability only after fencing
has succeeded and continuation has been revoked. That capability does not revive
the stopped writer or make stopped initialization restartable.

A stopped ready owner whose tree holds files the checkpoint refuses can still
checkpoint and release once they are gone, but it can never launch the run that
removes them. `bench workspace renew_admin KEY` is root's local answer; it is
not a socket operation. It requires real and effective UID 0, a ready workspace
without archive, transfer, initialization repair or pending checkpoint, an owner
stopped with its continuation revoked, a stopped and empty execution scope, and
no active daemon operation on the key. It then retires the stopped token and
saves a successor lease for the same holder with a new token and a fresh
continuation, carrying the stopped execution and retained runs over unchanged.
The state transition admits exactly that change and still never clears a stop on
the same token. The successor's `grant_token` and `root_path` go to stdout only.
The operator path for a dirty stopped lease:

```sh
bench workspace stop_admin --socket SOCKET KEY
bench workspace renew_admin --state STATE --data DATA \
  --cgroup-root CGROUP_ROOT --mirror-timeout TIMEOUT KEY
BENCH_LEASE_TOKEN=NEW_TOKEN bench workspace run --socket SOCKET --holder HOLDER KEY -- COMMAND
BENCH_LEASE_TOKEN=NEW_TOKEN bench workspace release --socket SOCKET KEY   # only if still held
bench workspace reclaim --socket SOCKET KEY                                # optional
```

`renew_admin` opens state directly rather than calling the daemon, so it has no
`--socket` and must be given the `--state`, `--data`, `--cgroup-root` and
`--mirror-timeout` that `benchd` runs with. Without them it reads the CLI's own
default roots under the caller's config and cache directories, finds no such
workspace there, and refuses with `unknown workspace`. That refusal always means
the state it read has no workspace for `KEY`: the wrong `--state`, or a mistyped
key. The other verbs go through the daemon with `--socket`; run locally instead,
they need the same roots.

`COMMAND` commits or removes the files. `run` checkpoints and releases on exit
zero unless `--checkpoint=false`.

## Preparation and completion

Creation and recovery initialize the whole repository vector and confirm its
genesis remotely before reporting ready. A failed partial initialization retains
its durable identity and owner. Ordinary acquisition prepares the workspace once
under its new lease. Run and setup acquire raw ownership first and perform their
own preparation, avoiding duplicate acquisition hooks.

Run selects a member or the workspace root. Omitted or true `checkpoint` means a
successful payload is followed by a complete-vector checkpoint and release;
false retains ownership. A failed payload or checkpoint preserves the owner and
available execution evidence. Successful setup also confirms a complete-vector
checkpoint and releases. Selecting one setup member does not permit claiming
initialization ready without the other members' preparation proofs.

Recovery currently creates a fresh workspace and distinct task refs. Original
task-ref ownership transfer is separate pending work. Archive freezes the whole
workspace; deletion still requires a concrete reviewed local reap plan and the
persisted grace deadline. A socket archive request never approves deletion.

## Durable request identity

Growth operations bind the authenticated UID, API version, operation, request ID
and canonical request body. Completed receipts replay their exact historical
bytes before mutable ownership preflight, including after release, a new owner
or archive. A replayed historical capability does not restore an ended lease.
Current backend ownership checks still govern subsequent actions.

An active receipt returns `in_progress`; interrupted or uncertain work returns
`operation_in_doubt` without redispatch. Receipt-capacity exhaustion blocks new
growth but leaves stop, checkpoint, release and archive available. A missing
creation receipt whose core identity already exists also requires reconciliation;
it cannot silently resume partial initialization. Completion requires updating
the owned running receipt. Persistence failure preserves an uncertain identity
and can return an independently authorized retained grant for reconciliation.

Request receipts are not collected automatically. Retiring response payloads
requires durable replay protection; deleting completed rows is not collection.

## Verification boundaries

The shared response corpus distinguishes client-observable wire checks from
producer-only grant, fencing and complete-inventory evidence. Strict response
parsing cannot independently prove that a host fenced a writer. Producer checks
and actual managed execution drills must supply that evidence separately.

Current HTTP tests exercise real Git repositories and SQLite through the handler,
including mixed inventory privacy, lifecycle operations, UID/body/version receipt
conflicts, exact historical replay, capacity, failed completion persistence and
missing creation receipts. These handler tests do not establish real Unix peer
authentication, managed cgroup execution, CLI behavior or a deployed pilot;
separate runtime verification supplies that evidence.

The managed Linux daemon/CLI drill passed with actual XFS project quotas, cgroups,
kernel peer credentials, two repositories and an independently confirmed remote
checkpoint vector. It covers fresh/stale selected setup, partial initialization,
partial publication and retry, stopped detached descendants, historical request
replay, distinct-ref recovery, concrete reviewed reap, mixed inventory and receipt
capacity reconciliation. See [verification evidence](workspace-api-output.txt)
for source hashes, exact scope and remaining obligations.

If a daemon is killed before it seals an aggregate payload's log, administrative
stop fences the retained execution and revokes continuation; release refuses the
missing outcome. Local reviewed [history reconciliation](history-reconciliation.md)
can then record an explicit unknown outcome from the exact stopped execution and
observed capture or absence. It preserves replay protection and never fabricates
successful setup. See its [verification evidence](history-reconciliation-output.txt).
Local reviewed [initialization repair](initialization-repair.md) uses a separate
approved epoch; historical service receipts never acquire its fresh capability.
