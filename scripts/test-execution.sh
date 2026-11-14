#!/usr/bin/env bash
# Root Linux staging test: binary and Python test paths as arguments.
set -euo pipefail
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-execution.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-test-${trial##*.}"
mkdir "$cgroups" "$trial/state"
mount -t tmpfs -o size=8m,mode=0700,nodev,nosuid tmpfs "$trial/state"
cleanup(){
 for scope in "$cgroups"/*;do
  if [[ -d "$scope" ]];then
   echo 1 > "$scope/cgroup.kill" || return
   for _ in {1..100};do if grep -q '^populated 0$' "$scope/cgroup.events";then break;fi;sleep .05;done
   rmdir "$scope" || return
  fi
 done
 rmdir "$cgroups" || return
 umount "$trial/state" || return
 rm -rf -- "$trial"
}
trap cleanup EXIT
args=("$2" "$trial" "$1" "$cgroups")
if [[ $# -gt 2 ]];then args+=("$3");fi
python3 -u "${args[@]}"
