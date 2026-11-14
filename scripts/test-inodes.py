#!/usr/bin/env python3
"""Actual XFS inode enforcement and explicit upgrade from the byte-only slice."""
import errno
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys

root=Path(sys.argv[1]);bench,helper,old_bench,old_helper=map(str,sys.argv[2:6]);cgroups=Path(sys.argv[6])
data=root/"data";state=root/"state";policy=root/"policy.json";ledger=root/"ledger/assignments.json"
current_helper=root/"current-helper";shutil.copyfile(old_helper,current_helper);current_helper.chmod(0o700)
settings={"Root":str(data),"Ledger":str(root/"ledger"),"OwnerUID":0,"OwnerGID":0,
          "BenchBytes":16<<20,"StoreBytes":16<<20,"MirrorBytes":32<<20,"ReserveBytes":64<<20}
def write_policy(values,path=policy):path.write_text(json.dumps(values));path.chmod(0o600)
write_policy(settings)
def run(args,**kwargs):return subprocess.run(list(map(str,args)),text=True,capture_output=True,timeout=60,**kwargs)
def good(args):
 p=run(args);assert p.returncode==0,p.stderr;return p.stdout
def command(binary,op,*args):return [binary,op,"--state",state,"--data",data,*args]
def cli(op,*args):return json.loads(good(command(bench,op,*args)))
def old(op,*args):return json.loads(good(command(old_bench,op,*args)))
def quota(project,unit="-i"):
 report=good(["xfs_quota","-x","-c",f"report -p -n {unit} -N -L {project} -U {project}",data])
 fields=next(line.split() for line in report.splitlines() if line.split() and line.split()[0]==f"#{project}")
 return int(fields[1]),int(fields[3])

def fill(directory,cap):
 directory.mkdir();made=[]
 try:
  for index in range(cap+1):
   path=directory/f"file-{index}"
   try:path.touch()
   except OSError as e:
    assert e.errno in (errno.EDQUOT,errno.ENOSPC),e
    break
   made.append(path)
  else:raise AssertionError("inode hard limit did not stop zero-byte creation")
  assert made and len(made)<cap
  return made
 except Exception:
  for path in made:path.unlink()
  directory.rmdir();raise

def clean_files(paths):
 directory=paths[0].parent
 for path in paths:path.unlink()
 directory.rmdir()

old("configure-host","--quota-helper",current_helper,"--quota-policy",policy,"--min-data-free",8<<20,"--min-state-free",1<<20)
seed=root/"seed";remote=root/"remote.git"
good(["git","init","--bare",remote]);good(["git","init","-b","main",seed])
(seed/"file").write_text("base\n");(seed/".gitignore").write_text("fill/\n")
good(["git","-C",seed,"add","."]);good(["git","-C",seed,"-c","user.name=Test","-c","user.email=test@example.invalid","commit","-m","base"])
good(["git","-C",seed,"remote","add","origin",remote]);good(["git","-C",seed,"push","origin","main"]);good(["git","-C",remote,"symbolic-ref","HEAD","refs/heads/main"])
legacy=old("create","--repo",remote,"task");original_id=legacy["ProjectID"]
assert quota(original_id)[1]==0
shutil.copyfile(helper,current_helper);current_helper.chmod(0o700)
assert run([helper,"--policy",policy,"apply","shared","store"]).returncode!=0
settings.update(BenchInodes=1,StoreInodes=256,MirrorInodes=512,ReserveInodes=128);write_policy(settings)
assert run([helper,"--policy",policy,"apply","shared","store"]).returncode!=0
assert run(command(bench,"migrate")).returncode!=0
with sqlite3.connect(state/"bench.db") as db:assert db.execute("pragma user_version").fetchone()[0]==4
assert run([helper,"--policy",policy,"migrate-inodes"]).returncode!=0
assert Path(legacy["Path"]).is_dir()
settings["BenchInodes"]=32;write_policy(settings)
assignments=json.loads(good([helper,"--policy",policy,"migrate-inodes"]))
assert next(a for a in assignments if a["ProjectID"]==original_id)["Inodes"]==32
# Simulate a kernel effect missing after a durable assignment update.
good(["xfs_quota","-x","-c",f"limit -p ihard=0 {original_id}",data])
good([helper,"--policy",policy,"migrate-inodes"])
assert quota(original_id)[1]==32
cli("migrate");record=cli("status","task")[0]
assert record["ProjectID"]==original_id and record["QuotaInodes"]==32
assert quota(100)[1]==256 and quota(101)[1]==512
assert run(command(old_bench,"status","task")).returncode!=0
assert run([old_helper,"--policy",policy,"apply","shared","store"]).returncode!=0
refused=run([helper,"--policy",policy,"migrate-inodes"],env={**os.environ,"SUDO_UID":"123"})
assert refused.returncode!=0 and "direct root" in refused.stderr
print("PASS: explicit resumable legacy migration pins original IDs and inode caps; old binaries and sudo migration refuse",flush=True)

checkout=Path(record["Path"]);files=fill(checkout/"fill",32)
assert quota(original_id)[0]==32
(data/"store/independent").write_text("other project");(data/"store/independent").unlink()
# Both cooperative acquisition and supervised run must restore/read back their
# own project's bound, not just trust a healthy shared-store quota.
good(["xfs_quota","-x","-c",f"limit -p ihard=0 {original_id}",data])
lease=cli("lease","--holder","inode-guard","task");assert quota(original_id)[1]==32
clean_files(files)
token=lease["Lease"]["Token"];cli("checkpoint","--token",token,"task");cli("release","--token",token,"task")
good(["xfs_quota","-x","-c",f"limit -p ihard=0 {original_id}",data])
result=cli("run","--holder","inode-run","--cgroup-root",cgroups,"task","--","true")
assert result["ExitCode"]==0 and not result["Bench"].get("Lease") and quota(original_id)[1]==32
print("PASS: zero-byte bench files hit the inode hard cap; lease/run restore and verify their pinned limit",flush=True)

for key,project,cap in [("store",100,256),("mirrors",101,512)]:
 paths=fill(data/key/"fixture-inodes",cap)
 assert quota(project)[0]==cap
 (checkout/"fill-independent").write_text("separate project");(checkout/"fill-independent").unlink()
 clean_files(paths)
print("PASS: both shared stores and mirrors enforce independent inode hard caps",flush=True)

candidate=root/"overcommit.json";write_policy({**settings,"ReserveInodes":1<<40},candidate)
before=json.loads(ledger.read_text())
failed=run([helper,"--policy",candidate,"apply","bench","blocked"])
assert failed.returncode!=0 and "pinned ledger" in failed.stderr,failed.stderr
assert json.loads(ledger.read_text())==before
# Capacity exhaustion is a separate check from policy drift. A fresh ledger
# cannot have a pinned policy yet and must reject the impossible reservation.
fresh_ledger=root/"overcommit-ledger";fresh_data=root/"overcommit-data";fresh_image=root/"overcommit.img"
fresh_data.mkdir()
good(["fallocate","-l","512M",fresh_image]);good(["mkfs.xfs","-q","-m","reflink=1",fresh_image])
good(["mount","-o","loop,prjquota,nodev,nosuid",fresh_image,fresh_data])
try:
 write_policy({**settings,"Root":str(fresh_data),"ReserveInodes":1<<40,"Ledger":str(fresh_ledger)},candidate)
 failed=run([helper,"--policy",candidate,"apply","bench","blocked"])
 assert failed.returncode!=0 and "inode budget exhausted" in failed.stderr,failed.stderr
 rejected=json.loads((fresh_ledger/"assignments.json").read_text())
 assert rejected["Assignments"]=={} and rejected["Next"]==10000
 assert not (fresh_data/"benches/blocked").exists()
finally:
 good(["umount",fresh_data]);fresh_image.unlink()
assert json.loads(ledger.read_text())==before
assert not (data/"benches/blocked").exists()
print("PASS: total inode reservation overcommit refuses before assigning a new project",flush=True)

cli("archive","--archive-grace","0s","task");plan=cli("plan-reap","task");planfile=root/"reap.json";planfile.write_text(json.dumps(plan))
assert cli("reap","--plan",planfile,"--approve",plan["Digest"],"task")["ReapedAt"]
retired=json.loads(ledger.read_text())["Assignments"]["bench:task"]
assert retired["Bytes"]==0 and retired["Inodes"]==0
replacement=cli("recover","--repo",remote,"--branch",record["Branch"],"replacement")
assert replacement["ProjectID"]>original_id and replacement["QuotaInodes"]==32
print("PASS: reviewed reap retires both reservations; recovery receives a new bounded project",flush=True)
print("DONE: inode quota integration passed",flush=True)
