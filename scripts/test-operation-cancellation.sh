#!/usr/bin/env bash
# Invoke ONLY through a fresh unshare --mount --net, same PID namespace.
# Args: bench helper testbin driver API-fixture evidence original-mnt original-net manifest.
set -euo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 077
[[ $# -eq 9 && $(id -u) -eq 0 && -z ${SUDO_UID:-} ]]
bench=$1 helper=$2 testbin=$3 driver=$4 api_driver=$5 evidence=$6 original_mount=$7 original_network=$8 manifest=$9
[[ "$original_mount" =~ ^mnt:\[[0-9]+\]$ && "$original_network" =~ ^net:\[[0-9]+\]$ ]]
[[ $(readlink /proc/self/ns/mnt) != "$original_mount" && $(readlink /proc/self/ns/net) != "$original_network" ]]
[[ $(stat -Lc '%d:%i' /proc/self/ns/mnt) != $(stat -Lc '%d:%i' /proc/1/ns/mnt) ]]
[[ $(stat -Lc '%d:%i' /proc/self/ns/net) != $(stat -Lc '%d:%i' /proc/1/ns/net) ]]
[[ $(stat -Lc '%d:%i' /proc/self/ns/pid) == $(stat -Lc '%d:%i' /proc/1/ns/pid) ]]
[[ "$evidence" == /var/tmp/superbench-verification/cancel-managed-*/* ]]
# Exact protected binary/source/driver hashes precede every mount/cgroup effect.
python3 -I - "$bench" "$helper" "$testbin" "$driver" "$api_driver" "$evidence" "$manifest" "${BASH_SOURCE[0]}" <<'PY'
import hashlib,json,re,stat,sys
from pathlib import Path
bench,helper,testbin,driver,api,evidence,manifest,wrapper=map(Path,sys.argv[1:])
def protected(path,directory=False):
 assert path.is_absolute() and path.resolve(strict=True)==path
 for parent in [path,*path.parents]:
  s=parent.lstat();assert s.st_uid==0 and not stat.S_ISLNK(s.st_mode)
  assert not s.st_mode&0o022 or stat.S_ISDIR(s.st_mode) and s.st_mode&stat.S_ISVTX
 s=path.lstat();assert stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)
 return s
protected(evidence,True);assert evidence.stat().st_mode&0o077==0
protected(manifest)
expected=json.loads(manifest.read_text())
paths=dict(bench=bench,helper=helper,testbin=testbin,driver=driver,api_driver=api,wrapper=wrapper,
 corpus=api.parents[1]/'internal/protocolv2/testdata/workspace-lifecycle.json')
assert set(expected)==set(paths)
for name,path in paths.items():
 before=protected(path);entry=expected[name]
 assert set(entry)=={'path','sha256'} and entry['path']==str(path) and re.fullmatch('[a-f0-9]{64}',entry['sha256'])
 assert hashlib.sha256(path.read_bytes()).hexdigest()==entry['sha256']
 after=path.lstat();assert (before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns)==(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns)
PY
[[ $(df --output=avail -B1 /var/tmp | tail -1) -ge 2147483648 ]]
for tool in mount umount mountpoint fallocate mkfs.xfs python3 git setsid xfs_io setpriv sudo visudo; do command -v "$tool" >/dev/null; done
[[ $(stat -fc %T /sys/fs/cgroup) == cgroup2fs ]]
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-cancel-managed.XXXXXXXX)
[[ "$trial" == /var/tmp/superbench-cancel-managed.* && ! -L "$trial" ]]
chmod 0711 "$trial"
trial_identity=$(stat -Lc '%d:%i' "$trial")
cgroups="/sys/fs/cgroup/superbench-cancel-${trial##*.}"
cgroups_created=false
cgroup_identity=
cleanup(){
 [[ -d "$trial" && ! -L "$trial" && $(stat -Lc '%d:%i' "$trial") == "$trial_identity" ]] || return
 if [[ -f "$trial/daemon.log" && ! -L "$trial/daemon.log" ]];then cp -- "$trial/daemon.log" "$evidence/daemon-final.log";fi
 if [[ -e "$trial/PRESERVE-LIVE-HELPER" ]];then printf '%s\n' "$trial preserved: unresolved fixture processes" > "$evidence/cleanup-preserved.txt";return;fi
 if ! python3 -I - <<'PY'
import os,sys
from pathlib import Path
ns=os.stat('/proc/self/ns/mnt');live=[]
for p in Path('/proc').iterdir():
 if not p.name.isdigit() or int(p.name) in (os.getpid(),os.getppid()):continue
 try:
  other=os.stat(p/'ns/mnt')
  if (other.st_dev,other.st_ino)==(ns.st_dev,ns.st_ino):live.append(p.name)
 except FileNotFoundError:pass
 except OSError:live.append(p.name+':unknown')
if live:print('unresolved owned namespace processes:',live,file=sys.stderr);sys.exit(1)
PY
 then printf '%s\n' "$trial preserved: namespace not quiescent" > "$evidence/cleanup-preserved.txt";return;fi
 if [[ "$cgroups_created" == true ]];then
  [[ -d "$cgroups" && ! -L "$cgroups" && $(stat -Lc '%d:%i' "$cgroups") == "$cgroup_identity" ]] || return
  for scope in "$cgroups"/*;do
   [[ -d "$scope" ]] || continue
   if [[ -L "$scope" || ! ${scope##*/} =~ ^[a-f0-9]{32}$ ]] || ! grep -q '^populated 0$' "$scope/cgroup.events";then
    printf '%s\n' "$trial preserved: cgroup identity/population unknown" > "$evidence/cleanup-preserved.txt";return
   fi
   if ! rmdir -- "$scope";then printf '%s\n' "$trial preserved: cgroup not empty" > "$evidence/cleanup-preserved.txt";return;fi
  done
  if ! rmdir -- "$cgroups";then printf '%s\n' "$trial preserved: control root not empty" > "$evidence/cleanup-preserved.txt";return;fi
 fi
 if mountpoint -q "$trial/data";then
  if ! umount "$trial/data";then printf '%s\n' "$trial preserved: private mount busy" > "$evidence/cleanup-preserved.txt";return;fi
 fi
 [[ -d "$trial" && ! -L "$trial" && $(stat -Lc '%d:%i' "$trial") == "$trial_identity" ]] || return
 rm -rf -- "$trial"
 printf '%s\n' "$trial removed; namespace quiescent; private mount unmounted; exact empty cgroup root removed" > "$evidence/cleanup.txt"
}
trap cleanup EXIT
[[ ! -e "$cgroups" && ! -L "$cgroups" ]]
mkdir "$cgroups"
cgroups_created=true
cgroup_identity=$(stat -Lc '%d:%i' "$cgroups")
[[ -f "$cgroups/cgroup.kill" ]]
chown 1001:1001 "$cgroups" "$cgroups/cgroup.procs" "$cgroups/cgroup.threads" "$cgroups/cgroup.subtree_control"
mkdir "$trial/data" "$trial/bin" "$trial/etc" "$trial/etc/superbench" "$trial/etc/pam.d" "$trial/etc/alternatives" "$trial/private-run"
chmod 0755 "$trial/data" "$trial/bin" "$trial/etc" "$trial/etc/superbench" "$trial/etc/pam.d" "$trial/etc/alternatives" "$trial/private-run"
install -m 0755 "$helper" "$trial/bin/bench-quota"
install -m 0755 "$bench" "$trial/bin/bench"
install -m 0755 "$testbin" "$trial/bin/service.test"
install -m 0644 "$driver" "$trial/bin/cancellation.py"
cmp "$bench" "$trial/bin/bench"
cmp "$testbin" "$trial/bin/service.test"
cmp "$driver" "$trial/bin/cancellation.py"
python3 -I - "$manifest" "$trial/bin/bench-quota" <<'PY'
import hashlib,json,sys
from pathlib import Path
assert hashlib.sha256(Path(sys.argv[2]).read_bytes()).hexdigest()==json.loads(Path(sys.argv[1]).read_text())['helper']['sha256']
PY
# Exact-helper authority is installed ONLY in this already-proven private
# mount/network namespace. No host sudo/PAM/policy/run state is modified.
for public in passwd group nsswitch.conf machine-id hosts resolv.conf; do cp -L "/etc/$public" "$trial/etc/$public"; done
chmod 0644 "$trial/etc/"{passwd,group,nsswitch.conf,machine-id,hosts,resolv.conf}
[[ $(readlink -f /usr/bin/sudo) == /usr/lib/cargo/bin/sudo ]]
[[ $(readlink -f /usr/sbin/visudo) == /usr/lib/cargo/bin/visudo ]]
[[ $(sha256sum /usr/lib/cargo/bin/sudo | cut -d' ' -f1) == 2eb5d31f91a12d75a2a05b54b7f79775e5565898af0b3263414fcabaad922afb ]]
[[ $(sha256sum /usr/lib/cargo/bin/visudo | cut -d' ' -f1) == 21d389b94ec02fb2a1a22d8d973c1cdf5b8d8bc8c6e64eccf130164172867470 ]]
ln -s /usr/lib/cargo/bin/sudo "$trial/etc/alternatives/sudo"
ln -s /usr/lib/cargo/bin/visudo "$trial/etc/alternatives/visudo"
printf 'Defaults timestamp_timeout=0\nDefaults secure_path="/usr/sbin:/usr/bin:/sbin:/bin"\n#1001 ALL=(root) NOPASSWD: %s\n' "$trial/bin/bench-quota" > "$trial/etc/sudoers"
chmod 0440 "$trial/etc/sudoers"
printf 'account required pam_permit.so\nsession required pam_permit.so\n' > "$trial/etc/pam.d/sudo"
chmod 0644 "$trial/etc/pam.d/sudo"
mkdir -m 0711 "$trial/private-run/sudo"
mount --bind "$trial/private-run" /run
mount --bind "$trial/etc" /etc
/usr/sbin/visudo -cf /etc/sudoers
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
python3 -I -u "$trial/bin/cancellation.py" --trial "$trial" --bench "$trial/bin/bench" --testbin "$trial/bin/service.test" --quota-helper "$trial/bin/bench-quota" \
 --api-driver "$api_driver" --cgroup-root "$cgroups" --evidence "$evidence"
