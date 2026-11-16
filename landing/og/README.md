# The link preview image

`public/og.png` is what a link to superbench.dev shows when it is pasted into Slack, X or
iMessage: 1200×630, rendered from the suite's one share-image template in the frontdoor repo
(`scripts/og/render.mjs`), so every product's preview reads as one suite. The headline there must
match this page's `<h1>`, "One task, one bench."

frontdoor's SuperBench entry still carries its frame tagline as the headline ("One task's
checkout, kept warm and checkpointed."), so the committed image was rendered from that template
with the entry's `h` set to `One task, <em>one bench.</em>` and Playwright's own Chromium instead
of channel `chrome`. Once frontdoor's entry carries the page's headline, render it as is.

To regenerate it after changing the headline or the mark, from a frontdoor checkout:

```bash
node scripts/og/render.mjs /tmp/og
cp /tmp/og/superbench.png <this repo>/landing/public/og.png
```
