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

The subsequent [disabled reader candidate](../../web/README.md) uses a pinned
proposed observation corpus for its actual JavaScript viewmodel. It adds no live
reader/authentication boundary and does not establish browser rendering proof for
this design. Its inventory/history subset leaves the full Phase 2 work explicit.
