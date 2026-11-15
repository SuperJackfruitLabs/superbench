#!/usr/bin/env bash
# Run via unshare --mount, passing the original mount namespace identity.
set -euo pipefail
[[ $# -eq 5 && $(id -u) -eq 0 ]]
helper=$1 testbin=$2 driver=$3 evidence=$4 original_namespace=$5
[[ $(readlink /proc/self/ns/mnt) != "$original_namespace" ]]
[[ "$evidence" == /var/tmp/superbench-verification/cancel-helper-* ]]
[[ $(df --output=avail -B1 /var/tmp | tail -1) -ge 2147483648 ]]
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-cancel-helper-XXXXXXXX)
[[ "$trial" == /var/tmp/superbench-cancel-helper-* && ! -L "$trial" ]]
chmod 0700 "$trial"
mkdir "$trial/data"
cleanup(){
 if [[ -e "$trial/PRESERVE-LIVE-HELPER" ]];then echo "preserved owned live-helper fixture $trial" >&2;return;fi
 if mountpoint -q "$trial/data";then umount "$trial/data" || return;fi
 [[ -d "$trial" && ! -L "$trial" && "$trial" == /var/tmp/superbench-cancel-helper-* ]] || return
 rm -rf -- "$trial"
 printf '%s\n' "$trial removed; private mount unmounted" > "$evidence/cleanup.txt"
}
trap cleanup EXIT
install -m 0755 "$helper" "$trial/bench-quota"
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
python3 -u "$driver" "$trial" "$testbin" "$evidence"
