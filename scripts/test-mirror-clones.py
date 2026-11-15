#!/usr/bin/env python3
"""Owned Linux drill: managed clone fencing and reviewed staging collection."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--bench', required=True)
    args = parser.parse_args()
    bench = str(Path(args.bench).resolve(strict=True))
    assert sys.platform == 'linux' and os.geteuid() == 0, 'disposable Linux root required'
    root = Path(tempfile.mkdtemp(prefix='superbench-mirror-clones.', dir='/var/tmp'))
    scopes = Path('/sys/fs/cgroup') / root.name
    scopes.mkdir()
    controllers = []
    handles = []
    marker = root / 'writer.json'
    token = 'owned-' + uuid.uuid4().hex
    evidence = {}

    def command(words, expected=0, env=None):
        p = subprocess.run(list(map(str, words)), capture_output=True, text=True,
                           timeout=90, env=env)
        if expected == 0:
            assert p.returncode == 0, (words, p.stdout, p.stderr)
        else:
            assert p.returncode != 0, ('unexpected success', words, p.stdout)
        return p

    def good(words):
        return command(words).stdout.strip()

    def process_alive(record):
        try:
            fields = Path('/proc/' + str(record['pid']) + '/stat').read_text().split(') ')[1].split()
            return fields[0] not in ('Z', 'X') and fields[19] == record['start']
        except FileNotFoundError:
            return False

    def ready(path):
        deadline = time.monotonic() + 30
        while not path.exists():
            assert time.monotonic() < deadline, 'missing owned marker ' + str(path)
            time.sleep(.01)

    def clone_records(state):
        return [json.loads(p.read_text()) for p in (state / 'mirror-stages').glob('*.json')
                if not p.name.endswith('-root.json')]

    def admin(state, data, op, clone_id, extra=(), expected=0):
        return command([bench, 'mirror-clones', op, '--state', state, '--data', data,
                        *extra, clone_id], expected)

    try:
        seed, remote = root / 'seed', root / 'remote.git'
        good(['git', 'init', '-b', 'main', seed])
        (seed / 'file').write_text('base\n')
        good(['git', '-C', seed, 'add', 'file'])
        good(['git', '-C', seed, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
              'commit', '-m', 'base'])
        good(['git', 'clone', '--bare', seed, remote])

        # Normal managed publication removes only its empty stage and own metadata.
        state, data = root / 'normal-state', root / 'normal-data'
        good([bench, 'create-cold', '--state', state, '--data', data,
              '--cgroup-root', scopes, '--repo', remote, 'normal'])
        assert not list((data / 'mirrors').glob('.clone-*'))
        assert not list((state / 'mirror-stages').iterdir())
        assert not list((state / 'mirror-stage-collections').iterdir())
        assert (data / 'benches' / 'normal' / 'file').read_text() == 'base\n'
        print('PASS: managed publication retires only its empty stage and exact own metadata', flush=True)

        # A successful cooperative clone is explicitly insufficient fencing evidence.
        state_c, data_c = root / 'cooperative-state', root / 'cooperative-data'
        good([bench, 'create-cold', '--state', state_c, '--data', data_c,
              '--cgroup-root', root / 'unavailable-control', '--repo', remote, 'cooperative'])
        cooperative = clone_records(state_c)
        assert len(cooperative) == 1 and cooperative[0]['ControlRoot'] == ''
        admin(state_c, data_c, 'inspect', cooperative[0]['ID'], expected=1)
        assert Path(cooperative[0]['Stage']).is_dir()
        print('PASS: cooperative clone remains protected despite command success', flush=True)

        # An owned remote helper blocks clone and leaves a setsid writer behind.
        tools = root / 'tools'
        tools.mkdir()
        worker = root / 'writer.py'
        worker.write_text('''import json,os,sys,time
from pathlib import Path
marker,stage,token=map(str,sys.argv[1:4])
fields=Path('/proc/self/stat').read_text().split(') ')[1].split()
temporary=Path(marker+'.tmp')
temporary.write_text(json.dumps({'pid':os.getpid(),'start':fields[19],'token':token,'stage':stage,'cgroup':Path('/proc/self/cgroup').read_text()}))
os.replace(temporary,marker)
while True:
 Path(stage,'writer-output').write_text(token)
 time.sleep(.02)
''')
        state_i, data_i = root / 'interrupted-state', root / 'interrupted-data'
        helper = tools / 'git-remote-sbowned'
        helper.write_text('#!' + sys.executable + '\n' +
                          'import json,subprocess,time\nfrom pathlib import Path\n' +
                          'state=Path(' + repr(str(state_i)) + ')\n' +
                          "files=[p for p in (state/'mirror-stages').glob('*.json') if not p.name.endswith('-root.json')]\n" +
                          'assert len(files)==1\nstage=json.loads(files[0].read_text())["Stage"]\n' +
                          'subprocess.Popen([' + repr(sys.executable) + ',' + repr(str(worker)) + ',' +
                          repr(str(marker)) + ',stage,' + repr(token) + '],start_new_session=True,' +
                          'stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)\n' +
                          'while True:time.sleep(60)\n')
        helper.chmod(0o700)
        environment = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ['PATH'])
        log = (root / 'interrupted-controller.log').open('w')
        handles.append(log)
        p = subprocess.Popen([bench, 'create-cold', '--state', str(state_i), '--data', str(data_i),
                              '--cgroup-root', str(scopes), '--repo', 'sbowned::source', 'interrupted'],
                             env=environment, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
        controllers.append(p)
        ready(marker)
        record = json.loads(marker.read_text())
        assert record['token'] == token and ('/' + scopes.name + '/') in record['cgroup']
        stage = Path(record['stage'])
        ready(stage / 'writer-output')
        p.kill()
        p.wait(timeout=10)
        assert process_alive(record), 'controller exit was incorrectly treated as fencing'
        assert (stage / 'writer-output').read_text() == token
        intent = clone_records(state_i)
        assert len(intent) == 1 and intent[0]['ControlRoot'] == str(scopes)
        clone_id = intent[0]['ID']
        first = admin(state_i, data_i, 'inspect', clone_id)
        assert not process_alive(record), 'managed reconciliation left the detached writer alive'
        assert not list((state_i / 'git-controls').glob('*.json'))
        print('PASS: controller SIGKILL leaves a setsid writer; managed inspection fences it before planning', flush=True)

        outside = root / 'outside-sentinel'
        outside.write_text('preserve outside\n')
        (stage / 'link').symlink_to(outside)
        plan_raw = admin(state_i, data_i, 'inspect', clone_id).stdout
        plan = json.loads(plan_raw)
        assert plan['Intent']['ID'] == clone_id and not plan.get('Published')
        plan_file = root / 'collection.json'
        plan_file.write_text(plan_raw)
        plan_file.chmod(0o600)
        admin(state_i, data_i, 'collect', clone_id,
              ['--plan', plan_file, '--approve', '0' * 64], expected=1)
        assert (stage / 'writer-output').read_text() == token
        changed = stage / 'new-after-review'
        changed.write_text('preserve changed data')
        admin(state_i, data_i, 'collect', clone_id,
              ['--plan', plan_file, '--approve', plan['Digest']], expected=1)
        assert changed.read_text() == 'preserve changed data' and (stage / 'link').is_symlink()
        changed.unlink()  # Only this fixture's exact injected file is removed.
        # Pre-effect refusal does not make its old observation current again.
        plan_raw = admin(state_i, data_i, 'inspect', clone_id).stdout
        plan = json.loads(plan_raw)
        plan_file.write_text(plan_raw)
        result = json.loads(admin(state_i, data_i, 'collect', clone_id,
                                 ['--plan', plan_file, '--approve', plan['Digest']]).stdout)
        assert result['Completed'] and result['ID'] == clone_id and not stage.exists()
        assert outside.read_text() == 'preserve outside\n'
        assert not clone_records(state_i)
        repeated = json.loads(admin(state_i, data_i, 'collect', clone_id,
                                   ['--plan', plan_file, '--approve', plan['Digest']]).stdout)
        assert repeated == result
        print('PASS: wrong/changed approvals preserve data; exact collection/replay removes only approved staging entries', flush=True)

        stage.mkdir(mode=0o700)
        admin(state_i, data_i, 'collect', clone_id,
              ['--plan', plan_file, '--approve', plan['Digest']], expected=1)
        review_raw = admin(state_i, data_i, 'inspect-root-removal', clone_id).stdout
        review = json.loads(review_raw)
        review_file = root / 'root-review.json'
        review_file.write_text(review_raw)
        review_file.chmod(0o600)
        repaired = json.loads(admin(state_i, data_i, 'reconcile-root-removal', clone_id,
                                   ['--plan', review_file, '--approve', review['Digest']]).stdout)
        assert repaired['Completed'] and not stage.exists()
        admin(state_i, data_i, 'forget', clone_id, ['--approve', plan['Digest']])
        assert not list((state_i / 'mirror-stage-collections').iterdir())
        admin(state_i, data_i, 'collect', clone_id,
              ['--plan', plan_file, '--approve', plan['Digest']], expected=1)
        assert outside.read_text() == 'preserve outside\n'
        print('PASS: returned root refuses old approval; fresh empty-root disposition and exact metadata retirement do not revive it', flush=True)
        evidence = {'results': 'PASS', 'detached_writer': record, 'clone_id': clone_id,
                    'plan_digest': plan['Digest'], 'outside_preserved': True}
        print('UNCOVERED: shared package-cache GC, legacy/cooperative adoption, cross-host recovery and whole Phase 1', flush=True)
    finally:
        for p in controllers:
            if p.poll() is None:
                p.kill()
                p.wait(timeout=10)
        # This exact cgroup root was exclusively created by this fixture; its
        # controller never joins it. No host service or foreign scope is touched.
        (scopes / 'cgroup.kill').write_text('1')
        deadline = time.monotonic() + 10
        while 'populated 1' in (scopes / 'cgroup.events').read_text():
            assert time.monotonic() < deadline, 'owned fixture scope did not empty'
            time.sleep(.01)
        for child in scopes.iterdir():
            if child.is_dir():
                assert len(child.name) == 32 and all(c in '0123456789abcdef' for c in child.name)
                child.rmdir()
        scopes.rmdir()
        for f in handles:
            f.close()
        evidence['cleanup'] = {'cgroup_absent': not scopes.exists(), 'trial': str(root)}
        location = os.environ.get('SUPERBENCH_MIRROR_CLONE_EVIDENCE')
        if location:
            destination = Path(location)
            assert destination.is_dir() and destination.resolve() != root.resolve()
            (destination / 'summary.json').write_text(json.dumps(evidence, indent=2))
            for source in root.glob('*.json'):
                shutil.copy2(source, destination / source.name)
            shutil.copy2(root / 'interrupted-controller.log', destination / 'interrupted-controller.log') if (root / 'interrupted-controller.log').exists() else None
        shutil.rmtree(root)
        assert not root.exists() and not scopes.exists()


if __name__ == '__main__':
    main()
