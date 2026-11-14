# Repository quota requests

A repository can request a smaller bench allocation in `.bench/config.toml`:

```toml
[quota]
size = "4G"
```

Sizes are positive whole KiB, up to 1 PiB. `K`, `M`, `G`, `T` and `P` use
binary powers, as do `KiB`, `MiB`, `GiB`, `TiB` and `PiB`. `B` and unitless
values mean bytes and must be divisible by 1024. Whitespace, fractional values,
zero and unknown fields are rejected. Omission uses the host's default bench
allocation. This table accepts no paths or inode-policy overrides.

On managed Linux XFS hosts, the root-owned `BenchBytes` policy is the maximum
and the default. Requests above it are refused before checkout materialization.
The request changes only the new bench's byte cap; shared store/mirror caps,
inode limits and host reserves remain controlled by root policy. Logical
reservations count the actual selected cap, including unfinished allocations.
Checkout-local caches consume the bench cap.

Unmanaged hosts, including macOS, record the request without enforcing a
per-bench filesystem quota. `RequestedQuotaBytes` is the pinned request;
`QuotaBytes` and `ProjectID` describe managed enforcement. A recorded request
alone is not evidence of enforcement.

## Pinning and recovery

Creation reads the bounded regular configuration blob from its immutable source
commit before allocation. It journals that request with the source SHA. Retrying
after a lost helper reply preserves the original reservation even if the remote
branch has advanced. The privileged helper never reads repository configuration.
Invalid configuration remains subject to strict preparation validation, which
preserves the initialization lease and checkout for repair.

Adding, removing or changing the request in an existing checkout does not resize
it. Ordinary acquisition and supervised execution refuse the mismatch. A held
lease can still checkpoint/release its changes; local repair acquisition remains
available. Commit and checkpoint the intended configuration, then recover its
branch under a new key to obtain a new allocation. Preserve the original bench
until its evidence is reconciled. A checkout that exhausts a small cap is also
preserved; retry never deletes files to make it fit.

State schema 11 records the pinned request. Existing registered benches retain
their allocation and an omitted request. Legacy pending creation intents that
already record an allocation retain their default rather than adopting a newly
understood configuration field. This also covers a helper allocation whose reply
was lost before its project ID reached the journal. All legacy intents retain an
omitted request. Older core binaries refuse the new state schema.

## Helper and ledger compatibility

The fixed helper accepts `apply bench KEY [BENCH_BYTES]`. The optional numeric
request must be whole KiB within the root policy ceiling. Shared allocations
accept no request. Omitting a request on an existing allocation retains its
assigned size; changing an existing size is refused. No arbitrary filesystem
path, XFS command or root policy change is accepted from the worker.

Ledger format 2 pins the filesystem UUID and owner, byte, inode and reserve
policy. A validated legacy ledger upgrades without resizing its allocations.
The new format omits the identity field required by older helpers, so they refuse
it. Install the new helper before using repository quota requests; a helper
downgrade requires an explicit migration rather than editing the ledger.
Ordinary allocation refuses changed root policy. Explicit direct-root inode
migration preserves custom byte caps and project IDs.

## Verification

Required checks passed on macOS and Linux. Unit tests cover quantity parsing,
immutable Git selection, request drift, policy binding, legacy creation replies
and mixed-format rejection. All nine disposable XFS quota groups passed, including
actual enforcement, lost replies, old/new compatibility and retirement. Managed
storage, inode, archive, grace and permission-recovery regressions also passed.
See [Linux verification](quota-linux-output.txt) for commands, artifact provenance
and complete normalized output. These are producer checks; no suite consumer or
live pilot conformance is claimed.
