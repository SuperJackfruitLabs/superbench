# SuperBench joins the suite: identity and landing site

Date: 2026-11-16. Status: approved by the operator, 2026-11-16.

## Intent

SuperBench is going public as a suite product in its own right. The GitHub mirror
(`SuperJackfruitLabs/superbench`) has been public since 2026-11-16, and the operator is buying
`superbench.dev`. This work gives it the suite's visual identity and a landing site, so it
arrives looking like SuperDispatch, SuperExchange and SuperToolkit, which joined the same way.

The site's job before the first release is **follow, not try**. It explains what SuperBench is
and why it matters, says plainly that it is not released yet, and gives developers ways to
follow it: watch or star on GitHub, and the GitHub releases feed. It invites no installs.

## Goals

1. SuperBench is a first-class product in vibekit, with its own accent `cyan` at hue 173,
   tuned per look by vibekit's own recipe.
2. SuperBench is in frontdoor's shared frame: its mark, its backdrop, the doorway, the suite
   bar, the footer, the ledger and the OG card.
3. A `landing/` site in the superbench repo, built and deployed like SuperDispatch's, at
   `superbench.dev` (apex and `www`).
4. SuperBench is listed on superjackfruit.com, like the other products.

## Non-goals

- No docs site yet. `docs.superbench.dev` waits for the first release, so it never documents
  behaviour that is still changing.
- No install instructions, binaries, early-access sign-up or waitlist.
- No change to SuperBench's code, CLI or daemon.
- No marketing campaign. The site goes live; announcing it is a separate decision.

## Anti-goals: these would make it a failure even if it works

- **Claiming more than is true.** The site must not present SuperBench as released, production
  ready, or as isolating untrusted code. Today it is an experimental single-host prototype
  (Go CLI plus a local Unix-socket daemon; Linux supervised runs; the separate payload user is
  still being wired). Every capability statement must be backed by public source, and stated
  limits stay in.
- **Hand-picked colours.** The accent comes from vibekit's recipe and tokens, never hard-coded
  in the site, which is how SuperDispatch's `landing/src/accent.ts` reads it.
- **Local names.** No agent, host or team names in anything shipped. The product word is
  "workspace".
- **Suite dependency in the product.** The site is suite-shaped. SuperBench itself still
  never depends on suite products (charter conditions 1 and 3).

## Decided (operator, 2026-11-16)

- **License: MIT** (see below).
- **Public now**, with the domain `superbench.dev` being bought by the operator.
- **Colour: cyan, hue 173**, between jade (155, SuperWitness) and teal (192, SuperDispatch). It
  was chosen from three candidates shown in all eight looks.
- **Mark:** the "translucent fold" bench in sjl-marketing,
  `brand/product-logos/translucent-v2/products/superbench/`. Near-square, end-on, with a sea-green
  `#038068` base and a brighter cyan translucent top. Masters: `mark-color.svg`,
  `mark-flat.svg`, `mark-black.svg`, `mark-white.svg` and `mark-current.svg`, plus PNGs from 16
  to 2048 px.
- **Approach A:** full suite parity now, landing page only; docs at the first release.

## Approaches considered

- **A. Full parity now, landing only (chosen).** vibekit, then frontdoor, then landing, then the
  suite site. The most work up front, but nothing gets redone, and the vibe rule holds.
- B. A standalone page with hand-written CSS now, suite integration later. Rejected: it breaks
  "shipped UI carries the product vibe", and the vibekit and frontdoor work would be done twice.
- C. A section on superjackfruit.com until release. Rejected: the domain is being bought, and
  every other product has its own site.

## The work, in order (producer before consumer)

Each step is one PR in its repo, merged and released before the next one consumes it. The
recent products show the exact file set: vibekit `905dc5b` (SuperToolkit) and `f562ee9`;
frontdoor `773a977` and `0d98383`; website `ca775d1`, `bd37aa2` and `f05334b`; SuperDispatch
landing `cb94bef`, `1c22aea` and `37c03e7`.

1. **vibekit** (forge primary; release through the GitHub mirror).
   - Add `cyan` to `ACCENTS`, with `ACCENT_HUES.cyan = 173`, a label and a description.
   - Add `superbench` to `PRODUCTS` and `PRODUCT_NAMES`, with `PRODUCT_ACCENTS.superbench = 'cyan'`.
   - Generate `ACCENT_PALETTE.cyan` for all eight looks with `tokens/source/recipe.ts`
     (`accentRecipe`). It searches lightness until the accent and text meet the contrast bars
     (≥4.6 against the background, ≥3.1 against the surface). Use the recipe's output, not the
     approximations on the candidates page.
   - Add a product tile in `tokens/source/shared.ts`.
   - Update everything that enumerates products or accents:
     - `appearance.ts` `HEAD_SCRIPT`, and its CSP hash;
     - `tailwind.css` `--color-superbench`;
     - the appnav tile line;
     - the DTCG looks;
     - the appearance picker references and their lock;
     - tests;
     - `llms.txt`, `docs/non-svelte.md` and the accent-tokens spec;
     - the CHANGELOG.
   - Run `pnpm verify`.
   - Check: cyan stays outside the signal band (4°–86°), and the contrast tests pass in every look.
   - Release the next `vibekit-ui` patch (0.0.19).
2. **frontdoor** (`suite-frame`).
   - Add `packages/frame/src/brand/superbench.svg`, the mark from sjl-marketing, recoloured
     only if `test/colours.test.ts` requires the hue to match vibekit.
   - Add a `make('superbench', 'SuperBench', …, 'superbench.dev', …, port 4510)` entry in `products.ts`.
   - Add a SuperBench backdrop in `backdrop-core.js`.
   - Add SuperBench everywhere else products are listed:
     - the doorway `MARKS`, `jack.ts` and `micro.ts`;
     - the Claims, ProductHeader and SuiteBar components, and the bar and footer styles;
     - the ledger snapshot and scripts;
     - the OG renderer, where the headline count changes from "Nine tools" to "Ten tools";
     - the worker docs, handler, releases and ledger, and its wrangler route;
     - tests and e2e.
   - The worker's docs link stays off until the docs site exists.
   - Bump to vibekit-ui 0.0.19, then release `vibekit-frame` 0.0.17.
3. **superbench `landing/`**, copied from SuperDispatch's landing.
   - Astro 7, sitemap, vibekit-frame 0.0.17 and vibekit-ui 0.0.19, dev port 4510.
   - `src/accent.ts` reads the accent from vibekit.
   - Files: `index` and `404` pages, `ShareMeta`, an `og.png` from frontdoor's OG renderer,
     `favicon.svg` from the mark, `llms.txt`, `robots.txt`, and `scripts/check-frame.mjs`.
   - `wrangler.jsonc` runs Worker `superbench-site` with `custom_domain` routes for
     `superbench.dev` and `www.superbench.dev`.
   - `.github/workflows/sites.yml` (GitHub mirror only, environment `websites`,
     `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID`) runs `scripts/deploy-sites.sh landing`.
   - Forge CI builds `landing/` on PRs that touch it.
   - Scenes: SuperDispatch's landing uses scroll-scrubbed 3D scenes. SuperBench's first landing
     page can use static scenes or none; scenes play one step per scroll position, never side by
     side or on autoplay.
4. **superjackfruit.com** (`super-jackfruit-website`, default branch `master`).
   - Add SuperBench to `src/data/suite.ts`, with `public/brand/superbench.svg`.
   - Update the counts and copy ("ten tools"), `llms.txt`, the OG image and the SEO.
   - In the village scene, add a house and a key 10, following SuperToolkit's three commits.
5. **sjl-marketing claims.** Add a `## superbench` section to `foundation/products.md`, with each
   capability the landing page states and its status:
   - `verified` when public source plus a test or run proves it;
   - `limited` with its caveat;
   - `roadmap` for what's still coming.

   The landing copy uses only `verified` and `limited` claims. Commit the SuperBench mark files
   that are already in that working tree.

## Landing page content (follow, not try)

1. **Hero:** "One task, one bench." A bench is one task's git worktree, kept warm, fenced and
   checkpointed between agents' turns. Status line: "Experimental · not released yet".
   Calls to action: "Watch on GitHub" and "Release feed".
2. **The problem** it solves: per-card checkouts multiply, cleanup never happens, and a
   handoff between agents loses work. Tell this generically; it is grounded in a real loss
   (a host's disk filled from per-card checkouts), but name no host.
3. **How it works** (verified claims only):
   - create, lease, checkpoint, release;
   - checkpoints push the exact HEAD and are confirmed on the remote; there are never invented
     commits or force pushes;
   - quotas per bench on XFS;
   - shared mirrors and caches;
   - archive and reap only after review.
4. **Standalone first:** a CLI plus a local daemon, with no TCP listener and no dependency on
   other products. The suite calls SuperBench; SuperBench never calls the suite.
5. **What's not done yet**, stated plainly:
   - payload isolation under a separate user is in progress;
   - single host only;
   - no release binaries;
   - Linux for supervised runs.
6. **Follow along:** GitHub watch, star and releases, plus the suite frame's footer and links.

## Constraints

- One PR per repo, one commit per item, and producer first. Run each repo's CI locally before
  pushing; check PR state before every push. vibekit, frontdoor and superbench are
  forge-primary: push to forge, and GitHub releases and deploys from the mirror.
- Only the operator pushes production tags. vibekit and frontdoor tags publish npm packages.
  Read each `release.yml` and confirm with the operator before tagging, per the standing
  release rule.
- Domain: the landing page deploys only once `superbench.dev` is in the Cloudflare account. The
  Worker's `custom_domain` routes attach it; nothing is done by hand in the dashboard.
- Mac disk is tight (the guard needs 2 GiB free), so build vibekit and frontdoor where there's
  room.

## Calls made in this design (check these)

- **Street: the quarter ("Where agents live")**, beside SuperDispatch, AgentPod and Salaam, not
  the main street ("Your work"). SuperBench is infrastructure that agents work inside. This
  decides frontdoor's footer group (the e2e expects 5 quarter tiles), the product order, and
  where the village house goes.
- **Repo rules:** superbench `AGENTS.md` says "No production deployment or tags". The landing PR
  rewords it: the product still has no production deployment, but its landing site deploys from
  the GitHub mirror like every other product's.
- **Deploy setup:** the public GitHub mirror is done, with the guard ruleset, and workspace#7
  drops the `private` tag. The GitHub `websites` environment, with `CLOUDFLARE_API_TOKEN` and
  `CLOUDFLARE_ACCOUNT_ID` like SuperDispatch's, is created by Claude. If setting the secret is
  blocked for Claude, the operator runs one `gh secret set` command.
- **Domain timing:** the landing PR builds in forge CI but does not add `sites.yml`. A small
  follow-up PR adds the deploy workflow once `superbench.dev` is in the Cloudflare account, so
  no merge produces a red deploy.

## License (decided)

MIT, decided by the operator on 2026-11-16, as most of the suite. The LICENSE file lands in the
same PR as this document. frontdoor's product entry uses `licence: 'MIT'`. SuperToolkit gets MIT
in the same pass.

## Left to the builder

- The backdrop animation's idea: something that reads as "kept warm and held steady", in the
  style of the others.
- The exact landing copy, within the claims rule.
- Whether to use scenes on the first landing page.
- The OG card layout, following the renderer's existing product cards.
