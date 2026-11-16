# The link preview image

`public/og.png` is what a link to superbench.dev shows when it is pasted into Slack, X or
iMessage: 1200×630, rendered from the suite's one share-image template in the frontdoor repo
(`scripts/og/render.mjs`), so every product's preview reads as one suite. The headline there must
match this page's `<h1>`, "One task, one bench."

It is not in the repository yet: it is rendered once frontdoor's renderer knows SuperBench. Until
then the page's `og:image` points at a file that does not exist. To render it, from a frontdoor
checkout:

```bash
node scripts/og/render.mjs /tmp/og
cp /tmp/og/superbench.png <this repo>/landing/public/og.png
```
