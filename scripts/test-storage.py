#!/usr/bin/env python3
"""Root Linux integration in two bounded disposable XFS filesystems."""
import errno
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import signal
import sqlite3
import subprocess
import sys
import tarfile
import time

root=Path(sys.argv[1]);binary=str(Path(sys.argv[2]).resolve());helper=str(Path(sys.argv[3]).resolve());cgroups=Path(sys.argv[4])
real_git=shutil.which('git');data=root/'data';state=root/'state';budget=root/'budget'
def run(args,**kwargs):return subprocess.run([str(a) for a in args],text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=60,**kwargs)
def good(args,**kwargs):
 p=run(args,**kwargs)
 if p.returncode!=0:
  detail=''
  try:
   record=json.loads(p.stdout);log=Path(record['Bench']['Lease']['Execution']['Log'])
   if log.is_relative_to(root):detail=log.read_text()
  except (ValueError,KeyError,TypeError,OSError):pass
  raise AssertionError(p.stderr+'\n'+detail)
 return p.stdout.strip()
def git(where,*args):return good([real_git,'-C',where,*args])
def writepolicy(path,datadir,ledger,bench=16<<20):
 p={'Root':str(datadir),'Ledger':str(ledger),'OwnerUID':0,'OwnerGID':0,'BenchBytes':bench,'StoreBytes':16<<20,'MirrorBytes':32<<20,'ReserveBytes':64<<20,'BenchInodes':2048,'StoreInodes':8192,'MirrorInodes':8192,'ReserveInodes':1024}
 path.write_text(json.dumps(p));path.chmod(0o600);return p
policy=root/'policy.json';settings=writepolicy(policy,data,root/'quota-ledger')
# Pausing after an actual privileged assignment tests the lost-reply boundary.
wrapper=root/'quota-wrapper'
wrapper.write_text('''#!/bin/sh
"$SUPERBENCH_REAL_QUOTA" "$@"
status=$?
if [ "$status" = 0 ] && [ "$SUPERBENCH_PAUSE_KEY" != "" ]; then
 for arg in "$@";do
  if [ "$arg" = "$SUPERBENCH_PAUSE_KEY" ];then
   echo ready > "$SUPERBENCH_MARKER"
   while :;do sleep 1;done
  fi
 done
fi
exit "$status"
''');wrapper.chmod(0o700)
os.environ['SUPERBENCH_REAL_QUOTA']=helper

def command(verb,*args):return [binary,verb,'--state',str(state),'--data',str(data),'--cgroup-root',str(cgroups),*map(str,args)]
def cli(verb,*args):return json.loads(good(command(verb,*args)))
cli('configure-host','--quota-helper',wrapper,'--quota-policy',policy,'--min-data-free',8<<20,'--min-state-free',1<<20)
status=cli('host-status');assert status['Managed'] and status['Data']['Device']!=status['State']['Device']
print('PASS: managed host config verifies a separate data filesystem and fixed-policy shared caps',flush=True)

package=root/'package';package.mkdir();(package/'package.json').write_text(json.dumps({'name':'storage-fixture','version':'1.0.0'}))
payload=os.urandom(4<<20);(package/'payload.bin').write_bytes(payload)
archive=root/'fixture.tgz'
with tarfile.open(archive,'w:gz') as tf:tf.add(package,arcname='package')
seed=root/'seed';remote=root/'remote.git';git(root,'init','--bare',remote);git(root,'init','-b','main',seed)
(seed/'vendor').mkdir();shutil.copyfile(archive,seed/'vendor/fixture.tgz')
(seed/'package.json').write_text(json.dumps({'name':'storage-client','private':True,'scripts':{'build':'node build.cjs'},'dependencies':{'storage-fixture':'file:vendor/fixture.tgz'}}))
(seed/'.gitignore').write_text('node_modules/\nbuild.out\n')
(seed/'build.cjs').write_text("require('fs').writeFileSync('build.out',require('fs').readFileSync('node_modules/storage-fixture/payload.bin').length.toString())")
good(['pnpm','install','--lockfile-only','--ignore-scripts','--store-dir',root/'generation-store'],cwd=seed,env={**os.environ,'CI':'true'})
git(seed,'add','.');git(seed,'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-m','fixture')
git(seed,'remote','add','origin',remote);git(seed,'push','origin','main');git(remote,'symbolic-ref','HEAD','refs/heads/main')
first=cli('create','--repo',remote,'first');assert first['ProjectID']>=10000 and first['QuotaBytes']==16<<20
setup=cli('setup','--holder','installer','first');assert setup['ExitCode']==0 and setup['Bench'].get('Lease') is None
built=cli('run','--holder','builder','first','--','pnpm','run','build');assert built['ExitCode']==0
assert (Path(first['Path'])/'build.out').read_text()==str(4<<20)
cli('setup','--holder','warm-installer','--offline','first')
second=cli('create','--repo',remote,'second');cli('setup','--holder','second-installer','--offline','second')
assert second['ProjectID']!=first['ProjectID']
pa=Path(first['Path'])/'node_modules/storage-fixture/payload.bin';pb=Path(second['Path'])/'node_modules/storage-fixture/payload.bin'
assert pa.read_bytes()==payload and pb.read_bytes()==payload
print('PASS: managed frozen setup, build and warm/offline handoffs work inside project quotas',flush=True)
noisy=cli('run','--holder','noisy-command','first','--','sh','-c','head -c 2097152 /dev/zero')
assert noisy['LogTruncated'] and Path(noisy['Execution']['Log']).stat().st_size<=1<<20
assert noisy['Bench'].get('Lease') is None
print('PASS: noisy command output is bounded, marked truncated and still checkpointed safely',flush=True)
map_a=good(['xfs_io','-c','fiemap -v',pa]);map_b=good(['xfs_io','-c','fiemap -v',pb])
print(map_a,flush=True);print(map_b,flush=True)
pattern=r'^\s*\d+:\s+\[[^]]+\]:?\s+(\d+\.\.\d+)\s+\d+\s+0x([0-9a-fA-F]+)'
extents_a=re.findall(pattern,map_a,re.M);extents_b=re.findall(pattern,map_b,re.M)
assert extents_a and extents_a==extents_b and all(int(flags,16)&0x2000 for _,flags in extents_a),'installed payloads do not share reflink extents'
with open(pa,'r+b') as f:f.write(b'changed')
assert hashlib.sha256(pb.read_bytes()).digest()==hashlib.sha256(payload).digest()
print('PASS: package clone mutation leaves the other bench intact',flush=True)
with open(Path(first['Path'])/'quota-fill','wb',buffering=0) as f:
 try:
  for _ in range(32):f.write(b'x'*(1<<20))
 except OSError as e:assert e.errno in (errno.ENOSPC,errno.EDQUOT),e
 else:raise AssertionError('bench quota did not stop write')
(data/'store/independent-write').write_bytes(b'y'*(1<<20))
(Path(first['Path'])/'quota-fill').unlink()
print('PASS: bench hard cap refuses writes while the shared store stays writable',flush=True)

# Insecure policy, escaping key and changed budgets cannot change allocations.
assert run([helper,'--policy',policy,'apply','bench','../escape']).returncode!=0
policy.chmod(0o666);assert run([helper,'--policy',policy,'apply','bench','blocked']).returncode!=0;policy.chmod(0o600)
link=root/'policy-link';link.symlink_to(policy);assert run([helper,'--policy',link,'apply','bench','blocked']).returncode!=0
changed=root/'changed-policy.json';modified={**settings,'BenchBytes':32<<20};changed.write_text(json.dumps(modified));changed.chmod(0o600)
assert run([helper,'--policy',changed,'apply','bench','blocked']).returncode!=0
print('PASS: helper refuses escaping keys, writable/symlink policy and allocation-policy drift',flush=True)
# A remount or administrator disabling enforcement must block new work too.
good(['xfs_quota','-x','-c','disable -p',data])
assert run(command('lease','--holder','blocked-by-enforcement','first')).returncode!=0
good(['xfs_quota','-x','-c','enable -p',data])
lease=cli('lease','--holder','after-enforcement-restored','first')
cli('checkpoint','--token',lease['Lease']['Token'],'first');cli('release','--token',lease['Lease']['Token'],'first')
print('PASS: disabled project enforcement blocks new work until restored',flush=True)


# Wrong mount mode is refused, then a separate image proves budget reservation.
other=root/'budget-policy.json';writepolicy(other,budget,root/'budget-ledger',128<<20)
assert run([helper,'--policy',other,'apply','bench','large-one']).returncode!=0
assert not (root/'budget-ledger').exists(),'non-enforcing filesystem created an allocation ledger'
good(['umount',budget]);good(['mount','-o','loop,prjquota,nodev,nosuid',root/'budget.img',budget])
good([helper,'--policy',other,'apply','bench','large-one']);good([helper,'--policy',other,'apply','bench','large-two'])
denied=run([helper,'--policy',other,'apply','bench','large-three'])
assert denied.returncode!=0 and 'budget exhausted' in denied.stderr,(denied.stdout,denied.stderr)
print('PASS: quota enforcement is required and total cap reservations reject overcommit',flush=True)

# Kill the caller after helper assignment but before receiving the reply.
marker=root/'allocation-ready';env={**os.environ,'SUPERBENCH_PAUSE_KEY':'allocation-crash','SUPERBENCH_MARKER':str(marker)}
p=subprocess.Popen(command('create','--repo',remote,'allocation-crash'),env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True,text=True)
for _ in range(400):
 if marker.exists():break
 if p.poll() is not None:raise AssertionError(p.communicate())
 time.sleep(.025)
else:raise AssertionError('quota boundary not reached')
os.killpg(p.pid,signal.SIGKILL);p.communicate(timeout=10)
ledger=json.loads((root/'quota-ledger/assignments.json').read_text());assigned=ledger['Assignments']['bench:allocation-crash']['ProjectID'];counter=ledger['Next']
resumed=cli('create','--repo',remote,'allocation-crash')
assert resumed['ProjectID']==assigned and json.loads((root/'quota-ledger/assignments.json').read_text())['Next']==counter
print('PASS: interrupted privileged allocation resumes with the same project and reservation',flush=True)

# Kill clone before publication. Retrying must never borrow incomplete objects.
otherremote=root/'other.git';git(root,'clone','--bare',remote,otherremote)
gitwrapper=root/'git-wrapper';gitwrapper.mkdir();marker=root/'clone-ready'
(gitwrapper/'git').write_text('''#!/bin/sh
"$SUPERBENCH_REAL_GIT" "$@"
status=$?
if [ "$status" = 0 ] && [ "$3" = clone ];then
 echo ready > "$SUPERBENCH_MARKER"
 while :;do sleep 1;done
fi
exit "$status"
''');(gitwrapper/'git').chmod(0o700)
env={**os.environ,'PATH':str(gitwrapper)+':'+os.environ['PATH'],'SUPERBENCH_REAL_GIT':real_git,'SUPERBENCH_MARKER':str(marker)}
p=subprocess.Popen(command('create','--repo',otherremote,'clone-crash'),env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True,text=True)
for _ in range(400):
 if marker.exists():break
 if p.poll() is not None:raise AssertionError(p.communicate())
 time.sleep(.025)
else:raise AssertionError('clone boundary not reached')
os.killpg(p.pid,signal.SIGKILL);p.communicate(timeout=10)
orphans=list((data/'mirrors').glob('.clone-*'));assert orphans
recovered=cli('create','--repo',otherremote,'clone-crash')
assert not any(str(path) in recovered['Mirror'] for path in orphans)
assert git(recovered['Mirror'],'rev-parse','--is-bare-repository')=='true'
print('PASS: interrupted mirror publication is isolated; retry publishes a complete mirror',flush=True)
ledgerfile=root/'quota-ledger/assignments.json';saved=ledgerfile.read_bytes()
ledgerfile.write_text('{}')
assert run(command('lease','--holder','corrupt-ledger','first')).returncode!=0
ledgerfile.write_bytes(saved)
print('PASS: incomplete privileged ledger fails closed without defaulting to fresh allocations',flush=True)

# UUID binding survives loop-device changes but refuses a different filesystem.
good(['umount',data])
spare=root/'spare.img'
with open(spare,'wb') as f:f.truncate(1<<20)
loop=good(['losetup','--find','--show',spare])
try:good(['mount','-o','loop,prjquota,nodev,nosuid',root/'data.img',data])
finally:good(['losetup','--detach',loop])
lease=cli('lease','--holder','after-remount','first')
cli('checkpoint','--token',lease['Lease']['Token'],'first');cli('release','--token',lease['Lease']['Token'],'first')
print('PASS: filesystem UUID binding survives remount on a different loop device',flush=True)
good(['umount',data]);good(['umount',budget])
good(['mount','-o','loop,prjquota,nodev,nosuid',root/'budget.img',data])
wrong_volume=run(command('lease','--holder','wrong-volume','first'))
assert wrong_volume.returncode!=0 and 'UUID differs' in wrong_volume.stderr,(wrong_volume.stdout,wrong_volume.stderr)
print('PASS: a different filesystem at the same path is refused',flush=True)

print('DONE: managed storage integration passed',flush=True)
