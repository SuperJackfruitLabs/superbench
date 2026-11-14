#!/usr/bin/env python3
"""Actual managed XFS reap, open-file retention, crash replay and downgrade guards."""
import json
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import time

root = Path(sys.argv[1])
bench, helper, old_bench, old_helper = map(str, map(Path, sys.argv[2:6]))
data = root / "data"
state = root / "state"
policy = root / "policy.json"
policy.write_text(json.dumps({"Root":str(data), "Ledger":str(root / "ledger"), "OwnerUID":0, "OwnerGID":0,
                             "BenchBytes":32<<20, "StoreBytes":16<<20, "MirrorBytes":32<<20, "ReserveBytes":64<<20,"BenchInodes":4096,"StoreInodes":8192,"MirrorInodes":8192,"ReserveInodes":1024}))
policy.chmod(0o600)

def run(args):
    return subprocess.run(list(map(str,args)),capture_output=True,text=True,timeout=30)
def good(args):
    p=run(args)
    assert p.returncode==0,p.stderr
    return p.stdout
def git(path,*args):
    return good(["git","-C",path,*args]).strip()
def command(op,*args):
    return [bench,op,"--state",state,"--data",data,*args]
def cli(op,*args):
    return json.loads(good(command(op,*args)))

# Pause only after the real helper durably retires a project; no simulated quota.
wrapper=root / "quota-wrapper"
wrapper.write_text('''#!/bin/sh
"$ARCHIVE_REAL_HELPER" "$@"
result=$?
if [ "$result" = 0 ] && [ "$ARCHIVE_PAUSE" = 1 ];then
 for word in "$@";do
  if [ "$word" = release ];then
   echo ready > "$ARCHIVE_MARKER"
   while :;do sleep 1;done
  fi
 done
fi
exit "$result"
''')
wrapper.chmod(0o700)
os.environ["ARCHIVE_REAL_HELPER"]=helper
os.environ["ARCHIVE_MARKER"]=str(root / "marker")
cli("configure-host","--quota-helper",wrapper,"--quota-policy",policy,"--min-data-free",8<<20,"--min-state-free",1<<20)
seed=root / "seed"
remote=root / "remote.git"
git(root,"init","--bare",remote);git(root,"init","-b","main",seed)
(seed / "file").write_text("base\n");(seed / ".gitignore").write_text("build/\n")
git(seed,"add",".");git(seed,"-c","user.name=Test","-c","user.email=test@example.invalid","commit","-m","base")
git(seed,"remote","add","origin",remote);git(seed,"push","origin","main");git(remote,"symbolic-ref","HEAD","refs/heads/main")

def archived(key):
    b=cli("create","--repo",remote,key)
    lease=cli("lease","--holder","worker",key)
    token=lease["Lease"]["Token"]
    cli("checkpoint","--token",token,key);cli("release","--token",token,key)
    build=Path(b["Path"]) / "build";build.mkdir();(build / "artifact").write_bytes(b"x"*(2<<20))
    b=cli("archive",key)
    plan=cli("plan-reap",key);planfile=root / (key+".json");planfile.write_text(json.dumps(plan));planfile.chmod(0o600)
    return b,plan,planfile

def reap_args(key,plan,file):
    return command("reap","--plan",file,"--approve",plan["Digest"],key)

first,plan,file=archived("first")
# Same-filesystem bind mounts evade st_dev-only traversal guards.
linked=Path(first["Path"]) / "build/bind"
linked.mkdir()
external=data / "external";external.mkdir();(external / "keep").write_text("preserve")
good(["mount","--bind",external,linked])
try:
    refused=run(command("plan-reap","first"))
    assert refused.returncode!=0 and "mount refused" in refused.stderr,refused.stderr
    assert (external / "keep").read_text()=="preserve"
finally:
    good(["umount",linked]);linked.rmdir()
print("PASS: same-filesystem nested bind mount is refused without deleting its contents",flush=True)
# Regenerate after removing the test mount/directory.
plan=cli("plan-reap","first");file.write_text(json.dumps(plan))
assert run(command("reap","--plan",file,"first")).returncode!=0
assert Path(first["Path"]).exists()
artifact=Path(first["Path"]) / "build/artifact"
fd=artifact.open("rb")
try:
    failed=run(reap_args("first",plan,file))
    assert failed.returncode!=0 and "reservation retained" in failed.stderr,failed.stderr
    ledger=json.loads((root / "ledger/assignments.json").read_text())
    assert ledger["Assignments"]["bench:first"]["Bytes"]==32<<20
finally:
    fd.close()
result=json.loads(good(reap_args("first",plan,file)))
assert result["ReapedAt"] and not Path(first["Path"]).exists()
ledger=json.loads((root / "ledger/assignments.json").read_text())
assert ledger["Assignments"]["bench:first"]["Bytes"]==0
assert run([helper,"--policy",policy,"apply","bench","first"]).returncode!=0
print("PASS: explicit approval required; open unlinked data retains quota until close; retry retires project",flush=True)

# A zero-byte open file proves the inode guard independently of block usage.
inode_bench,inode_plan,inode_file=archived("inode-only")
empty=Path(inode_bench["Path"]) / "build/empty";empty.touch()
inode_plan=cli("plan-reap","inode-only");inode_file.write_text(json.dumps(inode_plan))
with empty.open("rb") as held:
    failed=run(reap_args("inode-only",inode_plan,inode_file))
    assert failed.returncode!=0 and "reservation retained" in failed.stderr,failed.stderr
    def usage(flag):
        project=inode_bench["ProjectID"]
        report=good(["xfs_quota","-x","-c",f"report -p -n {flag} -N -L {project} -U {project}",data])
        return int(next(line.split()[1] for line in report.splitlines() if line.split() and line.split()[0]==f"#{project}"))
    assert usage("-b")==0 and usage("-i")>0
assert json.loads(good(reap_args("inode-only",inode_plan,inode_file)))["ReapedAt"]
print("PASS: zero-block open inode independently prevents reservation release",flush=True)

# The privileged helper removes only the matching empty logical directory.
assignment=json.loads(good([helper,"--policy",policy,"apply","bench","empty-guard"]))
empty_path=Path(assignment["Path"])
assert run([helper,"--policy",policy,"release","bench","empty-guard",assignment["ProjectID"]+1]).returncode!=0
assert empty_path.is_dir()
backup=data / "kept-empty";empty_path.rename(backup)
external=root / "external-kept";external.mkdir();(external / "keep").write_text("keep")
empty_path.symlink_to(external,target_is_directory=True)
assert run([helper,"--policy",policy,"release","bench","empty-guard",assignment["ProjectID"]]).returncode!=0
assert (external / "keep").read_text()=="keep"
empty_path.unlink();backup.rename(empty_path)
assert json.loads(good([helper,"--policy",policy,"release","bench","empty-guard",assignment["ProjectID"]]))["Bytes"]==0
print("PASS: wrong project IDs and symlink targets refuse privileged empty-directory retirement",flush=True)

# Pre-archive binaries must reject both new core state and retired quota ledgers.
old=run([old_bench,"lease","--state",state,"--data",data,"--holder","old","first"])
assert old.returncode!=0 and "unsupported state schema" in old.stderr,old.stderr
legacy_policy=root / "legacy-policy.json"
legacy_policy.write_text(json.dumps({key:value for key,value in json.loads(policy.read_text()).items() if not key.endswith("Inodes")}));legacy_policy.chmod(0o600)
old=run([old_helper,"--policy",legacy_policy,"apply","shared","store"])
assert old.returncode!=0 and "ledger allocation conflicts" in old.stderr,old.stderr
print("PASS: previous binaries refuse archive schema and retired-project ledger",flush=True)

second,plan2,file2=archived("second")
assert second["ProjectID"]>first["ProjectID"]
os.environ["ARCHIVE_PAUSE"]="1"
proc=subprocess.Popen(list(map(str,reap_args("second",plan2,file2))),start_new_session=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
try:
    deadline=time.monotonic()+15
    while not (root / "marker").exists():
        assert proc.poll() is None,"reap exited before retirement marker"
        assert time.monotonic()<deadline,"retirement marker timeout"
        time.sleep(.05)
    os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=5)
finally:
    if proc.poll() is None:
        os.killpg(proc.pid,signal.SIGKILL);proc.wait(timeout=5)
os.environ.pop("ARCHIVE_PAUSE")
status=cli("status","second")[0]
assert status["ReapingDigest"]==plan2["Digest"] and not status.get("ReapedAt")
assert json.loads(good(reap_args("second",plan2,file2)))["ReapedAt"]
assert json.loads(good(reap_args("second",plan2,file2)))["ReapedAt"]
replacement=cli("recover","--repo",remote,"--branch",second["Branch"],"replacement")
assert git(replacement["Path"],"rev-parse","HEAD")==second["Checkpoint"]
print("PASS: SIGKILL after quota retirement retains approval; retry finalizes only once; checkpoint recovers",flush=True)
print("DONE: managed archive/reap integration passed",flush=True)
