# SuperBench docs site

The documentation published at `docs.superbench.dev`, for SuperBench v0.0.2. Astro + Starlight, on the
same structure and tooling as SuperDispatch's docs site.

```sh
npm ci
npm run dev     # http://localhost:4511
npm run build   # checks the claims, astro check, build -> dist/, then checks the copy and links
```

Astro 7 needs Node 22.23.4 or newer.

## Why npm, and why it sits outside the Go module

Like `landing/`, the site has its own `package.json` and `package-lock.json`. Nothing in `cmd/` or
`internal/` shares a dependency tree with it, and the Go checks never read it.

## The release, on every page

SuperBench v0.0.2, the second release after v0.0.1, is an experimental single-host release.
`src/components/Banner.astro` replaces Starlight's `Banner`, so every page, the home and the 404
included, carries the same release-status line: v0.0.2 is released as an experimental
single-host release, Linux amd64, for trusted repositories on one host and not production
software, with links to the release notes, the install page and the status page. No page can opt
out. The release it names is `DOCUMENTED` in `scripts/releases.mjs`; at the next release, refresh
the docs and change it with the banner.

## Claims and copy are checked

`npm run build` fails when a page says something the code or the claims ledger does not back:

- `scripts/check-claims.mjs` runs **before** the build, over the page sources. It reads the Go
  source one directory up: every `bench` and `bench workspace` flag (with its default) and command
  is on `reference/bench.md`, every `benchd` flag on `reference/benchd.md`, the v2 operations table
  is `internal/protocolv2`'s request shapes field for field, the errors table is its code-to-status
  map, and the `.bench/config.toml` tables match `internal/setup` and `internal/cachepolicy`. It
  also checks each page has a title and description, that the ledger caveats (SB-01 to SB-08) stay
  on their pages, and the honesty rules below.
- `scripts/check-copy.mjs` runs **after** the build, over everything published: every HTML page,
  every page's `.md` route and the `llms*.txt` files. Every HTML page must carry the
  release-status line, a link to the release it names and a link to the repository, and
  `llms-full.txt` must exist and state the release and its limits (experimental single-host
  release, Linux amd64, trusted repositories, cooperative leases, one host).
  Adapted from `landing/scripts/check-copy.mjs`.
- `scripts/check-links.mjs` checks every internal link and anchor in `dist/` resolves.

The honesty rules live in `scripts/honesty.mjs`:

- A version number must be a released tag: one `CHANGELOG.md` has stamped as
  `## vX.Y.Z — YYYY-MM-DD`, dated today or earlier (`scripts/releases.mjs`). An unreleased,
  future or pre-release version fails, and so does any other three-part number.
- "is released" must name a released tag in the same sentence.
- Install commands appear only on `start/install.md` (and the `llms-full.txt` and
  `llms-small.txt` that carry it).
- Nothing may still say SuperBench is unreleased ("not released", "pre-release docs", "no tags").
- No local host, agent or team names; no "production-ready", "secure isolation", "production-grade"
  or multi-host claims; and "production", "sandbox", "isolates untrusted" and
  "isolated"/"isolation" only when negated.

When a check fails, fix the page, not the rule.

## What is deliberate about it

**It wears the suite's docs theme**, `@superjackfruitlabs/vibekit-docs`, with
`sjlDocs({ product: 'superbench', status: 'wet paint' })`. SuperBench is still `docs: null` in
vibekit-frame, so the street, switchers, footer and Copy prompt fall back to `superbench.dev`
until frontdoor lists this site; the home's agent prompt names this site's llms files directly.
Ask Captain Jack is off on the home (`ask={false}`) until the suite worker serves this site.
`public/favicon.svg` and `public/og.png` are copies of `landing/public/`; change them there and
copy them here.

**The sidebar uses the suite's groups**: Start (what it is, status, security model, limits), Use it
(the concepts), Build on it (the local API) and Reference. The theme warns at build time when a
top-level group is anything else.

**It says only what the release and `main` do.** Every page is written from the repository's
README, `docs/`, `CHANGELOG.md`, the CLI help and the API code. `start/status.md` follows
`docs/implementation.md`'s phase table, and `start/install.md` the v0.0.2 release and
`docs/pilot-installation.md`.

**For agents**: `/llms.txt`, `/llms-full.txt` and `/llms-small.txt` (`starlight-llms-txt`).

## Publishing

A Cloudflare Worker with static assets named `superbench-docs`, configured by
`docs-site/wrangler.jsonc`. From the repo root:

```sh
scripts/deploy-sites.sh docs   # checks, builds, then wrangler deploy --config docs-site/wrangler.jsonc
```

The custom domain is declared in the config, so a deploy also attaches it. CI runs
`scripts/deploy-sites.sh all` on merge to main from the GitHub mirror
(`.github/workflows/sites.yml`) with the organisation's Cloudflare token.
`assets.not_found_handling: "404-page"` serves `dist/404.html` with a real 404.
