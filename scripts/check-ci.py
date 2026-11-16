#!/usr/bin/env python3
"""Run the ordinary suite without root and verify the separate root refusal."""
import argparse
import hashlib
import os
from pathlib import Path
import pwd
import shutil
import stat
import subprocess
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--user', default='nobody')
    parser.add_argument('--gocache')
    parser.add_argument('--gomodcache')
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit('CI conductor requires root; the ordinary suite must not run as root')
    source = Path(__file__).resolve().parent.parent
    # Refuse unsupported privilege/delegation before any ordinary fixture or build.
    subprocess.run([sys.executable, str(source / 'scripts/check-identity-ci.py'),
                    '--preflight'], check=True, timeout=30)
    account = pwd.getpwnam(args.user)
    if account.pw_uid == 0 or account.pw_gid == 0:
        raise SystemExit('ordinary CI requires a distinct non-root user and group')
    if shutil.disk_usage('/var/tmp').free < 2 << 30:
        raise SystemExit('at least 2 GiB free is required before CI compilation')
    go = shutil.which('go')
    if go is None:
        raise SystemExit('the workflow must provide its pinned Go toolchain')
    for name in ('superbench-check-bin', 'superbench-check-quota', 'superbench-check-daemon',
                 'superbench-check-payload'):
        if os.path.lexists('/tmp/' + name):
            raise SystemExit('CI binary output already exists; use an isolated job tmp directory')
    fixture = Path(tempfile.mkdtemp(prefix='superbench-ci-', dir='/var/tmp'))
    # Physical-path inspectors open each ancestor; private children stay 0700.
    fixture.chmod(0o755)
    work, control = fixture / 'ordinary', fixture / 'control'
    work.mkdir(mode=0o700)
    os.chown(work, account.pw_uid, account.pw_gid)
    control.mkdir(mode=0o700)

    def owned(name):
        path = work / name
        path.mkdir(mode=0o700)
        os.chown(path, account.pw_uid, account.pw_gid)
        return path

    def cache(name, supplied):
        if supplied is None:
            return owned(name)
        path = Path(supplied)
        info = path.lstat()
        if not path.is_absolute() or path.resolve() != path or not stat.S_ISDIR(info.st_mode) or info.st_uid != account.pw_uid:
            raise SystemExit('a reused CI cache must be an existing physical directory owned by the test user')
        return path

    checkout = owned('source')
    task_home, task_tmp = owned('home'), owned('tmp')
    build_cache, module_cache = cache('build-cache', args.gocache), cache('modules', args.gomodcache)

    def drop():
        os.setgroups([])
        os.setgid(account.pw_gid)
        os.setuid(account.pw_uid)

    env = {'PATH': str(Path(go).parent) + ':/usr/local/bin:/usr/bin:/bin',
           'HOME': str(task_home), 'TMPDIR': str(task_tmp), 'GOCACHE': str(build_cache),
           'GOMODCACHE': str(module_cache), 'GOTOOLCHAIN': 'local', 'GOFLAGS': '-p=8',
           'GOMAXPROCS': '8', 'CI': 'true'}
    for name in ('GOPROXY', 'GOSUMDB', 'GOMEMLIMIT'):
        if name in os.environ:
            env[name] = os.environ[name]
    # Never expose checkout credentials or ignored operator files to the tests.
    with tempfile.TemporaryFile() as archive:
        subprocess.run(['git', '-c', 'safe.directory=' + str(source), 'archive', '--format=tar', 'HEAD'], cwd=source, stdout=archive, check=True)
        archive.seek(0)
        subprocess.run(['tar', '-xf', '-', '--no-same-owner', '-C', str(checkout)], stdin=archive,
                       env=env, preexec_fn=drop, check=True)
    print('ordinary suite UID:', account.pw_uid, 'fixture:', fixture, flush=True)
    subprocess.run(['id'], cwd=checkout, env=env, preexec_fn=drop, check=True)
    subprocess.run([go, 'version'], cwd=checkout, env=env, preexec_fn=drop, check=True)
    subprocess.run(['sh', 'scripts/check.sh'], cwd=checkout, env=env, preexec_fn=drop, check=True)
    candidate = work / 'root-reap.test'
    subprocess.run([go, 'test', '-c', '-o', str(candidate), './internal/reapsecurity'],
                   cwd=checkout, env=env, preexec_fn=drop, check=True)
    fd = os.open(candidate, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as opened:
        info = os.fstat(opened.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != account.pw_uid or info.st_nlink != 1 or info.st_size > 128 << 20:
            raise SystemExit('root refusal test binary is not a bounded owned regular file')
        raw = opened.read((128 << 20) + 1)
        after = os.fstat(opened.fileno())
        named = candidate.lstat()
        identity = lambda value: (value.st_dev, value.st_ino, value.st_mode, value.st_uid,
                                  value.st_nlink, value.st_size, value.st_mtime_ns, value.st_ctime_ns)
        if identity(info) != identity(after) or identity(info) != identity(named):
            raise SystemExit('root refusal test binary identity changed during capture')
        if len(raw) != info.st_size:
            raise SystemExit('root refusal test binary changed during capture')
    protected = control / 'root-reap.test'
    with protected.open('xb') as output:
        output.write(raw)
    protected.chmod(0o700)
    root_tmp = control / 'tmp'
    root_tmp.mkdir(mode=0o700)
    with (control / 'root-refusal.log').open('xb') as log:
        result = subprocess.run([str(protected), '-test.run=^TestRootCannotReapEvenAnOtherwiseValidOwnedTree$',
                                 '-test.v', '-test.timeout=30s'], cwd=control,
                                env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'TMPDIR': str(root_tmp),
                                     'SUPERBENCH_ROOT_REAP_TEST': '1'}, stdout=log,
                                stderr=subprocess.STDOUT, timeout=40)
    log = (control / 'root-refusal.log').read_bytes()
    if result.returncode != 0 or b'--- PASS: TestRootCannotReapEvenAnOtherwiseValidOwnedTree' not in log or b'SKIP' in log:
        raise SystemExit('the explicit effective-UID-zero refusal test failed; retain fixture ' + str(fixture))
    if list(root_tmp.iterdir()):
        raise SystemExit('the root refusal fixture retained unexpected temporary entries')
    print(log.decode('utf-8'), end='')
    print('root refusal binary SHA256:', hashlib.sha256(raw).hexdigest())
    print('PASS: ordinary unprivileged suite and explicit root refusal; fixture retained:', fixture)
    # This is a separate, mandatory privileged registry-binding check. The
    # script comes from the trusted CI checkout, never the ordinary writable copy.
    subprocess.run([shutil.which('python3'), str(source / 'scripts/check-identity-ci.py'),
                    '--source', str(checkout), '--user', args.user, '--go', go,
                    '--gocache', str(build_cache), '--gomodcache', str(module_cache)], check=True)


if __name__ == '__main__':
    main()
