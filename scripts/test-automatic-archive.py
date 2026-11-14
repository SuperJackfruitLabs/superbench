#!/usr/bin/env python3
"""Real CLI/periodic-daemon archive, immutable policy and crash recovery drill."""
import argparse
from datetime import datetime, timedelta
import json
import os
from pathlib import Path
import shutil
import signal
import sqlite3
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bench', required=True)
    parser.add_argument('--benchd', required=True)
    parser.add_argument('--old-bench', required=True, help='published schema-11 CLI')
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix='sb-automatic-archive-', dir='/var/tmp'))
    cgroups = Path('/sys/fs/cgroup') / root.name
    cgroups.mkdir()
    processes = []
    logs = []

    def run(words, **kwargs):
        environment = dict(os.environ, GIT_AUTHOR_NAME='Test', GIT_AUTHOR_EMAIL='test@example.invalid',
                           GIT_COMMITTER_NAME='Test', GIT_COMMITTER_EMAIL='test@example.invalid')
        return subprocess.run(list(map(str, words)), env=environment, capture_output=True, text=True, timeout=60, **kwargs)

    def good(words):
        result = run(words)
        assert result.returncode == 0, (words, result.stderr)
        return result.stdout.strip()

    def wait_for(check, label, process=None):
        deadline = time.monotonic() + 60
        while not check():
            if process is not None:
                assert process.poll() is None, 'daemon exited: ' + label
            assert time.monotonic() < deadline, 'timeout: ' + label
            time.sleep(.002)

    def stop(process, kill=False):
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL if kill else signal.SIGTERM)
        process.wait(timeout=20)

    class Fixture:
        def __init__(self, name, mode='branch-merged'):
            self.root = root / name
            self.root.mkdir()
            self.state, self.data = self.root / 'state', self.root / 'data'
            self.remote, self.seed = self.root / 'remote.git', self.root / 'seed'
            self.sock = self.root / 'api.sock'
            self.git(self.root, 'init', '--bare', self.remote)
            self.git(self.root, 'init', '-b', 'main', self.seed)
            (self.seed / 'source').write_text('confirmed source\n')
            (self.seed / '.gitignore').write_text('bulk/\n')
            (self.seed / '.bench').mkdir()
            (self.seed / '.bench/config.toml').write_text("[archive]\nwhen='" + mode + "'\n")
            self.commit(self.seed)
            self.git(self.seed, 'remote', 'add', 'origin', self.remote)
            self.git(self.seed, 'push', 'origin', 'main')
            self.git(self.remote, 'symbolic-ref', 'HEAD', 'refs/heads/main')

        def git(self, path, *words):
            return good(['git', '-C', path, *words])

        def commit(self, path):
            self.git(path, 'add', '.')
            self.git(path, 'commit', '-m', 'fixture work')
            return self.git(path, 'rev-parse', 'HEAD')

        def command(self, operation, *words, binary=None):
            return [binary or args.bench, operation, '--state', self.state, '--data', self.data,
                    '--cgroup-root', cgroups, *words]

        def cli(self, operation, *words, binary=None):
            return json.loads(good(self.command(operation, *words, binary=binary)))

        def status(self, key='task'):
            return self.cli('status', key)[0]

        def checkpoint(self, b, change=True, binary=None):
            held = self.cli('lease-repair', '--holder', 'writer', b['Key'], binary=binary)
            if change:
                (Path(b['Path']) / 'task-work').write_text(b['Key'] + '\n')
                self.commit(b['Path'])
            b = self.cli('checkpoint', '--token', held['Lease']['Token'], b['Key'], binary=binary)
            self.cli('release', '--token', held['Lease']['Token'], b['Key'], binary=binary)
            return b

        def create(self, key='task', binary=None):
            return self.cli('create-cold', '--repo', self.remote, key, binary=binary)

        def merge(self, b, squash=False):
            self.git(self.seed, 'fetch', 'origin', b['Branch'])
            if squash:
                self.git(self.seed, 'merge', '--squash', 'FETCH_HEAD')
                self.git(self.seed, 'commit', '-m', 'squash task into a distinct commit')
                assert self.git(self.seed, 'rev-parse', 'HEAD') != b['Checkpoint']
            else:
                self.git(self.seed, 'merge', '--no-ff', 'FETCH_HEAD', '-m', 'merge task')
            self.git(self.seed, 'push', 'origin', 'main')
            return self.git(self.seed, 'rev-parse', 'HEAD')

        def daemon(self):
            log = (self.root / ('daemon-' + str(len(logs)) + '.log')).open('wb')
            logs.append(log)
            process = subprocess.Popen(list(map(str, [args.benchd, '--state', self.state, '--data', self.data,
                '--socket', self.sock, '--cgroup-root', cgroups, '--archive-interval', '1s',
                '--retention-interval', '1h'])), stdout=log, stderr=log, start_new_session=True)
            processes.append(process)
            return process

        def tick(self):
            return self.cli('archive-run', '--limit', '100')

        def assert_active(self, key='task'):
            b = self.status(key)
            assert not b.get('ArchivedAt') and not b.get('ArchiveMerge') and not b.get('ArchiveFreezeDigest'), b
            assert Path(b['Path']).stat().st_mode & 0o200
            return b

    try:
        f = Fixture('periodic')
        b = f.create()
        assert b['CreatedFrom'] == f.git(f.seed, 'rev-parse', 'HEAD')
        b = f.checkpoint(b, change=False)
        assert f.tick()['Results'][0]['Status'] == 'not eligible'
        f.assert_active()
        b = f.checkpoint(b)
        proof = b['ArchiveUnmerged']
        assert proof['Checkpoint'] == b['Checkpoint'] and proof['TargetBranch'] == 'refs/heads/main'
        assert f.tick()['Results'][0]['Status'] == 'not eligible'
        target = f.merge(b)
        b = f.checkpoint(b, change=False)
        assert b['ArchiveUnmerged'] == proof
        daemon = f.daemon()
        wait_for(lambda: bool(f.status().get('ArchiveFrozenAt')), 'periodic archive completion', daemon)
        archived = f.status()
        assert archived['ArchiveMerge']['Checkpoint'] == b['Checkpoint']
        assert archived['ArchiveMerge']['TargetCommit'] == target
        assert archived['ArchiveMerge']['ConfigDigest'] == proof['ConfigDigest']
        assert datetime.fromisoformat(archived['ReapAfter'].replace('Z', '+00:00')) - datetime.fromisoformat(archived['ArchivedAt'].replace('Z', '+00:00')) == timedelta(days=7)
        assert not archived.get('ReapedAt') and Path(b['Path']).exists()
        for path in (Path(b['Path']), Path(b['Path']) / 'source', Path(b['Path']) / '.bench/config.toml'):
            assert path.stat().st_mode & 0o222 == 0
        assert run(f.command('lease-repair', '--holder', 'late', 'task')).returncode != 0
        time.sleep(1.1)
        assert f.status() == archived
        stop(daemon)
        assert daemon.returncode == 0
        print('PASS: periodic daemon ignores fresh/unmerged work, preserves repeated checkpoint proof, then archives a real merge read-only with exact evidence and seven-day grace; repeats are immutable and SIGTERM exits cleanly', flush=True)

        f = Fixture('follow-default')
        b = f.checkpoint(f.create(), change=False)
        (f.seed / 'default-work').write_text('unrelated default work')
        target = f.commit(f.seed)
        f.git(f.seed, 'push', 'origin', 'main')
        f.git(b['Path'], 'fetch', 'origin', 'main')
        f.git(b['Path'], 'merge', '--ff-only', 'FETCH_HEAD')
        b = f.checkpoint(b, change=False)
        assert b['Checkpoint'] == target and not b.get('ArchiveUnmerged')
        assert f.tick()['Results'][0]['Status'] == 'not eligible'
        f.assert_active()
        print('PASS: following a newer default commit without an observed unmerged checkpoint grants no archive authority', flush=True)

        for kind in ('manual', 'held', 'dirty', 'remote-advance', 'remote-delete', 'squash', 'hidden-edit', 'hidden-enable'):
            mode = 'manual' if kind in ('manual', 'hidden-enable') else 'branch-merged'
            f = Fixture(kind, mode)
            b = f.checkpoint(f.create())
            target = f.merge(b, squash=kind == 'squash')
            if kind == 'held':
                f.cli('lease-repair', '--holder', 'active-writer', 'task')
            elif kind == 'dirty':
                (Path(b['Path']) / 'task-work').write_text('uncommitted')
            elif kind == 'remote-advance':
                f.git(f.remote, 'update-ref', 'refs/heads/' + b['Branch'], target)
            elif kind == 'remote-delete':
                f.git(f.remote, 'update-ref', '-d', 'refs/heads/' + b['Branch'])
            elif kind in ('hidden-edit', 'hidden-enable'):
                flag = '--assume-unchanged' if kind == 'hidden-edit' else '--skip-worktree'
                f.git(b['Path'], 'update-index', flag, '.bench/config.toml')
                changed = 'manual' if kind == 'hidden-edit' else 'branch-merged'
                (Path(b['Path']) / '.bench/config.toml').write_text("[archive]\nwhen='" + changed + "'\n")
                assert not f.git(b['Path'], 'status', '--porcelain')
            result = f.tick()['Results'][0]
            assert result['Status'] in ('not eligible', 'leased', 'blocked'), (kind, result)
            current = f.assert_active()
            if kind == 'held':
                assert current['Lease']['Holder'] == 'active-writer'
            if kind == 'hidden-edit':
                assert 'differs from its checkpoint' in result.get('Error', '')
        print('PASS: manual, held, dirty, advanced/deleted remote branches and squash merges stay active; assume-unchanged/skip-worktree edits cannot authorize mismatched or uncommitted policy', flush=True)

        f = Fixture('pagination', 'manual')
        for key in ('a', 'b', 'c'):
            f.create(key)
        cursor = ''
        for key in ('a', 'b', 'c'):
            page = f.cli('archive-run', '--limit', '1', '--after-key', cursor)
            assert len(page['Results']) == 1 and page['Results'][0]['Key'] == key and page['Next'] == key
            cursor = page['Next']
        page = f.cli('archive-run', '--limit', '1', '--after-key', cursor)
        assert not page['Results'] and not page['Next']
        print('PASS: CLI after-key pagination visits every manual bench once within the requested bound', flush=True)

        f = Fixture('legacy')
        b = f.checkpoint(f.create(binary=args.old_bench), binary=args.old_bench)
        f.merge(b)
        with sqlite3.connect(f.state / 'bench.db') as db:
            assert db.execute('PRAGMA user_version').fetchone()[0] == 11
        f.cli('migrate')
        migrated = f.status()
        assert not migrated.get('CreatedFrom') and not migrated.get('ArchiveUnmerged')
        assert run(f.command('status', 'task', binary=args.old_bench)).returncode != 0
        assert f.tick()['Results'][0]['Status'] == 'not eligible'
        with sqlite3.connect(f.state / 'bench.db') as db:
            assert db.execute('PRAGMA user_version').fetchone()[0] == 12
        f.assert_active()
        print('PASS: genuine schema-11 CLI state migrates to12 without inventing creation/merge proof; old CLI refuses and legacy work remains active', flush=True)

        f = Fixture('interrupted')
        b = f.checkpoint(f.create())
        f.merge(b)
        checkout = Path(b['Path'])
        bulk = checkout / 'bulk'
        bulk.mkdir()
        for index in range(12000):
            (bulk / f'entry-{index:05}').write_text('preserved ignored output')
        daemon = f.daemon()
        wait_for(lambda: (checkout / 'source').stat().st_mode & 0o222 == 0, 'real freeze effect', daemon)
        stop(daemon, kill=True)
        pending = f.status()
        assert pending.get('ArchiveMerge') and pending.get('ArchiveFreezeDigest') and not pending.get('ArchiveFrozenAt'), pending
        assert run(f.command('lease-repair', '--holder', 'competitor', 'task')).returncode != 0
        assert run(f.command('plan-reap', 'task')).returncode != 0
        f.git(f.remote, 'symbolic-ref', 'HEAD', 'refs/heads/unborn')
        daemon = f.daemon()
        wait_for(lambda: bool(f.status().get('ArchiveFrozenAt')), 'restart finishes committed intent', daemon)
        completed = f.status()
        for field in ('ArchivedAt', 'ReapAfter', 'ArchiveMerge', 'ArchiveFreezeDigest'):
            assert completed[field] == pending[field], field
        assert len(list(bulk.iterdir())) == 12000
        assert all(path.stat().st_mode & 0o222 == 0 for path in bulk.iterdir())
        assert not completed.get('ReapedAt')
        stop(daemon)
        assert daemon.returncode == 0
        print('PASS: SIGKILL during actual daemon freeze preserves committed intent; restart finishes the same evidence/deadline/tree despite changed default HEAD, without deleting output', flush=True)
        print('DONE: automatic archive CLI/daemon integration passed', flush=True)
    finally:
        for process in processes:
            if process.poll() is None:
                stop(process, kill=True)
        for log in logs:
            log.close()
        for directory, _, _ in os.walk(root):
            os.chmod(directory, 0o700)
        shutil.rmtree(root)
        cgroups.rmdir()


if __name__ == '__main__':
    main()
