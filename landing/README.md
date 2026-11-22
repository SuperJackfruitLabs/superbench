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

## How it deploys

A Cloudflare Worker with static assets named `superbench-site`, configured by
`landing/wrangler.jsonc`. The custom domains (the apex and `www`) are declared there, so a deploy
attaches them. `scripts/deploy-sites.sh` builds, checks and deploys it. `.github/workflows/sites.yml`
runs that script from the GitHub mirror, with the organisation's Cloudflare token, on every merge to
main that touches the site. It can also be run by hand.
Forgejo CI builds the site on pull requests that touch `landing/`.
`assets.not_found_handling: "404-page"` serves `dist/404.html` with a real 404.

## What is deliberate about the page

**Try, carefully.** SuperBench v0.0.1 is the first release, an experimental single-host release.
The page says what it is and why it matters, says plainly that v0.0.1 is experimental, Linux
amd64 only and for trusted repositories, and shows how to download and verify it from the GitHub
release (`gh release download`, `sha256sum --check`, `gh attestation verify`). The docs' install
page has the host steps. The release it offers is `DOCUMENTED` in `scripts/releases.mjs`.

**It says only what the claims ledger allows.** Every capability sentence rests on a claim in the
SuperBench section of the suite's claims ledger, and every claim the ledger marks `limited`
carries its caveat beside it on the page. Roadmap items appear only under "What's not done yet".
The comment at the top of `src/pages/index.astro` says how the copy maps to the claims.

**The copy is checked.** `scripts/check-copy.mjs` fails the build when the page lacks
"Experimental", the release it names, "single-host release", a link to that release or a link
to the repository, or when it contains a curl, npm or go install command, a version number that
is not a released tag (stamped and dated in `CHANGELOG.md`; unreleased, future and pre-release
versions fail), "production" or "production-ready", "secure isolation", a multi-host claim,
pre-release wording such as "not released", or a local host or agent name. `llms.txt` must name
the release and its limits.
It scans `dist/index.html`, `dist/404.html` and `dist/llms.txt`; run it on other files with
`node scripts/check-copy.mjs <file>…`.

**The frame names the latest release.** The suite frame renders `Releases · vX.Y.Z` on this page.
A version is allowed once the release-prep script stamps it in `CHANGELOG.md`; the page keeps
naming `DOCUMENTED` until it is refreshed for that release.

**Cyan is vibekit's.** SuperBench's colour is vibekit's cyan (OKLCH hue 173), named by
`PRODUCT_ACCENTS.superbench`. `src/accent.ts` reads its Daylight tokens for light and dark from the
package's `accents.css` at build time, so the page follows vibekit rather than a copy. Only the
grounds are the page's own.

**No scenes yet.** The first page is text, the mark and the frame.

**Discovery.** `public/llms.txt`, a sitemap (`@astrojs/sitemap`), JSON-LD in the page head, and
`public/og.png`, rendered from the suite's one share-image template (see `og/README.md`).

**No analytics, no sign-in.**
