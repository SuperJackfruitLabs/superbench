# SuperBench docs site

The pre-release documentation published at `docs.superbench.dev`. Astro + Starlight, on the
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

## Pre-release, on every page

SuperBench is not released. `src/components/Banner.astro` replaces Starlight's `Banner`, so every
page, the home and the 404 included, carries the same notice: an experimental single-host
prototype under active development, nothing released or supported for use yet, these docs describe
`main` and will be refreshed at the first release. No page can opt out. At the first release the
banner is revised with the rest of the docs.

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
  every page's `.md` route and the `llms*.txt` files. Every HTML page must carry the banner and a
  link to the repository, and `llms-full.txt` must exist and say SuperBench is not released yet.
  Adapted from `landing/scripts/check-copy.mjs`.
- `scripts/check-links.mjs` checks every internal link and anchor in `dist/` resolves.

The honesty rules live in `scripts/honesty.mjs`: no install commands, no version numbers, no local
host, agent or team names, no "production-ready" or "secure isolation", and "production",
"sandbox", "released", "isolates untrusted" and "isolated"/"isolation" only when negated. When a check fails, fix the page,
not the rule.

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

**It says only what `main` does.** Every page is written from the repository's README, `docs/`,
the CLI help and the API code. `start/status.md` follows `docs/implementation.md`'s phase table.

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
