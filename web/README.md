# Proposed reader codec and mockup models

This directory contains the pinned proposed observation codec, fixtures, display
models and their tests. It ships no web page, styles, activation entry point,
renderer, listener or transport. Review the standalone static artifacts under
[`docs/mockups`](../docs/mockups/README.md) first. A product UI remains deferred
until operator review and must use the shared design system.

Models expose display fields only. They do not retain snapshot identifiers, bench
IDs or physical host identifiers. The codec still validates those protocol fields
and their request/response bindings before a model is produced. Latest-only
checkpoint evidence is not fabricated history; unknown time and storage remain
explicit. None of these display values grants ownership or content authority.

Run `node --test web/*.test.mjs` on Node 22.23.4 or newer. Hosted CI pins the
`.node-version` toolchain and runs these checks inside the ordinary non-root suite
through the required root-refusal CI conductor. Node is a development tool, not a
CLI or daemon runtime dependency.

Refresh the mirror deliberately with `node scripts/sync-reader-contract.mjs
--source /absolute/clean/treaty` after reviewing the owner revision and fixture
pins. Contract status remains proposed. No network authentication, content read,
terminal, producer safety, operational pilot or Phase 1/Phase 2 completion is
established by static mockups or codec tests. Browser rendering is unverified.
