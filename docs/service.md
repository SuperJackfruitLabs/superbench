# Local daemon and CLI

`benchd` owns one host-local Unix socket and one private state store. It exposes
no TCP listener. This remains a prototype. The reviewed
[Linux service installation](install.md) payload is available;
archive/reap, retention and suite adapters are subsequent work.

## Start and connect

Build `go build -o bin/benchd ./cmd/benchd` and
`go build -o bin/bench ./cmd/bench`. On Linux, provision a delegated cgroup-v2 root
as described in [execution](execution.md). Cooperative operations also run on
macOS; supervised runs fail before acquiring a lease there.

```sh
bin/benchd --state /absolute/private/state --data /absolute/data \
  --socket /absolute/runtime/benchd.sock --cgroup-root /sys/fs/cgroup/superbench \
  --operator-uids 1000,0 --reader-uids 1001
bin/bench create --socket /absolute/runtime/benchd.sock \
  --request-id create-task-1 --repo /absolute/remote.git task-1
bin/bench status --socket /absolute/runtime/benchd.sock task-1
bin/bench run --socket /absolute/runtime/benchd.sock \
  --request-id run-task-1 --holder work-run-1 task-1 -- sh -c 'git status --short'
```

Replace the example uids with actual local accounts. Operators are trusted to
execute arbitrary commands as the daemon user. The default operator set is the
daemon uid and root. Readers can query status/host-status only. Socket access also
requires filesystem traversal and write permission: the socket is mode 0660 and
its parent must not be group/world writable. Arrange a trusted local group for
readers/operators; uid flags do not themselves grant filesystem access. The state
directory must be private (0700), owned by the daemon and outside bench data.
All daemon paths must be absolute. Never expose this socket through a TCP proxy.

The HTTP server reads kernel peer credentials (SO_PEERCRED on Linux, LOCAL_PEERCRED
on macOS); supplied identity headers cannot grant authority. Lease tokens are
holder capabilities, independent of caller uid. A cooperative `lease` cannot
fence an externally launched writer. Only `run`/`setup` create supervised scopes.
An operator can use `stop --admin`; it fences the recorded scope before returning
the retained holder token. Status and checkpoint output never return that token.
Treat operation output and saved state as private.

CLI remote output uses versioned snake-case API result objects; direct local CLI
output retains its original engine format. A failed command preserves its exit
status and emits retained run metadata. `migrate` and `configure-host` remain
local administrative commands. Keep host configuration consistent with the
state/data/cgroup paths used by the daemon. Do not run competing direct writers
while benchd serves that state; direct core commands do not enforce the socket's
uid policy. The daemon's lifetime lock prevents a second daemon from serving the
same state, while the core's OS lock serializes its Git and state mutations.

## Retry and interruption

Growth operations (create/recover/lease/run/setup) require a request ID. The CLI
prints an automatically generated ID to stderr when `--request-id` is omitted.
Save it before issuing work whose reply might be lost. Retry with the **same ID
and arguments**. Body changes with that ID conflict. IDs and responses are scoped
to the authenticated peer uid. Completed replies are durably cached; repeating a
lease reply returns its historic token without changing the current holder.
Always use status to determine the current state.

A disconnected client does not cancel task-owned execution. A retry while the
operation runs reports `in_progress`. Daemon restart marks unfinished receipts
`operation_in_doubt` and never automatically runs them again. Inspect status,
stop the recorded execution if necessary, checkpoint committed work and release
using the retained capability. Changing the ID is not reconciliation.

SIGTERM cancels supervised runs, which fence before retaining their lease for
inspection. SIGKILL can leave descendants alive; restart and `stop --admin`
recover from the durable execution scope. No timeout proves that a writer stopped.
Shutdown stops new admission and retains the state lock/database until admitted
handlers finish, including slow Git work. HTTP shutdown has a ten-second grace
period, but that period does not release ownership over an unfinished core effect.
A service manager may ultimately SIGKILL a stuck daemon; the same recovery rules
then apply. Git network calls still have no application deadline.

Checkpoint/release/stop do not create growth receipts, so receipt capacity does
not prevent reconciliation. They still require normal authority and valid state.
A repeated release can fail after it cleared the lease; inspect status rather
than applying an old token to a new holder.

## Bounds

Requests are at most 64 KiB, responses at most 1 MiB. Unknown fields, duplicate
keys, null optional values, trailing documents and invalid UTF-8 fail. Maximum
concurrent connections is 64; active operations default to eight (configurable
1–64). Body/header read deadlines and response write deadlines bound transport
work, not execution lifetime. Overlarge status fails; query an individual key.
There is no pagination yet.

Receipts reserve 64 KiB per pending result, at most 10,000 rows and 64 MiB of
encoded replies. The private SQLite database uses FULL synchronous commits.
Receipt exhaustion refuses growth until reviewed retention is implemented; do
not manually delete it to retry uncertain work. The bytes budget covers payloads,
not all database metadata or filesystem allocation. Keep state on its own
monitored filesystem. Save failure after an effect reports `operation_in_doubt`;
the core lease/checkpoint remains the recovery authority.

## Contract impact and evidence

Producer: this repository's protocol/service layers. The local CLI is the sole
implemented consumer. The canonical request schema and shared vectors are pinned
from treaty commit `af7ada2`; `internal/protocol/testdata/source.json` records its
full revision and SHA-256 hashes. `scripts/sync-contracts.py` refreshes those copies.
The Go decoder runs shared valid/invalid/lexical vectors; treaty validates the
structural vectors with JSON Schema. Responses currently use the documented DTOs;
a shared response schema and published SDK are still pending.

- Ownership amendment: [charter PR #37](https://github.com/SuperJackfruitLabs/charter/pull/37),
  based on accepted charter revision `be8a10f`. The precise Environment split is
  proposed; this code does not claim it has been accepted.
- Wire proposal: [treaty PR #29](https://forge.superjackfruit.com/SuperJackfruitLabs/treaty/pulls/29).
  This is local RPC plain data, outside the CloudEvents catalogue. No new issuer,
  Accounts audience/grant or remote authority is introduced.
- Inspected future consumers: AgentPod `e9d6ddda`, SuperPipeline `5501b253`.
  Neither has an adapter in this checkpoint. SuperDispatch/SuperWitness/Salaam
  integration and rollout evidence remain pending.

Local `scripts/check.sh` covers race-enabled actual Unix socket lifecycle,
same-id replay, body conflicts, old-holder replay safety, denied/read-only peers,
forged headers, malformed/oversized requests, socket singleton/file preservation,
credential redaction, interrupted receipt recovery and oversize-result retention.
The full engine, host quota and protocol regressions run in the same check.
The disposable Linux drill in `scripts/daemon-integration.py` additionally checks
real cgroups and different peer uids; its recorded output is
[daemon-linux-output.txt](daemon-linux-output.txt).
