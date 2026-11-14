#!/usr/bin/env python3
"""Disposable Linux daemon/log history drill; no installed service is changed."""
import argparse
import fcntl
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import tempfile
import time
import uuid

class UnixHTTP(http.client.HTTPConnection):
 def __init__(self,path):super().__init__("local",timeout=10);self.path=str(path)
 def connect(self):self.sock=socket.socket(socket.AF_UNIX);self.sock.settimeout(self.timeout);self.sock.connect(self.path)

def wait_for(check,label):
 deadline=time.monotonic()+15
 while time.monotonic()<deadline:
  if check():return
  time.sleep(.05)
 raise AssertionError("timeout: "+label)

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument("--bench",required=True);p.add_argument("--benchd",required=True);p.add_argument("--old-bench",required=True);args=p.parse_args()
 if os.geteuid()!=0:raise SystemExit("disposable Linux root required")
 root=Path(tempfile.mkdtemp(prefix="sb-retention-",dir="/var/tmp"));state=root/"state";data=root/"data";sock=root/"api.sock"
 cg=Path("/sys/fs/cgroup")/("sb-retention-"+uuid.uuid4().hex);cg.mkdir()
 daemon=None;daemon_log=(root/"daemon.log").open("wb")
 def run(words):return subprocess.run(list(map(str,words)),capture_output=True,text=True,timeout=30)
 def good(words):
  r=run(words);assert r.returncode==0,r.stderr;return r.stdout
 def command(op,*words):return [args.bench,op,"--state",state,"--data",data,"--cgroup-root",cg,*words]
 def cli(op,*words):return json.loads(good(command(op,*words)))
 def plan():return cli("plan-collect","--older-than","0s","--metadata-older-than","0s")
 def save(value,name="plan.json"):
  path=root/name;path.write_text(json.dumps(value));path.chmod(0o600);return path
 def collect(value):return cli("collect","--plan",save(value),"--approve",value["Digest"])
 def start():
  process=subprocess.Popen([args.benchd,"--state",str(state),"--data",str(data),"--socket",str(sock),"--cgroup-root",str(cg)],stdout=daemon_log,stderr=daemon_log)
  def ready():
   assert process.poll() is None,"daemon exited"
   try:
    c=UnixHTTP(sock);c.request("GET","/healthz");r=c.getresponse();r.read();c.close();return r.status==200
   except (OSError,http.client.HTTPException):return False
  wait_for(ready,"daemon ready");return process
 try:
  seed=root/"seed";remote=root/"remote.git"
  good(["git","init","--bare",remote]);good(["git","init","-b","main",seed]);(seed/"file").write_text("base\n")
  good(["git","-C",seed,"add","."]);good(["git","-C",seed,"-c","user.name=Test","-c","user.email=test@example.invalid","commit","-m","base"])
  good(["git","-C",seed,"remote","add","origin",remote]);good(["git","-C",seed,"push","origin","main"]);good(["git","-C",remote,"symbolic-ref","HEAD","refs/heads/main"])
  cli("create","--repo",remote,"task")
  successful=cli("run","--holder","successful","task","--","sh","-c","printf evidence")
  success_id=successful["Execution"]["ID"];log=state/("run-"+success_id+".log")
  record=cli("history","task")[0];assert record["Origin"]=="supervised" and record["ExitCode"]==0 and record["Checkpoint"]
  with log.open("rb") as writer:
   fcntl.flock(writer,fcntl.LOCK_EX|fcntl.LOCK_NB);assert not plan()["Logs"]
  approved=plan();assert len(approved["Logs"])==1
  assert run(command("collect","--plan",save(approved),"--approve","wrong")).returncode!=0 and log.exists()
  log.write_text("changed")
  assert run(command("collect","--plan",save(approved),"--approve",approved["Digest"])).returncode!=0 and log.exists()
  log.write_text("evidence")
  assert collect(approved)["Status"]=="completed" and not log.exists()
  assert cli("resume-collect","--approve",approved["Digest"],approved["Digest"])["Status"]=="completed"
  print("PASS: real run history is sealed/released; open writer, wrong approval and changed file refuse; approved collection replays safely",flush=True)

  failed=run(command("run","--holder","failure","task","--","sh","-c","printf failure; exit 7"));assert failed.returncode==7
  failed_result=json.loads(failed.stdout);failed_id=failed_result["Execution"]["ID"]
  assert all(item["Record"]["ID"]!=failed_id for item in (plan()["Logs"] or []))
  token=failed_result["Bench"]["Lease"]["Token"];cli("checkpoint","--token",token,"task");cli("release","--token",token,"task")
  assert any(item["Record"]["ID"]==failed_id for item in plan()["Logs"])
  collect(plan())
  print("PASS: failed command evidence remains protected until explicit checkpoint/release",flush=True)

  legacy_id="f"*32;legacy=state/("run-"+legacy_id+".log");legacy.write_text("legacy output");legacy.chmod(0o600)
  assert not plan()["Logs"]
  adoption=cli("inspect-log",legacy_id);adoption_file=save(adoption,"adopt.json")
  assert run(command("adopt-log","--plan",adoption_file,"--approve",adoption["Digest"])).returncode!=0
  before=cli("status","task")
  adopted=cli("adopt-log","--plan",adoption_file,"--approve",adoption["Digest"],"--acknowledge-stopped-writers")
  assert adopted["Origin"]=="operator-adopted" and adopted["ExitCode"] is None and adopted["StartedAt"].startswith("0001-")
  assert cli("status","task")==before
  collect(plan());assert not legacy.exists()
  print("PASS: unregistered log is preserved until separate ownership approval; adoption invents no outcome or lease transition",flush=True)

  daemon=start();marker=root/"executed"
  request={"version":"superbench.v1","operation":"run","key":"task","holder":"interrupted","request_id":"interrupted-retention","arguments":["sh","-c",f"printf x >> {marker}; printf partial; sleep 90"]}
  c=UnixHTTP(sock);c.request("POST","/v1/operations",json.dumps(request),{"Content-Type":"application/json"});c.close()
  wait_for(marker.exists,"command started");daemon.kill();daemon.wait(timeout=10);daemon=None
  interrupted=next(r for r in cli("history","task") if r["Holder"]=="interrupted");assert interrupted["File"] is None
  stopped=cli("stop","--admin","task");token=stopped["Lease"]["Token"]
  cli("checkpoint","--token",token,"task");cli("release","--token",token,"task")
  assert not plan()["Logs"]
  sealed=cli("reconcile-log",interrupted["ID"]);assert sealed["File"] and sealed["ExitCode"] is None
  collect(plan());prune=plan();assert prune["Records"];collect(prune)
  assert cli("history")==[]
  daemon=start();c=UnixHTTP(sock);c.request("POST","/v1/operations",json.dumps(request),{"Content-Type":"application/json"});r=c.getresponse();reply=json.loads(r.read());c.close()
  assert r.status==409 and reply["error"]["code"]=="operation_in_doubt" and marker.read_text()=="x"
  old=run([args.old_bench,"status","--state",state,"--data",data]);assert old.returncode!=0 and "unsupported state schema" in old.stderr
  print("PASS: daemon SIGKILL retains held/unsealed history; fence/release/reconcile permit collection; pruning history never reexecutes an old request",flush=True)
  daemon.terminate();assert daemon.wait(timeout=15)==0;daemon=None
  print("DONE: run history and reviewed log retention integration passed",flush=True)
 finally:
  if daemon is not None and daemon.poll() is None:daemon.kill();daemon.wait(timeout=10)
  if (cg/"cgroup.kill").exists():(cg/"cgroup.kill").write_text("1")
  for child in cg.iterdir():
   if child.is_dir():child.rmdir()
  cg.rmdir();daemon_log.close();shutil.rmtree(root)

if __name__=="__main__":main()
