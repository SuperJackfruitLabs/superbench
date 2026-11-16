#!/usr/bin/env python3
"""Verify installed registry/helper binding in disposable Linux namespaces.

This is privileged CI for the registry binding primitives only, not payload
execution, sudo installation or the complete privileged storage lifecycle.
Unsupported namespace/cgroup environments fail; they never silently skip.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import shutil
import signal
import stat
import struct
import subprocess
import sys
import tempfile
import time


TESTS = (
    ('serviceidentity', 'TestInstalledServiceIdentityFixture', (
        'ACTUAL_DAEMON_RESOLVE_OK', 'IDENTICAL_LEAF_REPLACEMENT_REFUSED',
        'ANCESTOR_REPLACEMENT_REFUSED', 'ACTUAL_CREDENTIAL_CHANGE_REFUSED',
        'REGISTERED_UID_WRONG_GID_REFUSED')),
    ('payloadexec', 'TestInstalledHelperPolicyRegistryBindingFixture', (
        'HELPER_BINDING_FIXED_PRIVATE_PUBLIC_POSITIVE',
        'HELPER_BINDING_PRIVATE_MODE_LINK_TYPE_REFUSALS',
        'HELPER_BINDING_MISSING_MISMATCHED_REGISTRY_NO_FRAME',
        'HELPER_BINDING_CHANGED_PROFILE_AND_IDENTICAL_REPLACEMENT_REFUSED')),
)
LIMITS = {'cpu.max': '800000 100000', 'memory.max': str(2 << 30),
          'memory.swap.max': '0', 'pids.max': '512'}


def identity(info):
    return (info.st_dev, info.st_ino, info.st_mode, info.st_uid, info.st_gid,
            info.st_nlink, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def boundary():
    result = {}
    for name in ('/etc', '/etc/superbench', '/etc/superbench/service-identity.json',
                 '/etc/superbench/payload-policy.json'):
        try:
            result[name] = identity(os.lstat(name))
        except FileNotFoundError:
            result[name] = None
    # Private policy contents are deliberately never opened. Even registry
    # contents need not be read to prove these fixtures left host names intact.
    return result


def source_proof(source):
    paths = sorted(source.rglob('*.go')) + [source / 'go.mod', source / 'go.sum']
    result = {}
    for path in paths:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise RuntimeError('CI Go source must be single-link regular files')
        result[str(path.relative_to(source))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def guard():
    if shutil.disk_usage('/var/tmp').free < 2 << 30:
        raise RuntimeError('identity CI requires at least 2 GiB free')
    memory = dict(line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())
    if int(memory['MemAvailable'].split()[0]) < 4 << 20:
        raise RuntimeError('identity CI requires at least 4 GiB available memory')


def static_elf(raw):
    if len(raw) < 64 or raw[:6] != b'\x7fELF\x02\x01':
        raise RuntimeError('identity fixture must be little-endian ELF64')
    offset = struct.unpack_from('<Q', raw, 32)[0]
    size, count = struct.unpack_from('<HH', raw, 54)
    if size != 56 or offset + size * count > len(raw):
        raise RuntimeError('invalid ELF program headers')
    for index in range(count):
        kind, _, start, _, _, length, _, _ = struct.unpack_from('<IIQQQQQQ', raw, offset + index * size)
        if kind == 3:
            raise RuntimeError('dynamic ELF interpreter is forbidden')
        if kind == 2:
            if start + length > len(raw) or length % 16:
                raise RuntimeError('invalid ELF dynamic section')
            for position in range(start, start + length, 16):
                tag, _ = struct.unpack_from('<qQ', raw, position)
                if tag == 1:
                    raise RuntimeError('dynamic ELF dependency is forbidden')
                if tag == 0:
                    break


def capture(candidate, target, uid):
    fd = os.open(candidate, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    with os.fdopen(fd, 'rb') as opened:
        before = os.fstat(fd)
        if (not stat.S_ISREG(before.st_mode) or before.st_uid != uid or before.st_nlink != 1
                or stat.S_IMODE(before.st_mode) != 0o755 or not 0 < before.st_size <= 128 << 20):
            raise RuntimeError('identity fixture binary custody refused')
        raw = opened.read((128 << 20) + 1)
        if (len(raw) != before.st_size or identity(before) != identity(os.fstat(fd))
                or identity(before) != identity(candidate.lstat())):
            raise RuntimeError('identity fixture binary changed during capture')
    static_elf(raw)
    fd = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o755)
    with os.fdopen(fd, 'wb') as output:
        output.write(raw)
        output.flush()
        os.fchmod(output.fileno(), 0o755)
        os.fsync(output.fileno())
    directory = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
    if target.read_bytes() != raw:
        raise RuntimeError('protected identity fixture copy differs')
    return hashlib.sha256(raw).hexdigest()


def inside(binary, selector):
    if os.getuid() != 0 or os.geteuid() != 0:
        raise RuntimeError('identity namespace runner requires root')
    if os.readlink('/proc/self/ns/pid') != os.readlink('/proc/1/ns/pid'):
        raise RuntimeError('identity fixture requires original PID namespace')
    for kind in ('mnt', 'net'):
        original = os.stat('/proc/1/ns/' + kind)
        current = os.stat('/proc/self/ns/' + kind)
        if ((original.st_dev, original.st_ino) == (current.st_dev, current.st_ino)
                or os.environ.get('SUPERBENCH_IDENTITY_ORIGINAL_' + kind.upper()) != os.readlink('/proc/1/ns/' + kind)):
            raise RuntimeError('identity fixture requires verified private ' + kind + ' namespace')
    # The tagged fixture owns its private /etc mount and repeats these checks
    # before any mount operation. There is no PID namespace or host /etc write.
    os.execve(binary, [binary, '-test.run=^' + selector + '$', '-test.v', '-test.timeout=240s'],
              {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'GOMAXPROCS': '8',
               'SUPERBENCH_IDENTITY_FIXTURE': '1',
               'SUPERBENCH_IDENTITY_ORIGINAL_MNT': os.readlink('/proc/1/ns/mnt'),
               'SUPERBENCH_IDENTITY_ORIGINAL_NET': os.readlink('/proc/1/ns/net')})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--user', required=True)
    parser.add_argument('--go', required=True)
    parser.add_argument('--gocache', type=Path, required=True)
    parser.add_argument('--gomodcache', type=Path, required=True)
    args = parser.parse_args()
    if not sys.platform.startswith('linux') or os.getuid() != 0 or os.geteuid() != 0:
        raise SystemExit('identity CI requires a privileged Linux runner')
    account = pwd.getpwnam(args.user)
    if account.pw_uid == 0 or account.pw_gid == 0:
        raise SystemExit('identity compilation requires a non-root user/group')
    for tool in ('unshare', 'python3'):
        if shutil.which(tool) is None:
            raise SystemExit('identity CI requires ' + tool)
    for kind in ('mnt', 'net', 'pid'):
        if os.readlink('/proc/self/ns/' + kind) != os.readlink('/proc/1/ns/' + kind):
            raise SystemExit('identity CI conductor requires original namespaces')
    guard()
    source = args.source.resolve(strict=True)
    original_source, original_host = source_proof(source), boundary()
    fixture = Path(tempfile.mkdtemp(prefix='superbench-identity-ci-', dir='/var/tmp'))
    fixture.chmod(0o755)
    for parent in (fixture, *fixture.parents):
        info = parent.lstat()
        if (not stat.S_ISDIR(info.st_mode) or info.st_uid != 0
                or info.st_mode & 0o022 and not info.st_mode & stat.S_ISVTX):
            raise SystemExit('identity executable ancestry must be root protected')
    work, sealed = fixture / 'ordinary', fixture / 'sealed'
    work.mkdir(mode=0o700)
    os.chown(work, account.pw_uid, account.pw_gid)
    sealed.mkdir(mode=0o755)
    for name in ('home', 'tmp'):
        (work / name).mkdir(mode=0o700)
        os.chown(work / name, account.pw_uid, account.pw_gid)
    env = {'PATH': str(Path(args.go).parent) + ':/usr/local/bin:/usr/bin:/bin',
           'HOME': str(work / 'home'), 'TMPDIR': str(work / 'tmp'),
           'GOCACHE': str(args.gocache), 'GOMODCACHE': str(args.gomodcache),
           'GOTOOLCHAIN': 'local', 'GOPROXY': 'off', 'GOSUMDB': 'off',
           'GOFLAGS': '-p=8', 'GOMAXPROCS': '8', 'GOMEMLIMIT': '1GiB', 'CGO_ENABLED': '0'}
    cgroup = Path('/sys/fs/cgroup') / ('superbench-identity-ci-' + str(os.getpid()) + '-' + os.urandom(6).hex())
    cgroup.mkdir()  # No writable/delegated cgroup means a hard CI failure.
    cgroup_id = identity(cgroup.stat())[:2]
    report = {'fixture': str(fixture), 'scope': 'registry/helper binding only', 'binaries': {}, 'phases': []}
    process, failure = None, None

    def interrupted(number, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        raise InterruptedError('identity CI interrupted')

    def run(command, label, environment, ordinary=False):
        nonlocal process
        guard()
        if {name: (cgroup / name).read_text().strip() for name in LIMITS} != LIMITS:
            raise RuntimeError('identity CI resource limits changed')
        def join():
            if identity(cgroup.stat())[:2] != cgroup_id:
                raise RuntimeError('identity CI cgroup replaced')
            (cgroup / 'cgroup.procs').write_text(str(os.getpid()))
            if ordinary:
                os.setgroups([])
                os.setgid(account.pw_gid)
                os.setuid(account.pw_uid)
        record = {'label': label, 'command': command, 'state': 'spawn_intent'}
        report['phases'].append(record)
        (fixture / (label + '.process.json')).write_text(json.dumps(record) + '\n')
        with (fixture / (label + '.log')).open('xb') as log:
            process = subprocess.Popen(command, cwd=source, env=environment, preexec_fn=join,
                                       stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            record.update(pid=process.pid, start=Path('/proc/' + str(process.pid) + '/stat').read_text().split(') ', 1)[1].split()[19], state='running')
            (fixture / (label + '.process.json')).write_text(json.dumps(record) + '\n')
            record['exit'] = process.wait(timeout=300)
        record['state'] = 'exited'
        (fixture / (label + '.process.json')).write_text(json.dumps(record) + '\n')
        if record['exit'] != 0 or 'populated 0' not in (cgroup / 'cgroup.events').read_text():
            raise RuntimeError('identity CI phase failed or retained a descendant: ' + label)
        return (fixture / (label + '.log')).read_text()

    try:
        signal.signal(signal.SIGTERM, interrupted)
        signal.signal(signal.SIGINT, interrupted)
        for name, value in LIMITS.items():
            (cgroup / name).write_text(value)
        if {name: (cgroup / name).read_text().strip() for name in LIMITS} != LIMITS:
            raise RuntimeError('identity CI cgroup limits unsupported')
        report['limits'] = LIMITS
        for package, selector, markers in TESTS:
            candidate, binary = work / (package + '.test'), sealed / (package + '.test')
            run([args.go, 'test', '-c', '-tags=serviceidentityfixture', '-o', str(candidate),
                 './internal/' + package], package + '-compile', env, ordinary=True)
            report['binaries'][package] = {'sha256': capture(candidate, binary, account.pw_uid)}
            metadata = subprocess.check_output([args.go, 'version', '-m', str(binary)],
                                               env={'PATH': env['PATH'], 'GOTOOLCHAIN': 'local'}, text=True, timeout=10)
            if '\tbuild\tCGO_ENABLED=0\n' not in metadata or '\tbuild\tGOOS=linux\n' not in metadata:
                raise RuntimeError('identity fixture must be a CGO0 Linux binary')
            report['binaries'][package]['build_info'] = metadata
            namespace_env = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin',
                             'SUPERBENCH_IDENTITY_ORIGINAL_MNT': os.readlink('/proc/1/ns/mnt'),
                             'SUPERBENCH_IDENTITY_ORIGINAL_NET': os.readlink('/proc/1/ns/net')}
            log = run(['unshare', '--mount', '--net', '--propagation', 'private', sys.executable,
                       str(Path(__file__).resolve()), '--inside', str(binary), selector],
                      package + '-root', namespace_env)
            if ('--- PASS: ' + selector not in log or '--- FAIL:' in log or '--- SKIP:' in log
                    or '[no tests to run]' in log or any(marker not in log for marker in markers)):
                raise RuntimeError('identity fixture proof markers missing: ' + package)
            if package == 'payloadexec' and '--- PASS: ' + selector + '/FIFO ' not in log:
                raise RuntimeError('precise private-policy FIFO subtest did not pass')
            print(log, end='')
    except BaseException as error:
        failure = repr(error)
    finally:
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        try:
            if identity(cgroup.stat())[:2] != cgroup_id:
                raise RuntimeError('identity CI cgroup identity changed')
            (cgroup / 'cgroup.kill').write_text('1')
            if process is not None:
                process.wait(timeout=10)
            deadline = time.monotonic() + 10
            while time.monotonic() < deadline and 'populated 1' in (cgroup / 'cgroup.events').read_text():
                time.sleep(0.05)
            if 'populated 0' not in (cgroup / 'cgroup.events').read_text():
                raise RuntimeError('identity CI processes remain; preserve fixture')
            report['memory_events'] = (cgroup / 'memory.events').read_text()
            report['memory_peak'] = (cgroup / 'memory.peak').read_text()
            if 'oom_kill 0' not in report['memory_events'] or 'oom_group_kill 0' not in report['memory_events']:
                raise RuntimeError('identity CI encountered OOM')
            for child in sorted((p for p in cgroup.rglob('*') if p.is_dir()), key=lambda p: len(p.parts), reverse=True):
                child.rmdir()
            cgroup.rmdir()
            report['cgroup_empty_removed'] = True
        except BaseException as error:
            failure = failure or repr(error)
            report['cleanup_error'] = repr(error)
        try:
            report['host_before'], report['host_after'] = original_host, boundary()
            report['source_unchanged'] = source_proof(source) == original_source
            if report['host_before'] != report['host_after'] or not report['source_unchanged']:
                failure = failure or 'identity CI changed source or host /etc boundary'
        except BaseException as error:
            failure = failure or repr(error)
            report['postproof_error'] = repr(error)
        report['failure'] = failure
        (fixture / 'result.json').write_text(json.dumps(report, indent=2) + '\n')
    if failure:
        raise SystemExit('identity CI failed; retain ' + str(fixture) + ': ' + failure)
    print('PASS: installed registry/helper binding fixtures; retained evidence:', fixture)


if __name__ == '__main__':
    if len(sys.argv) == 4 and sys.argv[1] == '--inside':
        inside(sys.argv[2], sys.argv[3])
    else:
        main()
