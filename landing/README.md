# SuperBench landing page

The public page at `superbench.dev`. Astro, one page and a 404, wearing the suite frame
(`@superjackfruitlabs/vibekit-frame`). Outside the Go module, with its own `package.json` and
`package-lock.json`.

```sh
npm ci
npm run dev     # http://localhost:4510
npm run build   # -> dist/, then checks the frame and the copy
```

Astro 7 needs Node 22.23.4 or newer.

`package-lock.json` and `public/og.png` are added in phase B, once vibekit-frame 0.0.17 and
vibekit-ui 0.0.19 are published: the lockfile from the first `npm install`, the image from
frontdoor's renderer (see `og/README.md`). Until then `npm ci` and the build cannot run.

## How it deploys

A Cloudflare Worker with static assets named `superbench-site`, configured by
`landing/wrangler.jsonc`. The custom domains (the apex and `www`) are declared there, so a deploy
attaches them. Nothing deploys it yet: the deploy workflow, which runs from the GitHub mirror with
the organisation's Cloudflare token, is added once `superbench.dev` is in the Cloudflare account.
Forgejo CI builds the site on pull requests that touch `landing/`.
`assets.not_found_handling: "404-page"` serves `dist/404.html` with a real 404.

## What is deliberate about the page

**Follow, not try.** SuperBench is not released. The page says what it is and why it matters,
says plainly that it is experimental and not released, and offers two ways to follow it: the
GitHub repository and its release feed. No install command, no version number, no download.

**It says only what the claims ledger allows.** Every capability sentence rests on a claim in the
SuperBench section of the suite's claims ledger, and every claim the ledger marks `limited`
carries its caveat beside it on the page. Roadmap items appear only under "What's not done yet".
The comment at the top of `src/pages/index.astro` says how the copy maps to the claims.

**The copy is checked.** `scripts/check-copy.mjs` fails the build when the page lacks
"Experimental", "not released yet" or a link to the repository, or when it contains an install
command, a version number, "production-ready", "secure isolation" or a local host or agent name.
It scans `dist/index.html`, `dist/404.html` and `dist/llms.txt`; run it on other files with
`node scripts/check-copy.mjs <file>…`.

**It will fail on purpose at the first release.** Once SuperBench tags a release, the suite frame
renders `Releases · vX.Y.Z` on this page and `check-copy` fails on the version number. That is
the moment to revise the page from "follow" to "try", not to loosen the check.

**Cyan is vibekit's.** SuperBench's colour is vibekit's cyan (OKLCH hue 173), named by
`PRODUCT_ACCENTS.superbench`. `src/accent.ts` reads its Daylight tokens for light and dark from the
package's `accents.css` at build time, so the page follows vibekit rather than a copy. Only the
grounds are the page's own.

**No scenes yet.** The first page is text, the mark and the frame.

**Discovery.** `public/llms.txt`, a sitemap (`@astrojs/sitemap`), JSON-LD in the page head, and
`public/og.png`, rendered from the suite's one share-image template (see `og/README.md`).

**No analytics, no sign-in.**
