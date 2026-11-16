# Bench browser mockup

Open [bench-browser.html](bench-browser.html) directly in a browser. It is a
standalone design preview with illustrative data and no network requests or
service access. The first bench opens its detail view. Timeline, files, saved
diff and logs work with local sample content; terminal and takeover remain
unavailable. Tabs support arrow keys, Home and End. The layout adapts to narrow
screens.

This starts the original Phase 2 requirement for mockups before implementation.
It does not complete Phase 1, establish viewer contracts, enable a capability or
claim an operational pilot.

The design keeps the observation time visible, separates a recorded checkpoint
from the current working tree, shows remote confirmation and multi-repository
members, and distinguishes a held lease from ready and archived benches. Saved
logs do not serve as process-liveness evidence.

Before a live viewer is built, define and review its authentication and scoped
read access, snapshot identity and freshness, bounded path-safe file/diff/log
reads, output filtering, unavailable states and retention behavior. Watching a
terminal must not grant write access. Taking over must follow verified stopping
or fencing and lease acquisition. These are implementation work, not functions
of this mockup.

The [reader codec and models](../../web/README.md) use a pinned proposed
observation corpus. They add no product page, styles, activation entry point or
live authentication boundary. A product UI is deferred until operator mockup
review and use of the shared design system. The full Phase 2 work remains open.

## Fixture-backed reader states

Open [reader-states.html](reader-states.html) directly as a standalone static
review artifact. It needs no server or JavaScript and makes no network requests.
The displayed inventory, transferred-source block, latest-only checkpoint and
retained-history examples are generated through the actual proposed reader codec
from the pinned corpus. An explicit expired state shows why an old observation
cannot authorize a new read. Logical labels are escaped; internal bench IDs,
physical host identities, private paths and ownership capabilities are not shown.

Regenerate with `node scripts/render-reader-mockup.mjs`. The focused JavaScript
suite checks reproducibility, hostile text, missing fixtures and zero versus
unavailable values. These are source/model checks, not browser layout or runtime
authorization proof. No live adapter, service, listener or terminal is added.
