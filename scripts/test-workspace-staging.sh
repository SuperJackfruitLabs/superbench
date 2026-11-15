#!/usr/bin/env bash
# Disposable direct-root Linux drill: TEST_BINARY QUOTA_HELPER [SCHEMA13_CLI].
# Build with Go 1.26: go test -c -o /private/path/workspaceflow.test ./internal/workspaceflow
set -euo pipefail
if [[ ${1:-} != --inside-owned-mount-namespace ]]; then
 [[ $# -ge 2 && $# -le 3 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
 exec unshare --mount -- bash "$0" --inside-owned-mount-namespace "$@"
fi
shift
[[ $# -ge 2 && $# -le 3 && $EUID -eq 0 && -z ${SUDO_UID:-} ]]
test_binary=$(realpath "$1")
quota_helper=$(realpath "$2")
[[ -x "$test_binary" && -x "$quota_helper" ]]
old_cli=''
if [[ $# -eq 3 ]];then old_cli=$(realpath "$3");[[ -x "$old_cli" ]];fi
for tool in mkfs.xfs mount umount mountpoint fallocate xfs_io;do command -v "$tool" >/dev/null;done
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-workspace.XXXXXX)
cgroups="/sys/fs/cgroup/superbench-workspace-${trial##*.}"
cgroups_created=false
cleanup(){
 if [[ "$cgroups_created" == true ]];then
 for scope in "$cgroups"/*;do
  if [[ -d "$scope" ]];then
   echo 1 > "$scope/cgroup.kill" || return
   for _ in {1..100};do if grep -q '^populated 0$' "$scope/cgroup.events";then break;fi;sleep .05;done
   rmdir "$scope" || return
  fi
 done
 if [[ -d "$cgroups" ]];then rmdir "$cgroups" || return;fi
 fi
 if mountpoint -q "$trial/data";then umount "$trial/data" || return;fi
 rm -rf -- "$trial"
}
trap cleanup EXIT
[[ $(df --output=avail -B1 /var/tmp | tail -1) -gt 2147483648 ]]
mkdir "$trial/data"
mkdir "$cgroups"
cgroups_created=true
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
SUPERBENCH_WORKSPACE_MANAGED_TRIAL="$trial" \
SUPERBENCH_WORKSPACE_QUOTA_HELPER="$quota_helper" \
SUPERBENCH_WORKSPACE_CGROUP_ROOT="$cgroups" \
SUPERBENCH_WORKSPACE_OLD_CLI="$old_cli" \
 "$test_binary" -test.run '^TestManagedWorkspace(AggregateQuota|PreparationLifecycle|ExecutionLifecycle|RecoveryLifecycle)$' -test.v -test.timeout 4m
