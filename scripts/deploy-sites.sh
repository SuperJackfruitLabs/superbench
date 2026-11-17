#!/usr/bin/env bash
# Build superbench.dev and docs.superbench.dev and deploy each as a Cloudflare Worker with static
# assets.
#
# CI runs it on every merge to main that touches either site (.github/workflows/sites.yml), with
# the organisation's CLOUDFLARE_API_TOKEN. It also runs by hand with a `wrangler login`.
# landing/wrangler.jsonc and docs-site/wrangler.jsonc name each Worker and its custom domains, so
# this attaches the domains too.
#
# --config is explicit and wrangler runs from a scratch directory: in a site directory without a
# config, wrangler's project detection scaffolds an adapter into the site and rewrites
# astro.config.mjs. Do not simplify it away.
#
# Usage: scripts/deploy-sites.sh [landing|docs|all]   (default: all)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
which="${1:-all}"
case "$which" in landing|docs|all) ;; *) echo "usage: $0 [landing|docs|all]" >&2; exit 2 ;; esac

command -v wrangler >/dev/null 2>&1 || {
  echo "wrangler is not installed. npm i -g wrangler, then wrangler login." >&2; exit 1; }

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

ship() { # <dir> <url>
  echo "==> building $1"
  # --force: a stale content-layer cache can report an empty collection. A failing check stops
  # the deploy.
  case "$1" in
    # astro build plus the frame and copy checks over dist.
    landing) (cd "$root/$1" && npm ci --silent --no-audit --no-fund && npx astro build --force \
      && node scripts/check-frame.mjs && node scripts/check-copy.mjs) ;;
    # The claims against the Go source and the honesty rules before the build; the banner, copy
    # and links over dist after it.
    docs-site) (cd "$root/$1" && npm ci --silent --no-audit --no-fund && node scripts/check-claims.mjs \
      && npx astro build --force && node scripts/check-copy.mjs && node scripts/check-links.mjs) ;;
  esac
  echo "==> deploying $1"
  (cd "$scratch" && wrangler deploy --config "$root/$1/wrangler.jsonc" --cwd "$root/$1")
  echo "==> $2"
}

[ "$which" = docs ] || ship landing "https://superbench.dev"
[ "$which" = landing ] || ship docs-site "https://docs.superbench.dev"

echo
echo "The sites claim only what the claims ledger and the current source support. If a page and"
echo "the repository disagree, the page is wrong: check before telling anyone it is live."
