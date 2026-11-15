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

Typed managed helpers pin their actual binary, fixed policy and state-root
identity before launch. A durable operation deny marker and the same outer policy
lock cover queued and in-flight helper mutations. An initial acknowledgment does
not wait for that lock; terminal cancellation requires the independently verified
deny and quiescence. Older helpers reject the typed commands, causing this private
path to remain unavailable rather than falling back to ordinary untracked apply.

Managed restart reconciliation adopts only history that was durably precommitted
for the exact operation, execution scope and original history digest before
backend Start. It preserves actual partial or absent output and seals an unknown
interrupted outcome after independently fencing the original scope. Legacy,
unbound or changed authority remains in doubt. Neither cancellation nor restart
creates setup success, checkpoints dirty work, releases ownership or renews a
revoked continuation. A stopped partial materialization is retained for explicit
future repair.

The fixed privileged helper creates a new verified `benches` parent with exact
traversal permissions despite restrictive umask 0077. It does not widen existing
unknown directories or change the process umask. See
[verification evidence](operation-cancellation-output.txt) for the exact managed
Linux scope, artifact hashes and remaining limits.

State schema 27 records the exact immutable binding and terminal outcome of a
verified cancellation in the same transaction as core finalization. After a
receipt-save interruption, that conclusion can be replayed without touching
writers or requiring logs that ordinary reviewed retention has since collected.
A normal finished operation has no cancellation conclusion: its first cancellation
still requires complete original helper, Git, scope and sealed-history proof.
Missing or changed proof stays in doubt. Migration never invents conclusions for
older terminal rows, and a later owner cannot inherit an earlier conclusion.
