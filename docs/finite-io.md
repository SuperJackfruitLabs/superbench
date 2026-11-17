# Finite execution I/O

**Proposed and disabled by default.** This note describes how a v2 `run`
request carrying the proposed finite-I/O profile crosses the producer. The
contract is treaty `fixtures/superbench-api/private-execution-io.json`, pinned at
`496959ac16bc207135abb5ebb32e99339d124021` in
`internal/protocolv2/testdata/private-execution-io.source.json`. Merging this code
does not accept that proposal, enable a consumer or claim a deployed run.

A profiled run sends `Content-Type: application/json; profile=superbench.finite-io.v1`
and one extra `io` object:

```json
{"version":"superbench.finite-io.v1","stdin_base64":"AAEC","stdout_limit_bytes":1048576}
```

SuperBench treats the input and stdout as opaque bytes. It parses neither.

## Gate

The service has a `proposedFiniteIO` field, like `proposedCancellation` and
`proposedRegistrationObservation`. Public `Serve` never sets it, and no request
field, header or environment variable can. With the gate off:

- the operation route reads at most 64 KiB, accepts any `application/json`
  media type and gives a `profile` parameter no meaning;
- `protocolv2.Parse` refuses an `io` field, so a finite run is `400 invalid_request`;
- the receipt lookup keeps its 64 KiB original-body and 512 KiB envelope bounds;
- the receipt ledger is never upgraded (see "Receipt format").

`internal/service/finite_test.go` compares the full reply bytes of a legacy run
request with the gate off and on.

## Data flow

1. **Read and classify** (`internal/operationbody`). With the gate on,
   `protocolv2.BodyLimit` returns `(limit, finite, err)`. The profile is an
   explicit boolean, never inferred from the limit. Only the exact profile raises
   the read bound to 2 MiB; an unknown, duplicated or RFC 2231 alias refuses with 415.
2. **Parse** (`protocolv2.ParseFinite`). A profiled body must be a `run` with
   exactly one valid `io`; an unprofiled body is the unchanged `Parse`. Profile
   without `io`, `io` without profile, and the profile on any other operation all
   refuse with 400. Base64 must be canonical and decode to at most 1 MiB;
   `stdout_limit_bytes` is 1 to 1 MiB. Cancellation parsing is skipped for a
   profiled body.
3. **Preflight** (`workspaceops.Manager.Preflight`). It reparses with the finite
   parser and builds the finite plan. The production plan
   (`payloadstage.NewInstalled`) resolves the installed service identity. A host
   without the complete installed binding refuses with `503 unavailable` before
   any receipt, acquisition or effect. There is no ordinary fallback.
4. **Reserve** (`operationreceipts`). The claim reserves the whole response bound,
   `protocolv2.ResponseLimit(request)`, which is `finiteio.MaxResponseBytes(io)`
   for a finite run. The bound is charged against the existing 64 MiB unresolved
   budget in the same transaction that publishes the identity, before any effect.
   A legacy identity reserves 64 KiB as before.
5. **Run** (`workspaceprep.Manager.Run` with a `payloadstage.FiniteRun`). It keeps
   every legacy rule: the same ownership checks, member preparation, history and
   stop/seal pipeline. The plan rechecks the installed identity at the admission
   gate and at the launch gate. The transport is the registered helper bridge
   (`payloadlaunch.PreparePayloadInput`) with stdout separated from the log and
   bounded at `stdout_limit_bytes` (`stageexec.Plan.ResultLimitBytes`). The
   daemon passes no environment: the helper builds its fixed per-member one.
6. **Deliver once** (helper, `payloadexec.Exec` and `internal/payloadinput`). The
   copied bytes travel inside the helper's framed request. While still root, the
   helper writes them to an anonymous memfd, seals it (seal, shrink, grow, write)
   and reopens it read-only at offset zero. It then drops credentials, marks every
   other descriptor close-on-exec and binds that descriptor to stdin once, just
   before `exec`. Empty input keeps the existing EOF stdin. Setup hooks and Git
   never receive the input.
7. **Capture** (`stageexec`). Stdout is copied into a buffer bounded at the
   requested limit. A write past the bound fails the pump: the runner fences,
   revokes the continuation and returns before sealing. The captured bytes are
   returned only after Stop, the transport's own fence and drain, and a successful
   log seal, and only when the leader exited with a known status. A nonzero exit
   keeps its exact stdout; it is still `operation_failed`.
8. **Publish** (`payloadstage.Complete`). The observation is built only when the
   runner's stopped execution and the current owner's stopped execution are the one
   this run launched, and the installed identity is unchanged. `finiteio.Observe`
   computes the count and SHA-256.
9. **Respond and retain.** `RunOutcome.IO` is the final field of the result.
   `protocolv2.ParseResponse` validates it against the original request's `io`:
   bytes, digest and bound. The response, and the receipt `Finish` stores, are
   bounded by the reservation from step 4.

## Where each bound is enforced

| Bound | Value | Enforced by |
| --- | --- | --- |
| Profiled request body | 2 MiB | `operationbody.Read` (`MaxBytesReader`) and `ParseFinite` |
| Unprofiled request body | 64 KiB | unchanged, with the gate on or off |
| Decoded input | 1 MiB | `finiteio.Input.Bytes`, `payloadwire.Request.Validate`, `payloadinput.Open` |
| Stdout | `stdout_limit_bytes` ≤ 1 MiB | `stageexec` result buffer; `finiteio.Observe` |
| Response and retained receipt | `MaxResponseBytes(io)` ≤ 2 MiB | `ResponseLimit`, `ParseResponse`, ledger `Finish` |
| Unresolved receipt reservations | 64 MiB total | ledger claim, charged before insertion |
| Profiled lookup | 2 MiB body in a 16 MiB envelope | `receiptlookup`, `ParseFiniteReceiptLookupResponse` |

## Receipt format

A finite reservation needs a per-identity limit. The ledger keeps format 6 until
the first finite claim. That claim creates `response_limits` and sets
`user_version` to 7 in one transaction. Legacy identities keep their 64 KiB
bound and have no row. Open refuses a table without version 7, version 7 without
the table, and a limit row without a receipt.

This upgrade is one-way. A binary from before this change refuses a format-7
ledger ("unsupported receipt schema") rather than misreading a larger body. With
the gate off, nothing upgrades the ledger and rollback stays possible.

## Crashes and lost replies

Identity, replay and recovery are unchanged. The original UID, request ID and
typed digest, which includes the `io` object, bind the receipt.

- An exact replay returns the original status and bytes, up to the reserved
  bound, without reading authority or launching again.
- Different `io` bytes or a different limit under the same request ID is a
  digest conflict.
- A timeout, disconnect or lost reply never permits a resend of the payload, a
  replay of setup or a new launch. A daemon crash leaves the identity interrupted
  and in doubt, as for any growth operation.
- Overflow, cancellation, an unknown exit, a pump or drain failure, a seal
  failure or a changed installed identity never produce `io` or a stdout prefix.
  In-progress, unavailable and in-doubt responses never carry `io`.
- The read-only receipt lookup may use the same profile to return a retained
  finite body. Without the profile, a body over 64 KiB is reported `unavailable`,
  never truncated.

## What stays unchanged

v1, unprofiled v2 requests, their responses, digests, defaults and limits,
setup, Git, cancellation and every other operation. A request without `io`
omits the field, and a response without an observation omits `io`.

## Limits

- **No full installed run has been exercised.** The unprivileged tests drive the
  real `workspaceprep` lifecycle through a fake backend and a plain-pipe
  transport. The privileged fixture proves sealed delivery after the actual
  credential drop. Neither runs sudo, the cgroup scope or the full helper `Exec`
  with a registered layout. An installed host is still needed for that.
- **No shared caches.** The finite helper uses its fixed per-member environment.
  There is no typed cache transport for the registered helper yet, so a finite run
  gets no shared Go, npm, Bun or Playwright cache selection.
- **The payload can reread its stdin.** Delivery is once per launch. The payload
  can still seek its own descriptor back to zero. Nothing else can write to it.
- **Stderr is not returned.** It goes to the execution log; only stdout is
  observed.
- **No CLI.** `bench workspace run` has no finite flags. The Go client
  (`client.CallV2`) sends the profile when `Request.IO` is set.
- **Proposed contract.** The treaty files are proposals. The adapter conformance
  above checks byte spelling, bounds and digests, not producer lifecycle.
