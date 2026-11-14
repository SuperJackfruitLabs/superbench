#!/usr/bin/env bash
# Disposable root mount namespace: new CLI/helper, Python drill, old CLI/helper.
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-repository-quota.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-repository-quota-${trial##*.}"
mkdir "$trial/data" "$cgroups"
cleanup(){
 for scope in "$cgroups"/*;do
  if [[ -d "$scope" ]];then
   echo 1 > "$scope/cgroup.kill" || return
   for _ in {1..100};do if grep -q '^populated 0$' "$scope/cgroup.events";then break;fi;sleep .05;done
   rmdir "$scope" || return
  fi
 done
 rmdir "$cgroups" || return
 if mountpoint -q "$trial/data";then umount "$trial/data" || return;fi
 rm -rf -- "$trial"
}
trap cleanup EXIT
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
python3 -u "$3" "$trial" "$1" "$2" "$4" "$5" "$cgroups"
