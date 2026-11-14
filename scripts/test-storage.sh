#!/usr/bin/env bash
# Root Linux inside unshare --mount; arguments: bench, quota helper, Python test.
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-storage.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-storage-${trial##*.}"
mkdir "$trial/data" "$trial/budget" "$cgroups"
cleanup(){
 for scope in "$cgroups"/*;do if [[ -d "$scope" ]];then echo 1 > "$scope/cgroup.kill" || return;for _ in {1..100};do if grep -q '^populated 0$' "$scope/cgroup.events";then break;fi;sleep .05;done;rmdir "$scope" || return;fi;done
 rmdir "$cgroups" || return
 for path in "$trial/data" "$trial/budget";do if mountpoint -q "$path";then umount "$path" || return;fi;done
 rm -rf -- "$trial"
}
trap cleanup EXIT
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 3221225472 ]]
for name in data budget;do fallocate -l 512M "$trial/$name.img";mkfs.xfs -q -m reflink=1 "$trial/$name.img";done
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
mount -o loop,nodev,nosuid "$trial/budget.img" "$trial/budget"
python3 -u "$3" "$trial" "$1" "$2" "$cgroups"
