# Cross-host original-ref handoff implementation

Reviewed local administrative commands now export original task refs from one
host/state pair and import them into an explicitly reserved destination. Source
launch exclusion, metadata-only detachment and destination initialization use
versioned core state and durable journals. The private artifact grants no writer
capability, does not attest that a source stopped, and does not enable suite consumers.

An artifact binds the source workspace and its immutable transfer approval to
one destination host, state identity, workspace identity, key and creation ID.
It retains every repository, original task branch, integration base and committed
member, together with the exact checkpoint seal and manifest digest. The complete
canonical representation has a bounded size and integrity digest. Unknown or
duplicate fields, changed vectors and another destination are refused. It has
no lease token, local checkout path, command, environment or claimed fencing bit.
Repository addresses remain private administrative data.

## Administrative trust requirement

Offers, export plans and artifacts are unsigned. Their SHA-256 digests bind
bytes; they do not authenticate a machine, an operator or a completed source
relinquishment. The commands do not independently verify who produced these
files. An invented destination offer can permanently exclude a source key;
an invented export artifact can leave an old writer using the same branches.

Use this flow only between operators who independently verify both endpoints
through an authenticated channel they already trust. Before approving export,
the source operator must confirm the offer with the destination operator:
actual host and state identities, reserved key and workspace ID, creation ID,
repository/member allowlist and exact offer digest. Before approving import,
the destination operator must confirm the final artifact with the source
operator: exact source and destination identities, transfer approval, complete
repository/branch/commit vector, immutable checkpoint seal and digest, and the
source's completed permanent launch exclusion and writer fencing. Verify the
actual terminal source procedure and retained evidence, not an asserted field
or an artifact checksum alone. Transfer the exact approved bytes through that
same authenticated channel.

`--approve` records the local operator's review; it supplies no signature or
remote-origin proof. Refuse files from an unknown sender, a mismatched endpoint
or an unverified source. There is no automatic acceptance of unsigned third-party
offers or artifacts, and an unreachable source still needs independent fencing.
No additional issuer, signing key or network authority is introduced here.

The coordinator's host observation uses the stable Linux machine identity (in
the same hash domain as existing managed execution) or the macOS platform UUID.
Only a domain-separated digest leaves that observation; missing, ambiguous or
uninitialized identities fail closed. This identifies a machine for destination
binding. In particular, observing a macOS identity does not add managed process
fencing, and a label or hostname cannot substitute for the observation.

The coordinator establishes these conditions before import:

1. The destination creates and persists an offer using its actual local host and
   state identities. A user-selected label or reflected body is not that evidence.
2. Source inspection and approval bind this exact destination and complete vector.
   The source commits its permanent launch prohibition before branch detachment.
   The existing actual writer-stop and checkpoint checks remain mandatory.
3. Export publication is durably exclusive to that destination. It must also
   exclude the old same-host continuation path. Two exported artifacts cannot
   authorize two destinations for one transfer, including after a lost reply.
4. The destination verifies the independently reviewed export and its current
   offer, persists immutable import intent before any allocation or checkout,
   and checks remote original refs against the complete sealed source vector.
5. Creation, setup and genesis use the same original task refs and fresh local
   ownership. Failures retain the exact destination; they never restore source
   authority or silently generate a new import identity.

New durable cross-host authority needs versioned state that old binaries refuse;
a parser and a new file beside the database cannot provide that guarantee.
An unreachable source still requires independent fencing and its own reviewed
recovery procedure. This flow creates no network lease service or alternative
issuer. A two-host no-hook trial is verified below. Managed cross-host hook,
crash and fencing verification remains outstanding; local journal and lifecycle
tests are separate evidence.

The private journal implementation retains a state incarnation, unique
destination offers, one export per source and imports bound to the exact offer.
It checks writes by reading them back and retains terminal identity on replay.
Schema29 binds these journals to core source exclusion and destination creation.
SQL tests establish journal behavior; real Git/SQLite lifecycle tests separately
exercise export, import, partial failures and retries on one machine. Neither
kind of local test alone establishes remote source fencing or a completed two-host runtime trial.

## Reviewed local commands

The coordinator adds local administrative commands under `bench handoff`.
These commands do not use the daemon socket or accept a lease token. Use the
current binary for every participant. Schema29 makes older binaries refuse
this authority; a copied/restored database is not an independent state service.
The state incarnation and observed machine identity must both remain the ones
in the offer. Machine identity identifies the host; it does not fence writers.

On the destination, reserve the intended card key before any checkout effects:

```sh
bench handoff offer --creation-id import-card-example card_example > offer.json
```

The destination may use the same original card key as the source. Its fresh
workspace ID and actual host/state are retained in that offer. Normal scalar
creation, aggregate creation, recovery and same-host transfer cannot consume
this reservation. Exact retries return the original offer; keys and terminal
IDs are never recycled. `--quota-bytes` fixes an explicit destination budget;
zero derives declarations from the exact sealed commits.

Copy the private offer to the source. Source inspection requires an unleased,
ready source with no pending checkpoint or active stage, its clean committed
whole vector, remote checkpoint/pin/task-ref confirmation, and exact physical
worktree registrations. Ignored source artifacts and administrative/index
bytes are included in the reviewed snapshot and remain on the source.

```sh
bench handoff inspect-export --offer offer.json --transfer-id export-card-example card_example > export-plan.json
bench handoff begin-export --plan export-plan.json --approve REVIEWED_PLAN_DIGEST card_example
bench handoff continue-export --transfer-id export-card-example card_example > artifact.json
```

Review the concrete plan before `begin-export`. Beginning journals that exact
approval and commits permanent source launch exclusion in the same transaction.
Detachment starts only after this commit. A partial failure keeps the source
excluded and retains the exact destination. Continuation verifies the complete
mixed attached/detached vector, detaches metadata only, verifies every member
again, and confirms the checkpoint before publishing the final artifact. The
old same-host continuation commands cannot resume this cross-host export.
Source bytes, indexes, registrations, history and remote original refs are not
deleted or reset. Terminal export replay returns the same artifact even after
the destination legitimately advances its original task branches.

Copy and independently review the final artifact on its destination. The
administrator must verify the source retirement independently before approving
import; an artifact digest establishes integrity, not remote fencing:

```sh
bench handoff import --artifact artifact.json --approve REVIEWED_ARTIFACT_DIGEST card_example
bench handoff continue-import --creation-id import-card-example card_example
```

Only the exact existing actual-host offer may accept it. Import intent commits
before mirror selection, quota allocation or materialization. The original
remote task refs must still equal the exact sealed source vector; a divergent
local mirror ref is also preserved and refuses admission. Correcting a remote
ref does not authorize automatically rewriting a divergent local mirror ref.
Reconcile that separately after review. The destination receives a fresh local
initialization owner and a fresh whole-vector genesis while retaining the
original branches and integration bases. Import completion shares the actual
ready/unleased core transaction, including a later explicitly repaired
initialization. SQL failure retains the import intent and original owner.

Stopped initialization is never revived by replay. Use its explicit reviewed
initialization repair commands; the original import identity/provenance stays
fixed. Cancellation revokes only a fully authenticated original initialization
owner, never a later repair or operational owner. Completed replay returns no
later capability or root for another holder. No timeout grants a replacement
owner, no retry generates a new transfer/creation identity, and no source
failure restores source authority.

The artifact digest binds reviewed bytes; it is not a signature, remote
quiescence attestation or network authorization. The independent endpoint checks above are mandatory; an administrator must
transfer and review the actual final export produced by the source procedure. Importing
an invented artifact does not prove that another machine stopped. This feature
does not add a distributed lease service or protect against trusted operators
writing directly outside the protocol.

No-hook initialization can run on a laptop. Configured supervised hooks retain
the existing Linux backend requirement; a macOS host identity does not supply
that backend. Such an import retains its exact initialization ownership rather
than reporting managed readiness. The separate cooperative local preparation
mode is not supervised aggregate initialization evidence. Managed hook/crash
verification is required in addition to the journal, native lifecycle and no-hook
two-host evidence.


## Two-host verification scope

The implementation in [the cross-host handoff change](https://forge.superjackfruit.com/SuperJackfruitLabs/superbench/pulls/43)
passed an actual macOS-to-Linux transfer between independent host/state pairs.
The trial used no setup hooks and unmanaged logical quotas; it did not establish
managed macOS fencing or privileged cross-host recovery.

It verified both original task branches, permanent source exclusion and retained
source identity, metadata-only checkout detachment with ignored bytes and index
preserved, a fresh destination workspace identity and whole-vector genesis,
late-holder replay without a token or root grant, and a later checkpoint/release
while the original artifact remained immutable after remote task refs advanced.
A rejected unsupported repository transport was a separate preflight refusal;
the successful trial used an accepted Git transport without weakening validation.

This trial does not authenticate unsigned administrative files. The independent
operator checks above remain required. Managed setup hooks, controller-loss and
cross-host fencing need their own rerunnable verification before those guarantees
can be claimed.
