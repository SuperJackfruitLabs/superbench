#!/usr/bin/env bash
#
# Build release archives and SHA256SUMS for the four SuperBench commands:
# bench, benchd, bench-quota and bench-payload, for Linux amd64 and arm64.
#
# The builds are reproducible, binary and archive both. CGO is off, so every
# binary is static (the managed installer requires bench-payload to be), and
# -trimpath removes local paths. The tag's version is the only thing stamped in.
# tar records each entry's mtime, uid, gid and names, and gzip writes a
# timestamp of its own; both are normalised so two runs give the same bytes.
#
# Output goes to dist/ by default, or to $OUT_DIR when set.
set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: scripts/release.sh <tag>   e.g. scripts/release.sh v0.0.1" >&2
  exit 2
fi
case "$TAG" in
  v*) ;;
  *) echo "tags are vX.Y.Z: pass v$TAG, not $TAG" >&2; exit 2 ;;
esac
VERSION="${TAG#v}"
# vX.Y.Z or vX.Y.Z-pre; also lets a scratch tag such as v0.0.1-test through.
case "$VERSION" in
  [0-9]*.[0-9]*.[0-9]*) ;;
  *) echo "unexpected tag '$TAG'; expected vX.Y.Z" >&2; exit 2 ;;
esac
case "$VERSION" in
  *[!0-9A-Za-z.-]*|*..*|.*|*.) echo "unexpected tag '$TAG'; expected vX.Y.Z" >&2; exit 2 ;;
esac

readonly EPOCH="202601010000"
readonly COMMANDS="bench benchd bench-quota bench-payload"
readonly PKG="forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/buildinfo"

# GNU tar (Linux, CI) and bsdtar (macOS) spell "pretend the owner is root" differently.
tar_ownership() {
  if tar --version 2>/dev/null | grep -qi "gnu tar"; then
    printf '%s' "--owner=0 --group=0 --numeric-owner --sort=name"
  else
    printf '%s' "--uid 0 --gid 0 --numeric-owner"
  fi
}

# archive <dir> <name> <file...>: a deterministic tar.gz of files in <dir>.
archive() {
  local dir="$1" name="$2"; shift 2
  local f
  for f in "$@"; do touch -t "$EPOCH" "$dir/$f"; done
  # shellcheck disable=SC2046  # the flags are intentionally word-split
  tar $(tar_ownership) -cf - -C "$dir" "$@" | gzip -n -9 > "$dir/$name"
}

if command -v sha256sum >/dev/null 2>&1; then
  SHA_CMD="sha256sum"
else
  SHA_CMD="shasum -a 256"
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${OUT_DIR:-$root/dist}"
rm -rf "$out"; mkdir -p "$out"
cd "$root"

echo "building superbench $VERSION"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
for arch in amd64 arm64; do
  printf '  linux/%-8s' "$arch"
  mkdir -p "$stage/$arch"
  for cmd in $COMMANDS; do
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
      go build -trimpath -ldflags "-s -w -X $PKG.Version=$VERSION" \
      -o "$stage/$arch/$cmd" "./cmd/$cmd"
  done
  # shellcheck disable=SC2086  # COMMANDS is a fixed word list
  archive "$stage/$arch" "superbench_linux_${arch}.tar.gz" $COMMANDS
  mv "$stage/$arch/superbench_linux_${arch}.tar.gz" "$out/"
  echo "ok"
done

# Verify what was built: linux, the right arch, CGO off, -trimpath. -X against a
# missing symbol or a const is silently ignored, so the stamp is checked below too.
for arch in amd64 arm64; do
  check="$stage/verify-$arch"; mkdir -p "$check"
  tar -C "$check" -xzf "$out/superbench_linux_${arch}.tar.gz"
  for cmd in $COMMANDS; do
    info="$(go version -m "$check/$cmd")"
    for want in "CGO_ENABLED=0" "GOOS=linux" "GOARCH=$arch" "-trimpath=true"; do
      case "$info" in
        *"$want"*) ;;
        *) echo "$cmd (linux/$arch) lacks '$want' in its build info" >&2; exit 1 ;;
      esac
    done
  done
done

# -trimpath drops -ldflags from the build info, so the stamp cannot be read back from the
# release binaries. Build bench and benchd for this host with the same flags and run them.
for cmd in bench benchd; do
  go build -trimpath -ldflags "-s -w -X $PKG.Version=$VERSION" -o "$stage/probe-$cmd" "./cmd/$cmd"
  got="$("$stage/probe-$cmd" version)"
  [ "$got" = "$cmd $VERSION" ] || { echo "$cmd reports '$got', not '$cmd $VERSION'" >&2; exit 1; }
done
echo "  version stamp verified: bench and benchd report $VERSION"
echo "  linux builds verified: CGO off, trimpath, right arch"

cd "$out"
$SHA_CMD superbench_*.tar.gz > SHA256SUMS
echo
cat SHA256SUMS

# Reproducibility is a claim this script makes, so it checks it: build everything a
# second time (the repeat run skips this step) and compare every archive.
if [ -z "${SUPERBENCH_RELEASE_REPEAT:-}" ]; then
  echo
  echo "checking reproducibility"
  again="$(mktemp -d)"
  OUT_DIR="$again" SUPERBENCH_RELEASE_REPEAT=1 bash "$root/scripts/release.sh" "$TAG" >/dev/null
  if cmp -s "$out/SHA256SUMS" "$again/SHA256SUMS"; then
    echo "  the same source produces the same archives"
    rm -rf "$again"
  else
    echo "  NOT reproducible; compare $out and $again" >&2
    exit 1
  fi
fi
