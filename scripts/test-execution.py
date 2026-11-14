#!/usr/bin/env python3
"""Root Linux integration test. Uses only the supplied disposable cgroup subtree."""
import json
import errno
import os
from pathlib import Path
import signal
import sqlite3
import subprocess
import sys
import time

root = Path(sys.argv[1])
binary = str(Path(sys.argv[2]).resolve())
cgroups = Path(sys.argv[3])
old_binary = str(Path(sys.argv[4]).resolve()) if len(sys.argv)>4 else None
def run(args, **kwargs):
    return subprocess.run([str(a) for a in args], text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, timeout=30, **kwargs)
def good(args):
    p=run(args)
    assert p.returncode==0, (p.stdout,p.stderr)
    return p.stdout.strip()
def git(where,*args): return good(['git','-C',where,*args])
remote=root/'remote.git';seed=root/'seed'
git(root,'init','--bare',remote);git(root,'init','-b','main',seed)
(seed/'file').write_text('base\n');git(seed,'add','.')
git(seed,'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-m','base')
git(seed,'remote','add','origin',remote);git(seed,'push','origin','main');git(remote,'symbolic-ref','HEAD','refs/heads/main')
state=root/'state';data=root/'data'
def command(verb,*flags): return [binary,verb,'--state',str(state),'--data',str(data),'--cgroup-root',str(cgroups),*map(str,flags)]
def cli(verb,*flags): return json.loads(good(command(verb,*flags)))
def ready(path,proc=None):
    for _ in range(400):
        if path.exists(): return
        if proc is not None and proc.poll() is not None: raise AssertionError(proc.communicate())
        time.sleep(.025)
    raise AssertionError('readiness deadline exceeded: '+str(path))
def alive(pid):
    try: return Path('/proc/'+str(pid)+'/stat').read_text().split(') ')[1].split()[0] not in ('Z','X')
    except FileNotFoundError:return False
helper=root/'spawn.py'
helper.write_text('''import subprocess,sys,time
from pathlib import Path
marker=Path(sys.argv[1]);pidfile=Path(sys.argv[2])
child=subprocess.Popen(['setsid','sh','-c','echo $$ > "$1"; exec sleep 600','sh',str(pidfile)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
while not pidfile.exists():time.sleep(.01)
marker.write_text('ready')
if sys.argv[3]=='wait':
 while True:time.sleep(1)
''')

cli('create','--repo',remote,'first')
marker=root/'first-ready';pidfile=root/'first-child'
p=run(command('run','--holder','first-worker','first','--','python3',helper,marker,pidfile,'exit'))
assert p.returncode==0,(p.stdout,p.stderr)
result=json.loads(p.stdout)
assert result['Bench']['Lease'] is None if 'Lease' in result['Bench'] else True
assert not alive(int(pidfile.read_text())),'detached descendant survived completed run'
assert cli('status','first')[0].get('Lease') is None
print('PASS: parent exits normally; detached setsid descendant fenced before checkpoint/release',flush=True)

cli('create','--repo',remote,'second')
marker=root/'second-ready';pidfile=root/'second-child'
p=subprocess.Popen(command('run','--holder','second-worker','second','--','python3',helper,marker,pidfile,'wait'),text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
ready(marker,p)
status=cli('status','second')[0]
assert status['Lease']['Execution']['Phase']=='running'
if old_binary:
    old=run([old_binary,'status','--state',state,'--data',data,'second'])
    assert old.returncode!=0 and 'unsupported state schema' in old.stderr,(old.stdout,old.stderr)
    print('PASS: previous SQLite binary refuses execution-schema state',flush=True)
execution=status['Lease']['Execution']
assert (cgroups/execution['ID']/'cgroup.procs').exists()
child=int(pidfile.read_text());assert alive(child)
p.kill();p.communicate(timeout=10)
assert alive(child),'supervisor-only kill unexpectedly killed detached descendant'
assert run(command('lease','--holder','replacement','second')).returncode!=0
assert run(command('stop','--token','wrong','second')).returncode!=0
assert alive(child),'wrong token was allowed to fence execution'
with sqlite3.connect(str(state/'bench.db')) as db:
    record=json.loads(db.execute('select record from benches where key=?',('second',)).fetchone()[0])
token=record['Lease']['Token']
# A copied state file on another host must not be mistaken for a reboot.
foreign=json.loads(json.dumps(record));foreign['Lease']['Execution']['HostID']='another-host'
with sqlite3.connect(str(state/'bench.db')) as db:
    db.execute('update benches set record=? where key=?',(json.dumps(foreign),'second'))
wrong_host=run(command('stop','--admin','second'))
assert wrong_host.returncode!=0 and 'different host' in wrong_host.stderr,(wrong_host.stdout,wrong_host.stderr)
assert alive(child),'foreign-host metadata was accepted for fencing'
with sqlite3.connect(str(state/'bench.db')) as db:
    db.execute('update benches set record=? where key=?',(json.dumps(record),'second'))
print('PASS: foreign-host state is refused, rather than treated as a reboot',flush=True)
assert run(command('checkpoint','--token',token,'second')).returncode!=0
assert run(command('release','--token',token,'second')).returncode!=0
stopped=cli('stop','--admin','second')
assert stopped['Lease']['Execution']['Phase']=='stopped'
assert not alive(child),'detached descendant survived administrative fencing'
assert not (cgroups/execution['ID']).exists(), 'empty fenced scope was not removed'
cli('checkpoint','--token',stopped['Lease']['Token'],'second')
cli('release','--token',stopped['Lease']['Token'],'second')
print('PASS: supervisor SIGKILL retains lease; wrong-token stop refused; admin stop fences surviving descendants and permits recovery',flush=True)

cli('create','--repo',remote,'third')
marker=root/'third-ready';pidfile=root/'third-child'
p=subprocess.Popen(command('run','--holder','third-worker','third','--','python3',helper,marker,pidfile,'wait'),text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
ready(marker,p);p.send_signal(signal.SIGTERM);out,err=p.communicate(timeout=15)
assert p.returncode!=0,(out,err)
assert not alive(int(pidfile.read_text())),'descendant survived cancellation'
status=cli('status','third')[0]
assert status['Lease']['Execution']['Phase']=='stopped'
cli('checkpoint','--token',json.loads(out)['Bench']['Lease']['Token'],'third')
cli('release','--token',json.loads(out)['Bench']['Lease']['Token'],'third')
print('PASS: cancellation fences the tree and retains a recoverable stopped lease',flush=True)

cli('create','--repo',remote,'fourth')
marker=root/'fourth-ready';pidfile=root/'fourth-child'
p=subprocess.Popen(command('run','--holder','fourth-worker','fourth','--','python3',helper,marker,pidfile,'wait'),text=True,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
ready(marker,p)
with open(state/'filler','wb',buffering=0) as filler:
    try:
        while True:filler.write(b'x'*1048576)
    except OSError as e:assert e.errno==errno.ENOSPC,e
p.send_signal(signal.SIGTERM);out,err=p.communicate(timeout=15)
assert p.returncode!=0,(out,err)
assert not alive(int(pidfile.read_text())),'full state prevented cancellation fencing'
(state/'filler').unlink()
assert cli('status','fourth')[0]['Lease'] is not None,'state failure released lease'
stopped=cli('stop','--admin','fourth')
cli('checkpoint','--token',stopped['Lease']['Token'],'fourth')
cli('release','--token',stopped['Lease']['Token'],'fourth')
print('PASS: full state does not prevent known-scope cancellation; lease remains held for reconciliation',flush=True)
print('DONE: Linux cgroup execution integration passed',flush=True)
