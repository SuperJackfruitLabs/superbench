# Proposed historical receipt lookup

The default-disabled private extension `POST /v2/receipts/lookup` implements the
PROPOSED Treaty amendment at revision
`c745dc79eab4e22ad55db7141f16082d8d36d090`. This is historical evidence recovery,
not an operation, renewed capability, readiness, fencing or release proof.

The request contains the original request ID and typed digest. The kernel Unix
peer UID must still be a configured operator; readers and demoted operators are
refused before lookup. A different operator UID cannot read another UID's receipt.
A completed record returns its exact original HTTP status and UTF-8 body, bounded
at 64 KiB. Other states disclose no original body. Oversized or invalid UTF-8
historical bodies return unavailable without truncation or modification.

Two read connections have dedicated admission independent of the 64 ordinary
connections and eight classifiers (74 total). Excess reads fail immediately; a
connection keeps its reservation until the underlying socket closes. These
bounds prevent reads consuming ordinary or cancellation-classification slots;
they do not remove the shared database's serialization requirements.

A missing receipt, including after state replacement, never claims or dispatches
anything. A client must park the original attempt rather than resending its
operation body. Any historical token remains private and may be used only under
the original operation's separate current authorization rules. The route is off
by default. Only `receipt_lookup` in the root-owned activation file turns it on
(see [pilot installation](pilot-installation.md#activation)); no request can.
