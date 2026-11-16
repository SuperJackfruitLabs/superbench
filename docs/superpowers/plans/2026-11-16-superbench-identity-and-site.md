# SuperBench Identity and Landing Site Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make SuperBench a first-class suite product (vibekit accent, frontdoor frame, a follow-until-release landing site, and a listing on superjackfruit.com) without claiming more than it does.

**Architecture:** This is a producer-first chain across five repos. Each step merges, and where it publishes to npm, releases before the next step consumes it: vibekit (tokens), then frontdoor (frame), then superbench `landing/`, then the suite website and the marketing claims. Every step copies the most recent product additions: vibekit `905dc5b`, frontdoor `773a977`, SuperDispatch landing `cb94bef`/`1c22aea`/`37c03e7`, and website `ca775d1`/`bd37aa2`/`f05334b`.

**Tech Stack:** TypeScript, Svelte 5 and tsx (vibekit); Astro 7 web components and Cloudflare Workers (frontdoor, landing); Playwright e2e; npm OIDC publishing through the shared release kit.

**Spec:** `docs/superpowers/specs/2026-11-16-superbench-identity-and-site-design.md`

## Global Constraints

- Product name: `SuperBench`. Slug: `superbench`. Domain: `superbench.dev`. Repo: `SuperJackfruitLabs/superbench`. License: `MIT`.
- Accent: `cyan`, `ACCENT_HUES.cyan = 173`, `PRODUCT_ACCENTS.superbench = 'cyan'`. Per-look values come only from `accentRecipe(173, vibe, theme)`, never typed by hand.
- Mark: sjl-marketing `brand/product-logos/translucent-v2/products/superbench/svg/mark-color.svg`, with base `#038068`.
- Street: `'quarter'`. Dev port: `4510`. Role word `Bench`; line `One task's checkout, kept warm and checkpointed.`
- The landing page states only claims marked `verified` or `limited` in sjl-marketing `foundation/products.md`, and it must say "Experimental · not released yet".
- Shipped code and copy never name local agents, hosts or teams. No secrets.
- One PR per repo, one commit per item. Run the repo's own CI locally before pushing, and check PR state before every push. vibekit, frontdoor and superbench are forge-primary: open PRs on forge with `tea`.
- Tags publish npm packages: only after operator confirmation, following each repo's `release.yml`.
- Workspace guard: builds need at least 2 GiB free where they run. Run heavy builds (Storybook, Playwright) where there's space.

## Review Focus

1. **Cyan next to jade and teal in dark looks.** It must be visibly distinct from SuperWitness (155) and SuperDispatch (192) in every look, not only pass contrast. Task 1 asserts a minimum OKLCH hue distance of ≥15° from every other accent.
2. **Counts that silently stay at nine.** "Nine tools" copy, footer tile counts, keys 1–9 and OG headlines. Tasks 2 and 4 grep for `[Nn]ine` and `9` product counts and update every one; the tests assert 10.
3. **A docs link to a site that doesn't exist.** frontdoor derives `https://docs.<domain>`. Task 2 asserts that SuperBench has no docs link until the docs site exists.
4. **The mark at 16 px on a dark tab.** Task 3 uses `mark-compact` for the favicon, and its build check confirms `favicon.svg` is the compact master.
5. **An overclaiming page.** Task 3's copy test fails if the built page contains "production", "secure isolation", "install" commands, or a version number.

---

### Task 1: vibekit gains the `cyan` accent and the `superbench` product

**Files:**
- Modify: `src/lib/vibes.ts` (`ACCENTS`, `PRODUCTS`, `PRODUCT_NAMES`)
- Modify: `src/lib/accents.ts` (`ACCENT_HUES`, `ACCENT_LABELS`, `ACCENT_DESCRIPTIONS`, `PRODUCT_ACCENTS` with a comment line like the others)
- Modify: `tokens/source/accents.ts` (`ACCENT_PALETTE.cyan`, plus a `PUBLISHED_RAMPS.cyan` Studio ramp)
- Modify: `tokens/source/shared.ts` (the `superbench` product tile bg/fg per theme)
- Modify: `src/lib/appearance/appearance.ts` (`HEAD_SCRIPT` accent list and `HEAD_SCRIPT_CSP_HASH`)
- Modify: `src/lib/styles/tailwind.css`, `src/lib/styles/components/appnav.css`
- Regenerate: `src/lib/styles/accents.css`, `src/lib/styles/tokens.css`, `tokens/dtcg/looks/*.json`, `references/**` and `references/components/references.lock`
- Modify: `static/llms.txt`, `docs/non-svelte.md`, `docs/specs/vibes-sweep/accent-tokens.md`, `CHANGELOG.md` (under Unreleased)
- Test: `tests/accents.test.ts`, `src/lib/vibes.test.ts`, `src/lib/components/appearancepicker/{model,appearancepicker}.test.ts`

**Interfaces:**
- Produces: `PRODUCTS` includes `'superbench'`; `PRODUCT_ACCENTS.superbench === 'cyan'`; `ACCENT_HUES.cyan === 173`; generated CSS exposes `[data-accent="cyan"]` tokens and `--color-superbench`. Released as `@superjackfruitlabs/vibekit-ui` 0.0.19.

- [ ] **Step 1: Write the failing tests** in `tests/accents.test.ts`:
```ts
it('SuperBench wears cyan at hue 173', () => {
  expect(PRODUCT_ACCENTS.superbench).toBe('cyan');
  expect(ACCENT_HUES.cyan).toBe(173);
});
it('every accent is at least 15° from every other', () => {
  const hs = Object.values(ACCENT_HUES);
  for (const a of hs) for (const b of hs) if (a !== b) expect(Math.min(Math.abs(a - b), 360 - Math.abs(a - b))).toBeGreaterThanOrEqual(15);
});
```
Then extend the existing "accent recipe makes every palette" test, the signal-band loop and the appearance-picker model test to iterate the 11 accents and 11 products. These must fail on `cyan` and `superbench`.
- [ ] **Step 2: Run** `pnpm vitest run tests/accents.test.ts src/lib/vibes.test.ts src/lib/components/appearancepicker`. Expected: FAIL (`cyan` unknown).
- [ ] **Step 3: Add the names, hues and labels** in `vibes.ts` and `accents.ts`. Generate `ACCENT_PALETTE.cyan` for the 8 looks by calling `accentRecipe(173, vibe, theme)`, and paste its exact output. Do the same for the Studio ramp. The product tile values in `shared.ts` follow the teal/indigo pattern, derived from the cyan wash and text.
- [ ] **Step 4: Update the enumerations** (`appearance.ts` `HEAD_SCRIPT`, then recompute `HEAD_SCRIPT_CSP_HASH` with the repo's own test or script; `tailwind.css`; `appnav.css`) and the docs files listed above.
- [ ] **Step 5: Regenerate** with `pnpm tokens && pnpm references && pnpm references:lock`. Expected: diffs only add cyan and superbench.
- [ ] **Step 6: Run** `pnpm verify`. Expected: PASS, with contrast tests passing in every look for cyan.
- [ ] **Step 7: Commit** (`vibekit: SuperBench (Cyan) joins the products, as an eleventh accent`), push a branch, and open a forge PR. Review and merge on green CI.
- [ ] **Step 8: Release prep.** `sh .github/release/prep-release.sh v0.0.19` in a release PR, merged on green CI. Then ask the operator to confirm the `v0.0.19` tag, push it, and confirm npm shows `@superjackfruitlabs/vibekit-ui@0.0.19`.

### Task 2: frontdoor puts SuperBench in the frame

**Files:**
- Create: `packages/frame/src/brand/superbench.svg` (copy of `mark-color.svg`; keep its `role="img"`/`aria-label` pattern like `supertoolkit.svg`)
- Modify: `packages/frame/src/data/products.ts` (the `Slug` union; `make('superbench', 'SuperBench', <color>, <hi>, 'superbench.dev', 'superbench', 'quarter', 'Bench', "One task's checkout, kept warm and checkpointed.", 4510)`; append to `ORDER` after `supertoolkit`; `docs: null` until the docs site exists)
- Modify: `packages/frame/src/client/backdrop-core.js` (a `superbench()` backdrop: work held steady and kept warm, in the style of its neighbours, with `prefers-reduced-motion` respected as theirs do)
- Modify: `packages/frame/src/client/{doorway,jack,micro}.ts`, `packages/frame/src/components/{Claims,ProductHeader,SuiteBar}.astro`, `packages/frame/src/components/shadow.ts`, `packages/frame/src/components/styles/{bar,footer}.css`
- Modify: `packages/frame/src/data/ledger.snapshot.json`, `packages/frame/scripts/{build-ledger,refresh-releases}.mjs`
- Modify: `scripts/og/render.mjs` ("Ten tools"), `worker/src/{docs,handler,releases}.ts`, `worker/src/ledger.json`, `worker/wrangler.jsonc` (`superbench.dev/captain/*` route, matching siblings)
- Modify: `packages/frame/package.json` (vibekit-ui `^0.0.19`), `package-lock.json`, `packages/frame/README.md`
- Modify: `packages/frame/src/data/products.ts`: remove the `PRODUCTS.supertoolkit.licence = 'Apache-2.0'` override, because SuperToolkit is MIT since supertoolkit#56; `products.test.ts` 'licenses each product as its repo does' follows
- Test: `packages/frame/test/products.test.ts`, `packages/frame/test/colours.test.ts`, `packages/frame/test/lab.test.ts`, `packages/frame/e2e/{behaviour,render}.spec.ts`, `worker/test/lab.test.ts`

**Interfaces:**
- Consumes: vibekit-ui 0.0.19 (`PRODUCT_ACCENTS.superbench`, `ACCENT_HUES.cyan`).
- Produces: `PRODUCTS.superbench` (`street: 'quarter'`, `licence: 'MIT'`, `port: 4510`), `<sjl-*>` elements that render SuperBench. Released as `@superjackfruitlabs/vibekit-frame` 0.0.17.

- [ ] **Step 1: Update the failing tests.**
  - `products.test.ts`: `ORDER` ends `[..., 'supertoolkit', 'superbench']` and names end `'SuperBench'`. Add `expect(PRODUCTS.superbench.street).toBe('quarter')`, `expect(PRODUCTS.superbench.licence).toBe('MIT')`, and an assertion that SuperBench has no docs link.
  - `behaviour.spec.ts`: `.grp-quarter a.tile` has count 6, and `a.tile svg.logo` has count 10.
  - `colours.test.ts`: the mark hue must match cyan (173 ± its tolerance).
  - Lab copy expects "Ten".
- [ ] **Step 2: Run** `npm test -w packages/frame` and `npx playwright test -c packages/frame` (where there's disk). Expected: FAIL on superbench.
- [ ] **Step 3: Implement** the files above. `color`/`hi` are the mark's base and highlight hexes, checked by `colours.test.ts`. Update every "nine" or "9" product count you find with `git grep -nE '[Nn]ine|\b9\b'`.
- [ ] **Step 4: Run** the repo's full CI commands, read from `.forgejo/workflows/ci.yml`. Expected: PASS.
- [ ] **Step 5: Commit** (`0.0.17: SuperBench joins the frame`), open a forge PR, and merge on green CI. Then release-prep `v0.0.17`, get operator confirmation, tag, and confirm npm has `@superjackfruitlabs/vibekit-frame@0.0.17`.

### Task 3: superbench `landing/` (follow until release)

**Files:**
- Create: `landing/` copied from superdispatch `landing/`:
  - `astro.config.mjs` (`site: 'https://superbench.dev'`)
  - `package.json` (name `@superbench/landing`, dev port 4510, vibekit-frame `^0.0.17`, vibekit-ui `^0.0.19`)
  - `src/accent.ts`
  - `src/pages/{index,404}.astro`, `src/components/ShareMeta.astro`
  - `public/{favicon.svg,llms.txt,og.png,robots.txt}`
  - `scripts/check-frame.mjs`, `wrangler.jsonc` (Worker `superbench-site`; `custom_domain` routes `superbench.dev`, `www.superbench.dev`)
  - `README.md`
- Create: `landing/scripts/check-copy.mjs`
- Modify: `.forgejo/workflows/ci.yml` (a `landing` build job on PRs touching `landing/**`), `AGENTS.md` (production deployment wording, as the spec says)
- Do **not** add `.github/workflows/sites.yml` here. That comes in Task 5.

**Interfaces:**
- Consumes: vibekit-frame 0.0.17 and vibekit-ui 0.0.19.
- Produces: `landing/dist/` and the Worker config for Task 5's deploy.

- [ ] **Step 1: Write the failing check** `landing/scripts/check-copy.mjs`. It reads `dist/index.html` and fails when:
  - the text is missing "Experimental" or "not released yet", or a link to `https://github.com/SuperJackfruitLabs/superbench`;
  - the text contains, case-insensitive, `production-ready`, `secure isolation`, `npm install`, `go install`, `curl `, or a `v\d+\.\d+\.\d+` version;
  - it contains a local name from `guild|workbench|foundry|ashram|infra`.

  Wire it into `"build": "astro build && node scripts/check-frame.mjs && node scripts/check-copy.mjs"`.
- [ ] **Step 2: Run** `npm run build` in `landing/`. Expected: FAIL (no page yet).
- [ ] **Step 3: Build the page** following the spec's outline, using only claims marked verified or limited in Task 6's ledger: hero "One task, one bench.", the problem, how it works, standalone first, what's not done yet, and follow along. `favicon.svg` is `svg/mark-flat.svg` (compact, solid), and the OG image comes from frontdoor's renderer.
- [ ] **Step 4: Run** `npm run build`. Expected: `check-frame: ok` and `check-copy: ok`. Open `dist/index.html` locally and check all four vibes, light and dark.
- [ ] **Step 5: Commit** (`landing: SuperBench, follow until release`), open a forge PR, and merge on green CI. Nothing deploys yet.

### Task 4: superjackfruit.com lists SuperBench

**Files:**
- Modify: `src/data/suite.ts` (a SuperBench entry after SuperToolkit, as `ca775d1` did), `public/brand/superbench.svg`
- Modify: `public/_redirects`, `public/llms.txt`, `public/og.png`, `src/components/Seo.astro`, `src/data/projects.ts`, `src/layouts/{Layout,Site,Village}.astro`, `src/pages/index.astro`, `src/pages/village.astro` (ten tools), as `bd37aa2` did
- Modify: `src/scripts/village-scene.ts`, `src/scripts/village/pool.ts` (house 10 in the quarter, key 10), as `f05334b` did

**Interfaces:**
- Consumes: the mark SVG, and `https://superbench.dev` as the product URL.

- [ ] **Step 1:** Run `git grep -nE '[Nn]ine tools|\b9\b'` and list every count site. These are the assertions to update.
- [ ] **Step 2:** Add the entry, the mark, the copy counts and the village house and key, following the three commits named above.
- [ ] **Step 3:** Run the repo's CI commands, read from its workflow, and the village page locally. Expected: 10 houses; key 10 focuses SuperBench.
- [ ] **Step 4:** Commit (`suite: SuperBench joins the street`), open a PR against `master`, and merge on green CI.

### Task 5: deploy the landing site once `superbench.dev` is in Cloudflare

**Files:**
- Create: `.github/workflows/sites.yml`, copied from superdispatch's. It runs only on the GitHub mirror, with environment `websites`, path filter `landing/**` plus the workflow, and calls `scripts/deploy-sites.sh landing`.
- Create: `scripts/deploy-sites.sh`, landing only for now.

- [ ] **Step 1:** Confirm the operator has added `superbench.dev` to the Cloudflare account. Check with `dig +short NS superbench.dev`: the answer should be Cloudflare's nameservers.
- [ ] **Step 2:** Create the `websites` environment on `SuperJackfruitLabs/superbench`, with var `CLOUDFLARE_ACCOUNT_ID` and secret `CLOUDFLARE_API_TOKEN` (the same values as SuperDispatch's). If setting the secret is blocked for the agent, give the operator the one `gh secret set` command.
- [ ] **Step 3:** Commit the workflow and script, open a forge PR, and merge on green CI. Then watch the mirror's `sites` run: it must succeed.
- [ ] **Step 4:** Verify. `curl -sI https://superbench.dev` and `https://www.superbench.dev` should both answer 200 from the Worker. The page should show the frame and the "not released yet" line.

### Task 6: sjl-marketing claims for SuperBench

**Files:**
- Modify: `foundation/products.md` (a `## superbench` section with SB-NN entries), `foundation/repositories.md`
- Add: the existing `brand/product-logos/translucent-v2/products/superbench/` files, `USAGE.txt`, `manifest.json` and `checksums.sha256`. These are already in the working tree; commit them only after confirming nobody else is mid-change on them.

- [ ] **Step 1:** For each claim Task 3's page makes, add an entry with its evidence (a public source path plus a test name or recorded run) and a status of `verified`, `limited` (with its caveat) or `roadmap`.
- [ ] **Step 2:** Run `./scripts/check-content.sh`. Expected: PASS.
- [ ] **Step 3:** Commit (`claims: superbench, with its caveats attached`), open a PR, and merge on green CI. Task 3's copy must match these statuses before Task 3 merges, so run Task 6 before Task 3's merge.
