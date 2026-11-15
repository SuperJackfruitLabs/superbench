# Cross-host original-ref handoff implementation

The existing reviewed original-ref transfer works within one state/data pair.
Cross-host handoff is under implementation and is not an available CLI command.
The private artifact codec is the first part. It grants no writer capability,
does not attest that a source stopped, and does not enable suite consumers.

An artifact binds the source workspace and its immutable transfer approval to
one destination host, state identity, workspace identity, key and creation ID.
It retains every repository, original task branch, integration base and committed
member, together with the exact checkpoint seal and manifest digest. The complete
canonical representation has a bounded size and integrity digest. Unknown or
duplicate fields, changed vectors and another destination are refused. It has
no lease token, local checkout path, command, environment or claimed fencing bit.
Repository addresses remain private administrative data.

The coordinator's host observation uses the stable Linux machine identity (in
the same hash domain as existing managed execution) or the macOS platform UUID.
Only a domain-separated digest leaves that observation; missing, ambiguous or
uninitialized identities fail closed. This identifies a machine for destination
binding. In particular, observing a macOS identity does not add managed process
fencing, and a label or hostname cannot substitute for the observation.

The remaining coordinator must establish these conditions before import:

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
issuer. Actual cross-host Git/runtime and crash/replay tests remain required.

The private journal implementation retains a state incarnation, unique
destination offers, one export per source and imports bound to the exact offer.
It checks writes by reading them back and retains terminal identity on replay.
It has not been activated by a core migration or wired into source retirement
and destination creation. Its SQL tests demonstrate journal behavior only;
they do not demonstrate a stopped source, a completed import or usable commands.
