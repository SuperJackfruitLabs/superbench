# Operation cancellation candidate

This proposed extension is private and unavailable through production configuration
or `Serve`. A schema-valid observation is not independent evidence that writers
were stopped. The accepted lifecycle contract is unchanged.

The private transport keeps the existing 64 normal connection reservations and
adds eight classification reservations on the same Unix endpoint: at most 72
connections. Kernel peer identity, authorization, and bounded complete request
parsing precede promotion into the normal reservations. A parsed cancellation
keeps its classification reservation, persists the exact target's cancellation
intent, and returns without waiting for a host lock, helper, or normal operation
slot. `Config.MaxActive` still bounds normal active operations independently.

Private connections close after one response, including error responses. Eight
incomplete readers can occupy the classification reservations only within the
existing header/body deadlines. Admission remains available when 64 fully parsed
normal requests are blocked. This is not a fairness guarantee against concurrent
incomplete readers or an untrusted local denial-of-service attacker.

The initial `cancel_requested` acknowledgment certifies durable intent only.
Independent exact-operation reconciliation must prove the helper deny barriers,
writer fencing, sealed retained history, and inactive operation before producing
`cancelled`. A durable completed original receipt instead yields the cap-free
`already_completed` observation; cancellation never renews a historical grant.

Released history remains protected while its operation epoch is active, including
successful workspace release before the historical response is committed. Each
collection read or mutation checks the same durable launch inventory under its
state lock/transaction. The original completed-receipt cutoff permits core
finalization and then ordinary retention; it does not permanently pin all history.

After a successful validated exact-target cancellation acknowledgment, the private
producer retains that authenticated UID/request-ID/digest binding across restart.
Within the same durable service-state lineage, replaying the preserved exact
original body is receipt-only: it returns historical bytes or an existing
non-executing conflict, and cannot admit fresh growth. Unknown, failed or malformed
acknowledgments establish no such guarantee. This does not cover loss, replacement
or restoration of the receipt database, and does not authorize new request IDs,
changed arguments, release, reacquisition or execution.
