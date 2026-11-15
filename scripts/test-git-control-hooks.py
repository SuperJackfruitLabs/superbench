#!/usr/bin/env python3
"""Disposable Linux proof of control-Git hook isolation and user-command fencing."""
import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import sqlite3
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bench', required=True)
    parser.add_argument('--old-bench', required=True)
    args = parser.parse_args()
    assert os.geteuid() == 0, 'disposable Linux root required'
    root = Path(tempfile.mkdtemp(prefix='sb-git-control-', dir='/var/tmp'))
    cgroups = Path('/sys/fs/cgroup') / root.name
    cgroups.mkdir()
    events = root / 'events'
    events.mkdir()
    children = []
    controllers = []
    controller_logs = []

    def run(words, **kwargs):
        environment = dict(os.environ, GIT_AUTHOR_NAME='Test', GIT_AUTHOR_EMAIL='test@example.invalid',
                           GIT_COMMITTER_NAME='Test', GIT_COMMITTER_EMAIL='test@example.invalid')
        return subprocess.run(list(map(str, words)), env=environment, capture_output=True, text=True, timeout=60, **kwargs)

    def good(words):
        result = run(words)
        assert result.returncode == 0, (words, result.stdout, result.stderr)
        return result.stdout.strip()

    def alive(record):
        try:
            fields = Path('/proc/' + str(record['pid']) + '/stat').read_text().split(') ')[1].split()
            return fields[0] not in ('Z', 'X') and fields[19] == record['start']
        except FileNotFoundError:
            return False

    def stop_owned(record):
        if alive(record):
            commandline = Path('/proc/' + str(record['pid']) + '/cmdline').read_bytes()
            assert record['token'].encode() in commandline, 'PID identity changed'
            os.killpg(record['pid'], signal.SIGKILL)
        deadline = time.monotonic() + 10
        while alive(record):
            assert time.monotonic() < deadline, 'owned child survived cleanup'
            time.sleep(.01)

    def ready(path):
        deadline = time.monotonic() + 10
        while not path.exists():
            assert time.monotonic() < deadline, 'missing hook marker: ' + path.name
            time.sleep(.01)

    worker = root / 'worker.py'
    worker.write_text('''import sys,time
from pathlib import Path
time.sleep(float(sys.argv[3]) if len(sys.argv)>3 else 4)
Path(sys.argv[1]).write_text(sys.argv[2])
while True: time.sleep(60)
''')
    launcher = root / 'launch-hook.py'
    launcher.write_text('''import json,os,subprocess,sys,time
from pathlib import Path
kind,event,target,worker,token=sys.argv[1:6]
if kind=='reference-transaction' and sys.argv[6:]!=['committed']:sys.exit(0)
event=Path(event)
if event.exists():sys.exit(0)
child=subprocess.Popen([sys.executable,worker,target,token],start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,stdin=subprocess.DEVNULL)
fields=Path('/proc/'+str(child.pid)+'/stat').read_text().split(') ')[1].split()
temporary=event.with_suffix('.tmp')
temporary.write_text(json.dumps({'pid':child.pid,'start':fields[19],'token':token,'cgroup':Path('/proc/'+str(child.pid)+'/cgroup').read_text()}))
temporary.replace(event)
''')

    filter_helper = root / 'clean-filter.py'
    filter_helper.write_text('''import json,subprocess,sys,time
from pathlib import Path
event,target,worker,token,mode=sys.argv[1:6]
event=Path(event)
payload=sys.stdin.buffer.read()
new=not event.exists()
if new:
 child=subprocess.Popen([sys.executable,worker,target,token,'30' if mode=='pause' else '4'],start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,stdin=subprocess.DEVNULL)
 fields=Path('/proc/'+str(child.pid)+'/stat').read_text().split(') ')[1].split()
 temporary=event.with_suffix('.tmp')
 temporary.write_text(json.dumps({'pid':child.pid,'start':fields[19],'token':token,'cgroup':Path('/proc/'+str(child.pid)+'/cgroup').read_text()}))
 temporary.replace(event)
sys.stdout.buffer.write(payload)
sys.stdout.buffer.flush()
if new and mode=='pause':
 while True:time.sleep(1)
''')

    class Fixture:
        def __init__(self, name, binary):
            self.name, self.binary = name, binary
            self.root = root / name
            self.root.mkdir()
            self.state, self.data = self.root / 'state', self.root / 'data'
            self.remote, self.seed = self.root / 'remote.git', self.root / 'seed'
            self.git(self.root, 'init', '--bare', self.remote)
            self.git(self.root, 'init', '-b', 'main', self.seed)
            (self.seed / 'source').write_text('confirmed source\n')
            (self.seed / '.gitattributes').write_text('source filter=superbench-control-drill\n')
            self.git(self.seed, 'add', '.')
            self.git(self.seed, 'commit', '-m', 'base')
            self.git(self.seed, 'remote', 'add', 'origin', self.remote)
            self.git(self.seed, 'push', 'origin', 'main')
            self.git(self.remote, 'symbolic-ref', 'HEAD', 'refs/heads/main')
            self.bench = self.cli('create-cold', '--repo', self.remote, 'task')
            self.path = Path(self.bench['Path'])

        def git(self, path, *words):
            return good(['git', '-C', path, *words])

        def command(self, op, *words):
            return [self.binary, op, '--state', self.state, '--data', self.data, '--cgroup-root', cgroups, *words]

        def cli(self, op, *words):
            return json.loads(good(self.command(op, *words)))

        def status(self):
            return self.cli('status', 'task')[0]

        def hold_and_commit(self):
            token = self.cli('lease-repair', '--holder', 'writer', 'task')['Lease']['Token']
            (self.path / 'source').write_text('task work\n')
            self.git(self.path, 'add', 'source')
            self.git(self.path, 'commit', '-m', 'task work')
            return token

        def hooks(self, kind, target=None):
            if kind == 'reference-transaction':
                # A bare mirror does not necessarily retain a fetch mapping.
                # A normal tracking ref update makes push exercise this hook.
                self.git(self.path, 'config', '--replace-all', 'remote.origin.fetch',
                         '+refs/heads/*:refs/remotes/origin/*')
            directory = self.root / 'client-hooks'
            directory.mkdir()
            self.event = events / (self.name + '.json')
            self.target = target or self.path / 'post-release-writer'
            token = root.name + '-' + self.name + '-owned-worker'
            words = ['python3', launcher, kind, self.event, self.target, worker, token]
            script = '#!/bin/sh\nexec ' + ' '.join(shlex.quote(str(word)) for word in words) + ' "$@"\n'
            hook = directory / kind
            hook.write_text(script)
            hook.chmod(0o700)
            self.git(self.path, 'config', 'core.hooksPath', directory)
            self.hook_directory = directory

        def clean_filter(self, pause=False):
            self.event = events / (self.name + '.json')
            self.target = self.path / 'filter-post-release-writer'
            token = root.name + '-' + self.name + '-owned-worker'
            words = ['python3', filter_helper, self.event, self.target, worker, token,
                     'pause' if pause else 'return']
            value = ' '.join(shlex.quote(str(word)) for word in words)
            self.git(self.path, 'config', 'filter.superbench-control-drill.clean', value)
            self.filter_command = value
            # Force a content check without changing any tracked bytes. The
            # filter returns stdin exactly, so its helper does not make Git dirty.
            stamp = time.time() - 120
            os.utime(self.path / 'source', (stamp, stamp))

        def record(self):
            ready(self.event)
            record = json.loads(self.event.read_text())
            children.append(record)
            return record

        def checkpoint_release(self, token):
            self.cli('checkpoint', '--token', token, 'task')
            self.cli('release', '--token', token, 'task')
            assert not self.status().get('Lease')

        def preserve_config(self):
            assert self.git(self.path, 'config', '--get', 'core.hooksPath') == str(self.hook_directory)

    try:
        for kind in ('pre-push', 'reference-transaction'):
            f = Fixture('old-' + kind, args.old_bench)
            token = f.hold_and_commit()
            f.hooks(kind)
            f.checkpoint_release(token)
            record = f.record()
            assert alive(record) and root.name not in record['cgroup'], record
            ready(f.target)
            assert not f.status().get('Lease') and f.target.read_text() == record['token']
            assert 'post-release-writer' in f.git(f.path, 'status', '--porcelain')
            stop_owned(record)
        print('PASS: retained old CLI reproduces both pre-push and reference-transaction detached writers creating untracked files after lease release, outside the execution cgroup', flush=True)

        for kind in ('pre-push', 'reference-transaction'):
            f = Fixture('fixed-' + kind, args.bench)
            token = f.hold_and_commit()
            f.git(f.path, 'tag', '-a', 'unrelated-annotation', '-m', 'unrelated tag')
            f.git(f.path, 'config', 'push.followTags', 'true')
            f.hooks(kind)
            f.checkpoint_release(token)
            assert not f.event.exists() and not f.target.exists()
            assert not f.git(f.path, 'status', '--porcelain')
            f.preserve_config()
            assert f.git(f.path, 'config', '--get', 'push.followTags') == 'true'
            assert run(['git', '-C', f.remote, 'rev-parse', '--verify', 'refs/tags/unrelated-annotation']).returncode != 0
        print('PASS: fixed control checkpoint/release launches neither client hook nor descendant, preserves repository hook/followTags configuration, leaves checkout clean and publishes no unrelated annotated tag', flush=True)

        for kind in ('pre-push', 'reference-transaction'):
            f = Fixture('user-' + kind, args.bench)
            f.hooks(kind)
            if kind == 'pre-push':
                words = ['git', 'push', 'origin', 'HEAD:refs/heads/' + f.bench['Branch']]
            else:
                words = ['git', 'update-ref', 'refs/heads/user-explicit-command', 'HEAD']
            result = f.cli('run', '--holder', 'explicit-user-git', 'task', '--', *words)
            assert result['ExitCode'] == 0 and not result['Bench'].get('Lease'), result
            record = f.record()
            assert root.name in record['cgroup'], record
            assert not alive(record), 'detached user Git hook child escaped its fenced scope'
            time.sleep(4.1)
            assert not f.target.exists() and not f.git(f.path, 'status', '--porcelain')
            f.preserve_config()
        print('PASS: explicit user Git commands still execute both configured hooks; detached setsid descendants stay in the execution cgroup and are fenced before clean checkpoint/release', flush=True)

        for kind in ('pre-receive', 'reference-transaction'):
            f = Fixture('server-' + kind, args.bench)
            token = f.hold_and_commit()
            hook = f.remote / 'hooks' / kind
            script = '#!/bin/sh\n'
            if kind == 'reference-transaction':
                script += '[ "$1" != prepared ] && exit 0\n'
            script += 'exit 1\n'
            hook.write_text(script)
            hook.chmod(0o700)
            rejected = run(f.command('checkpoint', '--token', token, 'task'))
            assert rejected.returncode != 0
            assert f.status()['Lease']['Holder'] == 'writer'
            with sqlite3.connect(f.state / 'bench.db') as db:
                record = json.loads(db.execute('SELECT record FROM benches WHERE key=?', ('task',)).fetchone()[0])
            assert record['Lease']['Token'] == token
            assert run(f.command('release', '--token', token, 'task')).returncode != 0
            assert f.path.exists()
            hook.unlink()
            f.checkpoint_release(token)
        print('PASS: server pre-receive and reference-transaction rejection still block checkpoint and retain the same lease; removing the server rejection permits explicit recovery', flush=True)

        f = Fixture('old-clean-filter', args.old_bench)
        token = f.hold_and_commit()
        f.clean_filter()
        f.checkpoint_release(token)
        record = f.record()
        assert alive(record) and root.name not in record['cgroup']
        ready(f.target)
        assert not f.status().get('Lease') and f.target.read_text() == record['token']
        assert 'filter-post-release-writer' in f.git(f.path, 'status', '--porcelain')
        stop_owned(record)
        print('PASS: retained schema-12 CLI also releases a Git-clean checkout while an external pass-through clean filter leaves an unfenced delayed writer alive', flush=True)

        f = Fixture('fixed-clean-filter', args.bench)
        token = f.hold_and_commit()
        f.clean_filter()
        f.checkpoint_release(token)
        record = f.record()
        assert root.name in record['cgroup'], record
        assert not alive(record)
        time.sleep(4.1)
        assert not f.target.exists() and not f.git(f.path, 'status', '--porcelain')
        assert f.git(f.path, 'config', '--get', 'filter.superbench-control-drill.clean') == f.filter_command
        print('PASS: managed control Git contains an external pass-through clean filter and fences its detached writer before checkpoint/release while preserving tracked bytes and filter configuration', flush=True)

        f = Fixture('interrupted-clean-filter', args.bench)
        token = f.hold_and_commit()
        f.clean_filter(pause=True)
        log = (f.root / 'controller.log').open('wb')
        controller_logs.append(log)
        controller = subprocess.Popen(list(map(str, f.command('checkpoint', '--token', token, 'task'))),
                                      stdout=log, stderr=log, start_new_session=True)
        controllers.append(controller)
        record = f.record()
        assert root.name in record['cgroup'] and alive(record)
        journal_root = f.state / 'git-controls'
        deadline = time.monotonic() + 10
        while not list(journal_root.glob('*.json')):
            assert controller.poll() is None
            assert time.monotonic() < deadline, 'missing durable control journal'
            time.sleep(.01)
        pending_journal = None
        for path in journal_root.glob('*.json'):
            intent = json.loads(path.read_text())
            if ('/' + intent['ID']) not in record['cgroup']:
                continue
            assert intent['Version'] == 'superbench.git-control.v1'
            assert path.name == intent['ID'] + '.json'
            assert Path(intent['Root']).is_relative_to(cgroups)
            pending_journal = path
        assert pending_journal is not None, 'filter cgroup has no matching durable control intent'
        controller.kill()  # Leave its known cgroup worker/filter alive for recovery.
        controller.wait(timeout=15)
        assert alive(record), 'controller-only kill unexpectedly fenced detached helper'
        assert f.status().get('Lease') and alive(record), 'read-only status performed recovery'
        with sqlite3.connect(f.state / 'bench.db') as db:
            assert db.execute('PRAGMA user_version').fetchone()[0] == 23
        previous = run([args.old_bench, 'status', '--state', f.state, '--data', f.data, 'task'])
        assert previous.returncode != 0 and 'unsupported state schema' in previous.stderr
        blocked = run(f.command('lease-repair', '--holder', 'competitor', 'task'))
        assert blocked.returncode != 0
        assert not alive(record), 'next mutation did not recover/fence the durable control scope'
        assert not pending_journal.exists(), 'recovered control intent remained pending'
        assert not f.target.exists()
        f.checkpoint_release(token)
        replacement = f.cli('lease-repair', '--holder', 'after-recovery', 'task')['Lease']['Token']
        f.checkpoint_release(replacement)
        print('PASS: SIGKILL of a paused control controller leaves a durable schema-22 intent and live known scope; status does not fence, schema-12 CLI refuses, and the next mutation recovers/fences before handoff or a later lease grant', flush=True)

        f = Fixture('warm-creation', args.bench)
        target = f.data / 'benches/warm/post-release-writer'
        f.hooks('post-checkout', target=target)
        warm = f.cli('create-cold', '--repo', f.remote, 'warm')
        assert Path(warm['Path']).exists() and not f.event.exists() and not target.exists()
        f.preserve_config()
        print('PASS: a warm shared-mirror worktree creation suppresses configured post-checkout hooks without changing the repository hook configuration', flush=True)
        print('DONE: Git control-hook isolation and user-command cgroup fencing passed', flush=True)
    finally:
        for controller in controllers:
            if controller.poll() is None:
                controller.kill()
                controller.wait(timeout=15)
        for log in controller_logs:
            log.close()
        # Include a marker produced immediately before an unexpected failure.
        for path in events.glob('*.json'):
            record = json.loads(path.read_text())
            if record not in children:
                children.append(record)
        for record in children:
            stop_owned(record)
        scopes = [path for path in cgroups.rglob('*') if path.is_dir()]
        for scope in cgroups.iterdir():
            if scope.is_dir():
                (scope / 'cgroup.kill').write_text('1')
        for scope in sorted(scopes, key=lambda path: len(path.parts), reverse=True):
            deadline = time.monotonic() + 10
            while 'populated 1' in (scope / 'cgroup.events').read_text():
                assert time.monotonic() < deadline, 'owned cgroup cleanup timeout'
                time.sleep(.02)
            scope.rmdir()
        cgroups.rmdir()
        shutil.rmtree(root)


if __name__ == '__main__':
    main()
