#!/usr/bin/env python3
"""Linux staging experiment; run via spike-failures.sh in a private namespace."""
import errno
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import signal
import sqlite3
import subprocess
import sys
import tarfile
import time

root = Path(sys.argv[1])
binary = str(Path(sys.argv[2]).resolve())
real_git = shutil.which('git')

def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=60, **kwargs)

def good(args, **kwargs):
    p = run(args, **kwargs)
    assert p.returncode == 0, p.stdout
    return p.stdout.strip()

def git(where, *args):
    return good([real_git, '-C', where, *args])

remote = root / 'remote.git'
seed = root / 'seed'
git(root, 'init', '--bare', remote)
git(root, 'init', '-b', 'main', seed)
(seed / 'file').write_text('base\n')
git(seed, 'add', '.')
git(seed, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'base')
git(seed, 'remote', 'add', 'origin', remote)
git(seed, 'push', 'origin', 'main')
git(remote, 'symbolic-ref', 'HEAD', 'refs/heads/main')

state = root / 'mnt/state'
data = root / 'data'

def snapshot(directory=state):
    db_path=directory / 'bench.db'
    if not db_path.exists():
        return b'{}'
    with sqlite3.connect(str(db_path)) as db:
        tables=db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name='benches'").fetchall()
        records={} if not tables else {key:json.loads(record) for key,record in db.execute('SELECT key,record FROM benches')}
    return json.dumps(records,sort_keys=True).encode()

def command(verb, key='task', *flags, state_dir=state, data_dir=data):
    return [binary, verb, '--state', str(state_dir), '--data', str(data_dir), *map(str, flags), key]

def cli(verb, key='task', *flags, **kwargs):
    return json.loads(good(command(verb, key, *flags, **kwargs)))

def fill(path):
    with open(path, 'wb', buffering=0) as f:
        try:
            while True:
                f.write(b'x' * 1048576)
        except OSError as e:
            assert e.errno in (errno.ENOSPC, errno.EDQUOT), e
            print('EXPECTED quota exhaustion:', e, flush=True)

b = cli('create', 'task', '--repo', remote)
l = cli('lease', 'task', '--holder', 'worker')
token = l['Lease']['Token']
checkout = Path(b['Path'])
(checkout / 'file').write_text('checkpoint\n')
git(checkout, 'add', '.')
git(checkout, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'checkpoint')
sha = git(checkout, 'rev-parse', 'HEAD')
original = snapshot()
# Exhaust state after the remote accepts the push, before the local SQL update.
fill_hook = remote / 'hooks/post-receive'
fill_program = root / 'fill-state.py'
fill_program.write_text("import errno,sys\nf=open(sys.argv[1],'wb',buffering=0)\ntry:\n while True: f.write(b'x'*1048576)\nexcept OSError as e:\n assert e.errno in (errno.ENOSPC,errno.EDQUOT),e\n")
fill_hook.write_text('#!/bin/sh\nexec python3 "'+str(fill_program)+'" "'+str(state / 'filler')+'"\n')
fill_hook.chmod(0o700)
p = run(command('checkpoint' , 'task', '--token', token))
assert p.returncode != 0, p.stdout
print('EXPECTED SQLite write failure:', p.stdout.strip(), flush=True)
assert snapshot() == original
assert git(remote, 'rev-parse', 'refs/heads/bench/task') == sha
print('PASS: real state-write failure after successful push preserves prior state and lease', flush=True)
(state / 'filler').unlink()
fill_hook.unlink()
cli('checkpoint', 'task', '--token', token)
original = snapshot()
fill(state / 'filler')
p = run(command('release', 'task', '--token', token))
assert p.returncode != 0, p.stdout
assert snapshot() == original
assert json.loads(original)['task']['Lease']['Token'] == token
(state / 'filler').unlink()
cli('release', 'task', '--token', token)
print('PASS: failed release state-save retains lease; retry succeeds after freeing space', flush=True)

# A test-only git wrapper waits AFTER the real command succeeds; no production failpoints.
wrapper = root / 'wrapper'
wrapper.mkdir()
(wrapper / 'git').write_text('''#!/bin/sh
"$SUPERBENCH_REAL_GIT" "$@"
code=$?
if [ "$code" = 0 ] && { { [ "$SUPERBENCH_PAUSE" = create ] && [ "$3" = worktree ] && [ "$4" = add ]; } || { [ "$SUPERBENCH_PAUSE" = push ] && [ "$3" = push ]; }; }; then
  echo ready > "$SUPERBENCH_MARKER"
  while :; do sleep 1; done
fi
exit "$code"
''')
(wrapper / 'git').chmod(0o700)

def interrupt(args, phase):
    marker = root / ('marker-' + phase)
    env = {**os.environ, 'PATH': str(wrapper) + ':' + os.environ['PATH'],
           'SUPERBENCH_REAL_GIT': real_git, 'SUPERBENCH_PAUSE': phase,
           'SUPERBENCH_MARKER': str(marker)}
    p = subprocess.Popen(args, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                         text=True, start_new_session=True)
    try:
        for _ in range(200):
            if marker.exists():
                break
            if p.poll() is not None:
                raise AssertionError(p.communicate()[0])
            time.sleep(0.05)
        else:
            raise AssertionError('git boundary was not reached')
    finally:
        if p.poll() is None:
            os.killpg(p.pid, signal.SIGKILL)
        p.communicate(timeout=10)

crash_state = root / 'crash-state'
crash_data = root / 'crash-data'
args = command('create', 'orphan', '--repo', remote, state_dir=crash_state, data_dir=crash_data)
interrupt(args, 'create')
orphan = crash_data / 'benches/orphan'
assert (orphan / 'file').read_text() == 'base\n'
assert json.loads(snapshot(crash_state)) == {}
p = run(args)
assert p.returncode == 0, p.stdout
registered = json.loads(p.stdout)
assert registered['Path'] == str(orphan)
assert (orphan / 'file').read_text() == 'base\n'
print('PASS: SIGKILL after worktree creation leaves durable intent; retry safely registers original checkout', flush=True)

l = cli('lease', 'task', '--holder', 'second')
token = l['Lease']['Token']
(checkout / 'file').write_text('post-crash checkpoint\n')
git(checkout, 'add', '.')
git(checkout, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-m', 'next')
sha = git(checkout, 'rev-parse', 'HEAD')
old = snapshot()
interrupt(command('checkpoint', 'task', '--token', token), 'push')
assert snapshot() == old
assert git(remote, 'rev-parse', 'refs/heads/bench/task') == sha
assert run(command('lease', 'task', '--holder', 'third')).returncode != 0
cli('checkpoint', 'task', '--token', token)
cli('release', 'task', '--token', token)
print('PASS: SIGKILL after push retains lease; checkpoint retry reconciles remote/local state', flush=True)

# An actual pnpm store failure, with one healthy cached package retained.
store = root / 'mnt/store'
fixtures = root / 'fixtures'
fixtures.mkdir()
def package(name, megabytes):
    archive = fixtures / (name + '.tgz')
    with tarfile.open(archive, 'w:gz') as tf:
        for filename, content in [('package.json', json.dumps({'name': name, 'version': '1.0.0'}).encode()),
                                  ('payload.bin', os.urandom(megabytes * 1048576))]:
            info = tarfile.TarInfo('package/' + filename)
            info.size = len(content)
            tf.addfile(info, io.BytesIO(content))
    return archive
healthy = package('healthy-fixture', 4)
large = package('large-fixture', 12)
def client(name, archive):
    p = root / 'mnt' / name
    p.mkdir()
    (p / 'package.json').write_text(json.dumps({'name': name, 'private': True,
        'dependencies': {archive.stem: 'file:' + str(archive)}}))
    (p / '.npmrc').write_text(f'store-dir={store}\npackage-import-method=clone\nside-effects-cache=false\nupdate-notifier=false\n')
    return p

def install(p, *args):
    return run(['pnpm', 'install', '--ignore-scripts', '--reporter=append-only', *args],
               cwd=p, env={**os.environ, 'CI': 'true'})
first = client('healthy-client', healthy)
p = install(first)
assert p.returncode == 0, p.stdout
payload = next((first / 'node_modules/.pnpm').rglob('payload.bin'))
digest = hashlib.sha256(payload.read_bytes()).hexdigest()
second = client('large-client', large)
p = install(second)
assert p.returncode != 0 and ('ENOSPC' in p.stdout or 'EDQUOT' in p.stdout or 'space' in p.stdout.lower()), p.stdout
print('EXPECTED pnpm store failure:', p.stdout[-1200:], flush=True)
assert hashlib.sha256(payload.read_bytes()).hexdigest() == digest
# Relax only this disposable project's cap and retry normally; no manual store repair.
good(['xfs_quota', '-x', '-c', 'limit -p bhard=64m 101', root / 'mnt'])
p = install(second)
assert p.returncode == 0, p.stdout
third = client('offline-client', healthy)
shutil.copy(first / 'pnpm-lock.yaml', third / 'pnpm-lock.yaml')
# Copy exact manifest so the frozen lockfile has the same dependency specification.
shutil.copy(first / 'package.json', third / 'package.json')
p = install(third, '--offline', '--frozen-lockfile')
assert p.returncode == 0, p.stdout
assert hashlib.sha256(next((third / 'node_modules/.pnpm').rglob('payload.bin')).read_bytes()).hexdigest() == digest
print('PASS: pnpm reports store exhaustion, healthy install stays intact, retry and healthy offline reuse succeed', flush=True)
print('DONE: failure and interruption experiments passed', flush=True)
