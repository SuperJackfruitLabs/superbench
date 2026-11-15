#!/usr/bin/env bash
# Opt-in direct-root Linux drill: frozen BENCH BENCHD QUOTA_HELPER [SCHEMA20_CLI].
# Creates only its own private mount namespace, XFS image and cgroup subtree.
set -euo pipefail
if [[ ${1:-} != --inside-owned-mount-namespace ]]; then
 [[ $# -ge 3 && $# -le 4 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
 exec unshare --mount -- bash "$0" --inside-owned-mount-namespace "$@"
fi
shift
[[ $# -ge 3 && $# -le 4 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
bench=$(realpath "$1")
benchd=$(realpath "$2")
quota_helper=$(realpath "$3")
old_cli="${SUPERBENCH_TRANSFER_PREVIOUS_CLI:-}"
if [[ $# -eq 4 ]]; then old_cli=$(realpath "$4"); fi
if [[ -n "$old_cli" ]]; then [[ -x "$old_cli" ]]; fi
[[ -x "$bench" && -x "$benchd" && -x "$quota_helper" ]]
for tool in mkfs.xfs mount umount mountpoint fallocate xfs_io python3 git setsid setpriv; do command -v "$tool" >/dev/null; done
mount --make-rprivate /
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
trial=$(mktemp -d /var/tmp/superbench-workspace-transfer.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-workspace-transfer-${trial##*.}"
cgroups_created=false
cleanup() {
 if [[ "$cgroups_created" == true ]]; then
  for scope in "$cgroups"/*; do
   if [[ -d "$scope" ]]; then
    echo 1 > "$scope/cgroup.kill" || return
    for _ in {1..100}; do
     if grep -q '^populated 0$' "$scope/cgroup.events"; then break; fi
     sleep .05
    done
    rmdir "$scope" || return
   fi
  done
  rmdir "$cgroups" || return
 fi
 if mountpoint -q "$trial/data"; then umount "$trial/data" || return; fi
 # Only the directory allocated above; never an installed service or shared root.
 rm -rf -- "$trial"
}
trap cleanup EXIT
mkdir "$trial/data"
mkdir "$cgroups"
cgroups_created=true
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
SUPERBENCH_TRANSFER_PREVIOUS_CLI="$old_cli" python3 "$(dirname "$0")/test-workspace-transfer.py" --bench "$bench" --benchd "$benchd" --quota-helper "$quota_helper" --trial "$trial" --cgroup-root "$cgroups"
