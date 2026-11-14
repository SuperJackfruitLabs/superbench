#!/usr/bin/env bash
# Root, Linux, private namespace; arguments: Linux bench binary, Python experiment.
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-failures.XXXXXX)
cleanup(){ cd /;if mountpoint -q "$trial/mnt";then umount "$trial/mnt" || return;fi;rm -rf -- "$trial"; }
trap cleanup EXIT
mkdir "$trial/mnt"
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
fallocate -l 512M "$trial/image"
mkfs.xfs -q -m reflink=1 "$trial/image"
mount -o loop,prjquota,nodev,nosuid "$trial/image" "$trial/mnt"
mkdir "$trial/mnt/store" "$trial/mnt/state"
xfs_quota -x -c "project -s -p $trial/mnt/store 101" "$trial/mnt"
xfs_quota -x -c 'limit -p bhard=8m 101' "$trial/mnt"
xfs_quota -x -c "project -s -p $trial/mnt/state 102" "$trial/mnt"
xfs_quota -x -c 'limit -p bhard=16m 102' "$trial/mnt"
python3 -u "$2" "$trial" "$1"
