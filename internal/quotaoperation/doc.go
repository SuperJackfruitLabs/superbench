package quotaoperation

// Durability protocol:
//
// The fixed helper first verifies its original policy/root/binary binding, then
// holds one privileged ledger-wide operations lock across marker inspection,
// publication and every allocation effect. A canonical fixed marker must be
// synced and read back before Apply can call the fixed-policy implementation.
//
// Complete typed pending files reserve their immutable operation identity but
// never authorize effects. Recovery retains and counts them; it never publishes
// or deletes an old pending record. Unknown, partial or conflicting metadata
// fails closed. Initial publication uses a fresh synced pending file and rename.
//
// A known marker's identity never changes. Under the same lock, cancellation
// monotonically changes its one canonical state byte from 0 to 1, syncs the same
// validated inode, rechecks canonical bytes and pathname identity, and syncs its
// directory. Failed or torn writes acknowledge nothing. Exact retries sync an
// existing 1 again. This path needs no extra metadata slot at full capacity.
//
// Markers are permanent replay protection. There is no collection, expiry or
// operation identity reuse. Proof establishes only that this helper's typed
// invocations cannot perform further quota effects. It does not prove execution
// fencing, log sealing, workspace ownership, or complete service cancellation.
