#!/usr/bin/env bash
# Opt-in direct-root Linux drill: frozen BENCH BENCHD QUOTA_HELPER.
# Creates only its own private mount namespace, XFS image and cgroup subtree.
set -euo pipefail
if [[ ${1:-} != --inside-owned-mount-namespace ]]; then
 [[ $# -eq 3 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
 exec unshare --mount -- bash "$0" --inside-owned-mount-namespace "$@"
fi
shift
[[ $# -eq 3 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
bench=$(realpath "$1")
benchd=$(realpath "$2")
quota_helper=$(realpath "$3")
[[ -x "$bench" && -x "$benchd" && -x "$quota_helper" ]]
for tool in mkfs.xfs mount umount mountpoint fallocate xfs_io python3 git setsid; do command -v "$tool" >/dev/null; done
mount --make-rprivate /
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
trial=$(mktemp -d /var/tmp/superbench-workspace-api.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-workspace-api-${trial##*.}"
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
python3 "$(dirname "$0")/test-workspace-api.py" --bench "$bench" --benchd "$benchd" --quota-helper "$quota_helper" --trial "$trial" --cgroup-root "$cgroups"
