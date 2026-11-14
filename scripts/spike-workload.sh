#!/usr/bin/env bash
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-workload.XXXXXX)
cleanup(){ cd /; if mountpoint -q "$trial/mnt";then umount "$trial/mnt" || return;fi;rm -rf -- "$trial"; }
trap cleanup EXIT
mkdir "$trial/mnt"
# Reject low-space hosts before allocating a bounded 3 GiB image.
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 6442450944 ]]
fallocate -l 3G "$trial/image"
mkfs.xfs -q -m reflink=1 "$trial/image"
mount -o loop,prjquota,nodev,nosuid "$trial/image" "$trial/mnt"
root="$trial/mnt"
mkdir "$root/a" "$root/b" "$root/cache"
tar -xf - -C "$root/a"
cp -a "$root/a/." "$root/b/"
export npm_config_cache="$root/cache" ASTRO_TELEMETRY_DISABLED=1
export npm_config_audit=false npm_config_fund=false
measure(){
 label=$1; shift
 start=$(date +%s%N)
 if ! timeout 240 "$@" > "$trial/$label.log" 2>&1;then tail -60 "$trial/$label.log";return 1;fi
 end=$(date +%s%N)
 echo "$label milliseconds=$(((end-start)/1000000))"
 tail -4 "$trial/$label.log"
 sync -f "$root"
 df --output=used -B1 "$root" | tail -1 | xargs echo "$label filesystem_used_bytes="
}
cd "$root/a"
measure cold_install npm ci
measure cold_build npm run build
measure warm_handoff_check node --input-type=module -e "import('astro').then(() => console.log('existing dependencies usable'))"
# Real warm handoff keeps node_modules; no actual reinstall.
measure warm_build npm run build
cd "$root/b"
measure second_checkout_install npm ci --offline
measure second_checkout_build npm run build
printf '\nFINAL ALLOCATIONS\n'
du -s -B1 "$root/a" "$root/b" "$root/cache"
df -B1 "$root"
echo 'PASS: real package-lock installs and builds; cleanup follows'
