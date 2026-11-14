#!/usr/bin/env bash
# Disposable root mount namespace: new bench, helper, Python drill, old binaries.
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-archive.XXXXXX)
cleanup(){
 if mountpoint -q "$trial/data";then umount "$trial/data" || return;fi
 rm -rf -- "$trial"
}
trap cleanup EXIT
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
mkdir "$trial/data"
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
python3 -u "$3" "$trial" "$1" "$2" "$4" "$5"
