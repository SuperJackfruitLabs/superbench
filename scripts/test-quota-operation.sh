#!/usr/bin/env bash
# unshare --mount ONLY; args helper testbin driver evidence original-ns manifest.
set -euo pipefail
[[ $# -eq 6 && $(id -u) -eq 0 ]]
helper=$1 testbin=$2 driver=$3 evidence=$4 original_namespace=$5 manifest=$6
[[ "$original_namespace" =~ ^mnt:\[[0-9]+\]$ ]]
[[ $(readlink /proc/self/ns/mnt) != "$original_namespace" ]]
[[ $(stat -Lc '%d:%i' /proc/self/ns/mnt) != $(stat -Lc '%d:%i' /proc/1/ns/mnt) ]]
[[ $(stat -Lc '%d:%i' /proc/self/ns/pid) == $(stat -Lc '%d:%i' /proc/1/ns/pid) ]]
[[ "$evidence" == /var/tmp/superbench-verification/cancel-helper-* ]]
# Protected exact input identities/hashes and evidence root precede mount effects.
python3 -I - "$helper" "$testbin" "$driver" "$evidence" "$manifest" <<'PY'
import hashlib,json,os,re,stat,sys
from pathlib import Path
helper,testbin,driver,evidence,manifest=map(Path,sys.argv[1:])
def protected(path,directory=False):
 assert path.is_absolute() and path.resolve(strict=True)==path
 for parent in [path,*path.parents]:
  s=parent.lstat();assert s.st_uid==0 and not stat.S_ISLNK(s.st_mode)
  assert not s.st_mode&0o022 or stat.S_ISDIR(s.st_mode) and s.st_mode&stat.S_ISVTX
 s=path.lstat()
 assert stat.S_ISDIR(s.st_mode) if directory else stat.S_ISREG(s.st_mode)
 return s
protected(evidence,True);assert evidence.stat().st_mode&0o077==0
protected(manifest)
expected=json.loads(manifest.read_text());assert set(expected)=={'helper','testbin','driver'}
for name,path in [('helper',helper),('testbin',testbin),('driver',driver)]:
 before=protected(path);entry=expected[name]
 assert set(entry)=={'path','sha256'} and entry['path']==str(path) and re.fullmatch('[a-f0-9]{64}',entry['sha256'])
 assert hashlib.sha256(path.read_bytes()).hexdigest()==entry['sha256']
 after=path.lstat();assert (before.st_dev,before.st_ino,before.st_size,before.st_mtime_ns)==(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns)
PY
[[ $(df --output=avail -B1 /var/tmp | tail -1) -ge 2147483648 ]]
mount --make-rprivate /
trial=$(mktemp -d /var/tmp/superbench-cancel-helper-XXXXXXXX)
[[ "$trial" == /var/tmp/superbench-cancel-helper-* && ! -L "$trial" ]]
chmod 0700 "$trial"
trial_identity=$(stat -Lc '%d:%i' "$trial")
mkdir "$trial/data"
cleanup(){
 [[ -d "$trial" && ! -L "$trial" && $(stat -Lc '%d:%i' "$trial") == "$trial_identity" ]] || return
 if [[ -e "$trial/PRESERVE-LIVE-HELPER" ]];then echo "preserved owned live-helper fixture $trial" > "$evidence/cleanup-preserved.txt";return;fi
 # Prove namespace quiescence independently, including children orphaned by a
 # timeout/crash before Python's own finally. Never infer it from a PID list.
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
if live:print('unresolved namespace processes:',live,file=sys.stderr);sys.exit(1)
PY
 then echo "preserved owned unresolved-process fixture $trial" > "$evidence/cleanup-preserved.txt";return;fi
 if mountpoint -q "$trial/data";then
  if ! umount "$trial/data";then echo "preserved owned busy-mount fixture $trial" > "$evidence/cleanup-preserved.txt";return;fi
 fi
 [[ -d "$trial" && ! -L "$trial" && $(stat -Lc '%d:%i' "$trial") == "$trial_identity" && "$trial" == /var/tmp/superbench-cancel-helper-* ]] || return
 rm -rf -- "$trial"
 printf '%s\n' "$trial removed; namespace quiescent; private mount unmounted" > "$evidence/cleanup.txt"
}
trap cleanup EXIT
install -m 0755 "$helper" "$trial/bench-quota"
python3 -I - "$manifest" "$trial/bench-quota" <<'PY'
import hashlib,json,sys
from pathlib import Path
assert hashlib.sha256(Path(sys.argv[2]).read_bytes()).hexdigest()==json.loads(Path(sys.argv[1]).read_text())["helper"]["sha256"]
PY
fallocate -l 512M "$trial/data.img"
mkfs.xfs -q -m reflink=1 "$trial/data.img"
mount -o loop,prjquota,nodev,nosuid "$trial/data.img" "$trial/data"
python3 -I -u "$driver" "$trial" "$testbin" "$evidence"
