#!/usr/bin/env bash
# Build superbench.dev and deploy it as a Cloudflare Worker with static assets.
#
# CI runs it on every merge to main that touches the site (.github/workflows/sites.yml), with
# the organisation's CLOUDFLARE_API_TOKEN. It also runs by hand with a `wrangler login`.
# landing/wrangler.jsonc names the Worker and its custom domains, so this attaches the domains too.
#
# --config is explicit and wrangler runs from a scratch directory: in a site directory without a
# config, wrangler's project detection scaffolds an adapter into the site and rewrites
# astro.config.mjs. Do not simplify it away.
#
# Usage: scripts/deploy-sites.sh
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

command -v wrangler >/dev/null 2>&1 || {
  echo "wrangler is not installed. npm i -g wrangler, then wrangler login." >&2; exit 1; }

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

echo "==> building landing"
# npm run build is astro build plus the frame and copy checks over dist; a failing check stops
# the deploy. --force: a stale content-layer cache can report an empty collection.
(cd "$root/landing" && npm ci --silent --no-audit --no-fund && npx astro build --force \
  && node scripts/check-frame.mjs && node scripts/check-copy.mjs)
echo "==> deploying landing"
(cd "$scratch" && wrangler deploy --config "$root/landing/wrangler.jsonc" --cwd "$root/landing")
echo "==> https://superbench.dev"

echo
echo "The page claims only what the claims ledger supports. If the page and the repository"
echo "disagree, the page is wrong: check before telling anyone it is live."
