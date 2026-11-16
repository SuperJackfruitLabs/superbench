#!/usr/bin/env python3
"""Privileged CI: actual static helper exec and private causal source overlays.

Requires the same protected runner as installed identity CI. This proves the
credential/descriptor mechanics, not sudo admission or a service lifecycle.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import signal
import subprocess
import sys
import tempfile
import time

# The entry point and child are executed as scripts; never create imported
# control bytecode in the immutable source tree before its RO compiler mount.
sys.dont_write_bytecode = True
SCRIPT = Path(__file__).resolve()
SPEC = importlib.util.spec_from_file_location('identity_ci', SCRIPT.with_name('check-identity-ci.py'))
CI = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CI)
# Match the explicit privileged CI budget; this is not the operator's separate
# eight-core targeted QA wrapper. Keep memory bounded for static helper builds.
LIMITS = dict(CI.LIMITS, **{'cpu.max': '200000 100000'})

def cpu_budget(value):
    if not 1 <= value <= 8:
        raise ValueError('Exec CI CPU budget must be between 1 and 8')
    return {'cpu.max': str(value * 100000) + ' 100000', 'GOMAXPROCS': str(value), 'GOFLAGS': '-p=' + str(value)}
NAMES = ('plain', 'no-groups', 'verify-fault', 'verify-mutant', 'late-fd', 'late-fd-mutant', 'probe')
MARKERS = tuple('EXEC_CAUSAL_' + name.upper().replace('-', '_') + '_PASS' for name in NAMES[:-1])


def source_proof(source):
    result = CI.source_proof(source)
    for name in ('check-payload-exec-ci.py', 'payload-exec-variants.py', 'payload-exec-fixture.py', 'check-identity-ci.py'):
        path = source / 'scripts' / name
        result[str(path.relative_to(source))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result



def compile_inside(config):
    if os.getuid() != 0 or os.geteuid() != 0:
        raise RuntimeError('compiler namespace setup requires root')
    for kind in ('mnt', 'net'):
        current, original = os.stat('/proc/self/ns/' + kind), os.stat('/proc/1/ns/' + kind)
        if (current.st_dev, current.st_ino) == (original.st_dev, original.st_ino):
            raise RuntimeError('compiler requires private namespace')
    if os.readlink('/proc/self/ns/pid') != os.readlink('/proc/1/ns/pid'):
        raise RuntimeError('compiler requires original PID namespace')
    cfg = json.loads(config.read_text())
    subprocess.run(['mount', '--make-rprivate', '/'], check=True)
    subprocess.run(['mount', '-t', 'tmpfs', '-o', 'mode=1777', 'tmpfs', '/tmp'], check=True)
    def alias(name, path, readonly):
        target = Path('/tmp') / name
        target.mkdir()
        subprocess.run(['mount', '--bind', str(path), str(target)], check=True)
        if readonly:
            subprocess.run(['mount', '-o', 'remount,bind,ro', str(target)], check=True)
        return str(target)
    alias('source', cfg['source'], True)
    alias('go', Path(cfg['go']).parent.parent, True)
    alias('modules', cfg['modules'], True)
    alias('work', cfg['work'], False)
    if cfg['lowers']:
        lowers = [alias('lower' + str(i), path, True) for i, path in enumerate(cfg['lowers'])]
        cache_work = Path(cfg['work']) / 'overlay-work'
        cache_work.mkdir(mode=0o700)
        os.chown(cache_work, cfg['uid'], cfg['gid'])
        Path('/tmp/cache').mkdir()
        subprocess.run(['mount', '-t', 'overlay', 'overlay', '-o',
                        'lowerdir=' + ':'.join(lowers) + ',upperdir=' + cfg['cache'] + ',workdir=' + str(cache_work),
                        '/tmp/cache'], check=True)
    else:
        alias('cache', cfg['cache'], False)
    command = ['setpriv', '--reuid=' + str(cfg['uid']), '--regid=' + str(cfg['gid']), '--clear-groups',
               '--no-new-privs', 'bwrap', '--uid', str(cfg['uid']), '--gid', str(cfg['gid']), '--clearenv',
               '--unshare-all', '--share-net', '--die-with-parent', '--ro-bind', '/usr', '/usr',
               '--symlink', 'usr/bin', '/bin', '--symlink', 'usr/sbin', '/sbin', '--symlink', 'usr/lib', '/lib',
               '--symlink', 'usr/lib64', '/lib64', '--proc', '/proc', '--dev', '/dev',
               '--ro-bind', '/tmp/source', '/src', '--ro-bind', '/tmp/go', '/go',
               '--ro-bind', '/tmp/modules', '/modules', '--bind', '/tmp/work', '/work',
               '--bind', '/tmp/work/home', '/home/test', '--bind', '/tmp/work/tmp', '/tmp',
               '--bind', '/tmp/cache', '/cache']
    environment = {'PATH': '/go/bin:/usr/bin:/bin', 'HOME': '/home/test', 'TMPDIR': '/tmp', 'GOROOT': '/go',
                   'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off', 'GOENV': 'off', 'GOWORK': 'off',
                   'GOMODCACHE': '/modules', 'GOCACHE': '/cache', 'GOMAXPROCS': str(cfg['cpus']),
                   'GOFLAGS': '-p=' + str(cfg['cpus']), 'GOMEMLIMIT': '1GiB', 'CGO_ENABLED': '0'}
    for name, value in environment.items():
        command += ['--setenv', name, value]
    command += ['--chdir', '/src', '/usr/bin/python3', '-I', '-B', '/src/scripts/payload-exec-variants.py',
                '/src', '/work/variants', '/go/bin/go']
    os.execv('/usr/bin/setpriv', command)

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--cpus', type=int, choices=range(1, 9), default=2)
    parser.add_argument('--cache-lower', action='append', default=[])
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--user', required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--gocache', required=True)
    parser.add_argument('--gomodcache', required=True)
    args = parser.parse_args()
    budget = cpu_budget(args.cpus)
    limits = dict(LIMITS, **{'cpu.max': budget['cpu.max']})
    CI.LIMITS = limits
    CI.preflight()
    for tool in ('mount', 'umount', 'setpriv', 'bwrap'):
        if CI.shutil.which(tool) is None:
            raise SystemExit('Exec CI requires ' + tool)
    account = pwd.getpwnam(args.user)
    if account.pw_uid == 0 or account.pw_gid == 0:
        raise SystemExit('Exec fixture requires an actual ordinary compilation identity')
    source = args.source.resolve(strict=True)
    original, host = source_proof(source), CI.boundary()
    donors = {path: CI.identity(Path(path).stat())[:6] for path in (args.go, args.gomodcache, *args.cache_lower)}
    trial = Path(tempfile.mkdtemp(prefix='superbench-exec-ci-', dir='/var/tmp'))
    trial.chmod(0o755)
    for parent in (trial, *trial.parents):
        info = parent.lstat()
        if info.st_uid != 0 or info.st_mode & 0o022 and not info.st_mode & 0o1000:
            raise RuntimeError('Exec fixture ancestry is not protected')
    work, sealed, fixture = (trial / name for name in ('ordinary', 'sealed', 'fixture'))
    work.mkdir(mode=0o700)
    os.chown(work, account.pw_uid, account.pw_gid)
    sealed.mkdir(mode=0o755)
    fixture.mkdir(mode=0o755)
    for name in ('home', 'tmp'):
        (work / name).mkdir(mode=0o700)
        os.chown(work / name, account.pw_uid, account.pw_gid)
    # Root executes only the protected control copy, never compiler-owned files.
    driver = sealed / 'driver.py'
    with driver.open('xb') as output:
        output.write((SCRIPT.with_name('payload-exec-fixture.py')).read_bytes())
        output.flush()
        os.fchmod(output.fileno(), 0o644)
        os.fsync(output.fileno())
    driver_hash = hashlib.sha256(driver.read_bytes()).hexdigest()
    env = {'PATH': str(Path(args.go).parent) + ':/usr/bin:/bin', 'HOME': str(work / 'home'),
           'TMPDIR': str(work / 'tmp'), 'GOCACHE': args.gocache, 'GOMODCACHE': args.gomodcache,
           'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off', 'CGO_ENABLED': '0',
           'GOMAXPROCS': str(args.cpus), 'GOFLAGS': '-p=' + str(args.cpus), 'GOMEMLIMIT': '1GiB'}
    cgroup = Path('/sys/fs/cgroup') / ('superbench-exec-ci-' + str(os.getpid()) + '-' + os.urandom(6).hex())
    cgroup.mkdir()
    physical = CI.identity(cgroup.stat())[:2]
    report = {'scope': 'actual Exec credentials and FDs, no installed lifecycle', 'trial': str(trial),
              'binaries': {}, 'phases': [], 'driver_sha256': driver_hash, 'limits': limits, 'donor_anchors': donors}
    compile_config = trial / 'compile.json'
    compile_config.write_text(json.dumps({'source': str(source), 'work': str(work), 'go': args.go,
        'cache': args.gocache, 'modules': args.gomodcache, 'lowers': args.cache_lower,
        'uid': account.pw_uid, 'gid': account.pw_gid, 'cpus': args.cpus}) + '\n')
    compile_config.chmod(0o600)
    process, failure = None, None

    def interrupt(number, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        raise InterruptedError('Exec CI interrupted')

    def run(command, label, environment, timeout=300):
        nonlocal process
        CI.guard()
        if {name: (cgroup / name).read_text().strip() for name in limits} != limits:
            raise RuntimeError('Exec CI limits changed')
        def join():
            if CI.identity(cgroup.stat())[:2] != physical:
                raise RuntimeError('Exec CI cgroup replaced')
            (cgroup / 'cgroup.procs').write_text(str(os.getpid()))
        record = {'phase': label, 'state': 'spawn_intent', 'command': command}
        report['phases'].append(record)
        record_path = trial / (label + '.process.json')
        record_path.write_text(json.dumps(record) + '\n')
        with (trial / (label + '.log')).open('xb') as log:
            process = subprocess.Popen(command, cwd=source, env=environment, preexec_fn=join,
                                       stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            record.update(pid=process.pid, start=Path('/proc/' + str(process.pid) + '/stat').read_text().split(') ', 1)[1].split()[19], state='running')
            record_path.write_text(json.dumps(record) + '\n')
            record['exit'] = process.wait(timeout=timeout)
        record['state'] = 'exited'
        record_path.write_text(json.dumps(record) + '\n')
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and ((cgroup / 'cgroup.procs').read_text().strip() or 'populated 0' not in (cgroup / 'cgroup.events').read_text()):
            time.sleep(0.05)
        if record['exit'] != 0 or (cgroup / 'cgroup.procs').read_text().strip() or 'populated 0' not in (cgroup / 'cgroup.events').read_text():
            raise RuntimeError('Exec CI phase failed or retained descendants: ' + label)
        return (trial / (label + '.log')).read_text()

    try:
        signal.signal(signal.SIGTERM, interrupt)
        signal.signal(signal.SIGINT, interrupt)
        for name, value in limits.items():
            (cgroup / name).write_text(value)
        run(['unshare', '--mount', '--net', '--propagation', 'private', sys.executable, '-I', '-B',
             str(SCRIPT), '--compile-inside', str(compile_config)], 'compile',
            {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'}, timeout=900)
        for name in NAMES:
            target = sealed / (name + '.bin')
            digest = CI.capture(work / 'variants' / (name + '.bin'), target, account.pw_uid)
            metadata = subprocess.check_output([args.go, 'version', '-m', str(target)], env=env, text=True, timeout=10)
            if '\tbuild\tCGO_ENABLED=0\n' not in metadata or '\tbuild\tGOOS=linux\n' not in metadata:
                raise RuntimeError('Exec fixture is not a CGO0 Linux executable')
            report['binaries'][name] = {'sha256': digest, 'build_info': metadata}
        if source_proof(source) != original or hashlib.sha256(driver.read_bytes()).hexdigest() != driver_hash:
            raise RuntimeError('Exec fixture source/control changed')
        scopes = cgroup / 'scopes'
        scopes.mkdir()
        namespace_env = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin',
                         'SUPERBENCH_EXEC_ORIGINAL_MNT': os.readlink('/proc/1/ns/mnt'),
                         'SUPERBENCH_EXEC_ORIGINAL_NET': os.readlink('/proc/1/ns/net')}
        log = run(['unshare', '--mount', '--net', '--propagation', 'private', sys.executable,
                   str(driver), str(fixture), str(sealed), str(scopes), str(account.pw_uid), str(account.pw_gid)], 'root', namespace_env)
        if any(marker not in log for marker in MARKERS) or 'ACTUAL_EXEC_AND_THREE_CAUSAL_MUTATIONS_PASS' not in log:
            raise RuntimeError('actual Exec causal markers missing')
        print(log, end='')
    except BaseException as error:
        failure = repr(error)
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        try:
            if CI.identity(cgroup.stat())[:2] != physical:
                raise RuntimeError('Exec CI cleanup cgroup changed')
            (cgroup / 'cgroup.kill').write_text('1')
            if process is not None:
                process.wait(timeout=10)
            deadline = time.monotonic() + 10
            while 'populated 0' not in (cgroup / 'cgroup.events').read_text() and time.monotonic() < deadline:
                time.sleep(0.05)
            if (cgroup / 'cgroup.procs').read_text().strip() or 'populated 0' not in (cgroup / 'cgroup.events').read_text():
                raise RuntimeError('Exec CI processes remain; retain fixture')
            report['memory_events'] = (cgroup / 'memory.events').read_text()
            if any(int(line.split()[1]) for line in report['memory_events'].splitlines() if line.split()[0] in ('oom', 'oom_kill', 'oom_group_kill')):
                raise RuntimeError('Exec CI OOM')
            for child in sorted((p for p in cgroup.rglob('*') if p.is_dir()), key=lambda p: len(p.parts), reverse=True):
                child.rmdir()
            cgroup.rmdir()
            report['cgroup_empty_removed'] = True
        except BaseException as error:
            report['cleanup_error'] = repr(error)
            failure = failure or repr(error)
        try:
            report['donors_unchanged'] = {path: CI.identity(Path(path).stat())[:6] for path in donors} == donors
            report['source_unchanged'] = source_proof(source) == original
            report['host_unchanged'] = CI.boundary() == host
            if not report['source_unchanged'] or not report['host_unchanged'] or not report['donors_unchanged']:
                raise RuntimeError('Exec CI source or host boundary changed')
        except BaseException as error:
            failure = failure or repr(error)
        report['failure'] = failure
        (trial / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
    if failure:
        raise SystemExit('Exec CI failed; retained ' + str(trial) + ': ' + failure)
    print('PASS: actual static helper Exec and causal mutations; retained:', trial)


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--compile-inside':
        compile_inside(Path(sys.argv[2]))
    else:
        main()
