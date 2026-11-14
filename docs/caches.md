# Repository cache settings

Repository setup and supervised commands select cache locations from
`.bench/config.toml`. Existing configurations keep shared cache defaults. Each
boolean selects sharing: `true` uses the host's shared store, and `false` keeps
that cache inside the checkout. It does not disable the package manager's cache.

```toml
[setup]
once = "pnpm install --frozen-lockfile"
each = "pnpm install --offline --frozen-lockfile"

[caches]
pnpm = true
npm = true
bun = true
playwright = false
go = false
```

All five settings default to `true`. Unknown keys, path strings, nonboolean
values and duplicate keys are rejected by the bounded TOML reader. The repository
cannot supply an arbitrary cache directory through this table. `[quota]` and
`[archive]` repository settings remain unsupported rather than silently ignored.

## Locations and command behavior

| Setting | Shared location under data | Checkout-local location | Environment |
| --- | --- | --- | --- |
| `pnpm` | `store/pnpm` | `.bench/cache/pnpm` | `npm_config_store_dir`; clone imports |
| `npm` | `store/npm` | `.bench/cache/npm` | `npm_config_cache` |
| `bun` | `store/bun` | `.bench/cache/bun` | `BUN_INSTALL_CACHE_DIR` |
| `playwright` | `store/playwright` | `.bench/cache/playwright` | `PLAYWRIGHT_BROWSERS_PATH` |
| `go` | `store/go-build`, `store/go-mod` | `.bench/cache/go-build`, `.bench/cache/go-mod` | `GOCACHE`, `GOMODCACHE` |

Setup hooks and the main command inherit these paths. pnpm hooks receive the
store setting and `npm_config_package_import_method=clone`, matching frozen
setup's explicit store/import arguments. Recognized inherited cache overrides,
including uppercase npm aliases, are replaced; the lease token is still stripped.
Frozen pnpm/npm setup uses the same selected paths. An ordinary shell opened
outside `bench run` does not inherit this environment merely by obtaining a
cooperative lease. Use managed setup/run for these defaults.

The engine reads one configuration inside each stage's acquisition transaction
for both its command plan and cache selection. Preparation handoffs validate
that same parsed configuration; successful setup still rechecks the file after
fencing/sealing before recording its receipt. Changing any configuration bytes,
including cache settings, invalidates once reuse for the previous configuration.
Changing selection does not delete the old cache.

## Checkout-local caches and quotas

When any cache is checkout-local, the repository should ignore its generated
files explicitly:

```gitignore
.bench/cache/
```

SuperBench never edits `.gitignore`, hides tracked changes or deletes untracked
cache files to make a checkpoint pass. Without the ignore rule, generated output
can block checkpoint/release and leave a stopped lease with its files preserved.
Review and commit the intended ignore configuration using that held lease.

On managed XFS hosts, checkout-local caches inherit the bench quota. Shared
caches remain under the separately capped store project. Selecting a private
cache cannot increase either cap or change root allocation policy. Existing
symlinks and nondirectory cache prefixes are refused before launching the command;
they are never replaced or removed. Tracked files in `.bench/cache/` also
refuse private-cache execution; that namespace cannot silently become a cache
over existing source files. Cache directories themselves are generated
artifacts, not authoritative lease or setup-receipt state.

These settings configure defaults for trusted commands. Explicit command flags
can override tool configuration, and a process sharing the same uid can access
other directories. This is not filesystem or tenant isolation. Root/host policy
and the cooperative execution boundary remain unchanged.

## Compatibility and verification

State schema 10 and the local socket request shapes are unchanged. A published
producer without these settings rejects the new cache keys before running hooks
or the requested command; it must not silently fall back to shared storage. No
suite consumer deployment is implied by this repository configuration change.

Required checks passed on Linux and macOS. The actual Linux daemon/CLI drill
uses pnpm's reported store path and import setting, checks cross-bench shared
reuse, per-bench cache persistence, once invalidation, parent override removal,
linked-prefix refusal and unignored-output preservation. It probes Playwright's
selected directory with fixture files; it does not install or launch a browser.
The XFS drill performs a real frozen pnpm install into a private store, fills that
cache to its bench cap and confirms the shared store stays writable. The
unprivileged systemd installation also verifies private settings through setup,
run, read-only archive and reviewed reap.

Recorded results: [caches-output.txt](caches-output.txt).
