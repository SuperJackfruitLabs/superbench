#!/usr/bin/env python3
"""Actual static helper exec and causal variants, only in an owned namespace.

No sudo installation, service activation or host configuration is performed.
The outer conductor owns the resource cgroup and kills it on every exit path.
"""
import hashlib
import json
import os
from pathlib import Path
import selectors
import stat
import struct
import subprocess
import sys
import time

PAYLOAD, PAYLOAD_GROUP = 61002, 61004


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def raw(value):
    return json.dumps(value, separators=(',', ':'), ensure_ascii=False).encode()


def directory(path, uid=0, gid=0, mode=0o755):
    path.mkdir(mode=mode)
    os.chown(path, uid, gid)
    path.chmod(mode)


def write(path, value, uid=0, gid=0, mode=0o600):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, 'wb') as output:
        output.write(value)
        os.fchown(output.fileno(), uid, gid)
        os.fchmod(output.fileno(), mode)
        output.flush()
        os.fsync(output.fileno())


def node(path):
    info = path.stat()
    return {'Device': info.st_dev, 'Inode': info.st_ino}


def read_output(process, outputs):
    result = [bytearray(), bytearray()]
    with selectors.DefaultSelector() as selector:
        for index, fd in enumerate(outputs):
            os.set_blocking(fd, False)
            selector.register(fd, selectors.EVENT_READ, index)
        deadline = time.monotonic() + 10
        while selector.get_map():
            require(time.monotonic() < deadline, 'helper output deadline exceeded')
            for event, _ in selector.select(0.05):
                data = os.read(event.fd, 65537)
                if not data:
                    selector.unregister(event.fd)
                else:
                    result[event.data].extend(data)
                    require(len(result[event.data]) <= 65536, 'helper output bound exceeded')
    return process.wait(timeout=2), *(bytes(value) for value in result)


def observe(output, canary, stdio, allow_canary=False):
    value = json.loads(output)
    require(set(value) == {'Status', 'Descriptors'}, 'unexpected probe report')
    expected = {'Uid': ' '.join([str(PAYLOAD)] * 4), 'Gid': ' '.join([str(PAYLOAD_GROUP)] * 4),
                'Groups': '', 'NoNewPrivs': '1'}
    expected.update({key: '0000000000000000' for key in ('CapInh', 'CapPrm', 'CapEff', 'CapBnd', 'CapAmb')})
    require(value['Status'] == expected, 'actual exec credentials differ')
    require(isinstance(value['Descriptors'], list) and len(value['Descriptors']) <= 256, 'invalid FD inventory')
    seen, found = set(), False
    for entry in value['Descriptors']:
        require(set(entry) == {'FD', 'Device', 'Inode', 'Mode', 'Target'}, 'invalid FD entry')
        fd = entry['FD']
        require(type(fd) is int and fd >= 0 and fd not in seen, 'duplicate FD')
        seen.add(fd)
        if fd <= 2:
            require(stat.S_ISFIFO(entry['Mode']) and (entry['Device'], entry['Inode']) == stdio[fd], 'stdio is not the exact daemon-created pipe')
        elif (entry['Device'], entry['Inode']) == canary:
            require(allow_canary, 'root canary survived final descriptor sweep')
            found = True
        else:
            require(entry['Target'] in ('anon_inode:[eventpoll]', 'anon_inode:[eventfd]'), 'unknown inherited FD survived exec')
    require({0, 1, 2} <= seen, 'stdio missing')
    require(found == allow_canary, 'causal late-FD mutation not observed')
    print('PROBE_KERNEL_OBSERVATION ' + json.dumps(value, sort_keys=True), flush=True)


def main():
    root, sealed, cgroup = (Path(value).resolve(strict=True) for value in sys.argv[1:4])
    daemon, daemon_group = (int(value) for value in sys.argv[4:6])
    require(0 < daemon < 2**32 - 1 and 0 < daemon_group < 2**32 - 1 and daemon != PAYLOAD and daemon_group != PAYLOAD_GROUP, 'invalid daemon identity')
    require(os.getuid() == os.geteuid() == 0, 'root fixture required')
    for kind in ('mnt', 'net'):
        current, original = os.stat('/proc/self/ns/' + kind), os.stat('/proc/1/ns/' + kind)
        require((current.st_dev, current.st_ino) != (original.st_dev, original.st_ino), 'private namespace required')
        require(os.environ['SUPERBENCH_EXEC_ORIGINAL_' + kind.upper()] == os.readlink('/proc/1/ns/' + kind), 'original namespace mismatch')
    require(os.readlink('/proc/self/ns/pid') == os.readlink('/proc/1/ns/pid'), 'original PID namespace required')
    require(not list(root.iterdir()), 'fresh fixture root required')
    # Do not reinterpret an existing account or live process as our payload.
    import pwd
    import grp
    try:
        pwd.getpwuid(PAYLOAD)
    except KeyError:
        pass
    else:
        raise RuntimeError('fixture payload UID already has an account')
    try:
        grp.getgrgid(PAYLOAD_GROUP)
    except KeyError:
        pass
    else:
        raise RuntimeError('fixture payload GID already has an account')
    for entry in Path('/proc').iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            process_dir = os.open(entry, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC)
        except FileNotFoundError:
            require(not entry.exists(), 'process inventory changed ambiguously')
            continue
        try:
            try:
                status = os.open('status', os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=process_dir)
                try:
                    data = os.read(status, 65537)
                finally:
                    os.close(status)
            except (FileNotFoundError, ProcessLookupError):
                require(not entry.exists(), 'process credentials could not be verified')
                continue
            require(len(data) <= 65536, 'process status exceeds inventory bound')
            credentials = {}
            for line in data.decode('ascii').splitlines():
                key, separator, value = line.partition(':')
                if separator and key in ('Uid', 'Gid', 'Groups'):
                    require(key not in credentials, 'duplicate process credential field')
                    values = value.split()
                    require(all(item.isdecimal() for item in values), 'malformed process credentials')
                    credentials[key] = [int(item) for item in values]
            require(set(credentials) == {'Uid', 'Gid', 'Groups'} and len(credentials['Uid']) == len(credentials['Gid']) == 4,
                    'incomplete process credentials')
            require(PAYLOAD not in credentials['Uid'], 'fixture payload UID has a live process')
            require(PAYLOAD_GROUP not in credentials['Gid'] + credentials['Groups'], 'fixture payload GID has a live process')
        finally:
            os.close(process_dir)
    machine = Path('/etc/machine-id').read_bytes()
    require(0 < len(machine) < 256, 'invalid host machine ID')
    directory(root / 'etc'); directory(root / 'etc/superbench')
    write(root / 'etc/machine-id', machine, mode=0o644)
    directory(root / 'run')
    write(root / 'run/superbench-exec-fixture-canary', b'nonsecret root-only fixture canary')
    canary_info = (root / 'run/superbench-exec-fixture-canary').stat()
    canary = (canary_info.st_dev, canary_info.st_ino)
    directory(root / 'data'); directory(root / 'data/benches')
    directory(root / 'state', daemon, daemon_group, 0o700)
    directory(root / 'state/payload-layouts', daemon, daemon_group, 0o700)
    bench = root / 'data/benches/fixture'
    directory(bench, PAYLOAD, PAYLOAD_GROUP, 0o700)
    for part in ('a', '.superbench-git', '.superbench-git/a', '.superbench-runtime',
                 '.superbench-runtime/a', '.superbench-runtime/a/home', '.superbench-runtime/a/tmp',
                 '.superbench-runtime/.root', '.superbench-runtime/.root/home', '.superbench-runtime/.root/tmp'):
        directory(bench / part, PAYLOAD, PAYLOAD_GROUP, 0o700)
    policy = {'DaemonUID': daemon, 'DaemonGID': daemon_group, 'PayloadUID': PAYLOAD, 'PayloadGID': PAYLOAD_GROUP,
              'DataRoot': str(root / 'data'), 'StateRoot': str(root / 'state'), 'CgroupRoot': str(cgroup)}
    digest = hashlib.sha256(raw(policy)).hexdigest()
    write(root / 'etc/superbench/payload-policy.json', raw(policy))
    registry = {'Version': 'superbench.service-identity.v1', 'LayoutVersion': 'superbench.payload-layout.v1',
                'Policy': policy, 'PolicySHA256': digest}
    write(root / 'etc/superbench/service-identity.json', raw(registry), mode=0o644)
    layout = {'Version': 'superbench.payload-layout.v1', 'Key': 'fixture', 'ID': '1' * 32,
              'PolicyDigest': digest, 'Scalar': False, 'Root': node(bench),
              'RootHome': node(bench / '.superbench-runtime/.root/home'), 'RootTemp': node(bench / '.superbench-runtime/.root/tmp'),
              'Members': [{'Name': 'a', 'Checkout': node(bench / 'a'), 'Admin': node(bench / '.superbench-git/a'),
                           'Home': node(bench / '.superbench-runtime/a/home'), 'Temp': node(bench / '.superbench-runtime/a/tmp')}]}
    write(root / 'state/payload-layouts/fixture.json', raw(layout), daemon, daemon_group)
    mounted = []
    try:
        for name in ('etc', 'run'):
            subprocess.run(['mount', '--bind', str(root / name), '/' + name], check=True, timeout=10)
            mounted.append('/' + name)
        for number, name in enumerate(('plain', 'no-groups', 'verify-fault', 'verify-mutant', 'late-fd', 'late-fd-mutant'), 1):
            scope = cgroup / ('%032x' % number)
            scope.mkdir(mode=0o755)
            req = {'Version': 'superbench.payload.v1', 'Key': 'fixture', 'LayoutID': '1' * 32,
                   'Member': 'a', 'PolicyDigest': digest, 'Kind': 'payload', 'Root': False,
                   'Scope': {'ID': scope.name, 'HostID': hashlib.sha256(b'superbench-host:' + machine.strip()).hexdigest(),
                             'BootID': Path('/proc/sys/kernel/random/boot_id').read_text().strip()},
                   'Arguments': [str(sealed / 'probe.bin')]}
            data = raw(req)
            pipes = [os.pipe() for _ in range(3)]
            for pair in pipes:
                for fd in pair:
                    os.fchown(fd, daemon, daemon_group)
            stdio = [(os.fstat(pipes[0][0]).st_dev, os.fstat(pipes[0][0]).st_ino),
                     (os.fstat(pipes[1][1]).st_dev, os.fstat(pipes[1][1]).st_ino),
                     (os.fstat(pipes[2][1]).st_dev, os.fstat(pipes[2][1]).st_ino)]
            inherited = os.open('/run/superbench-exec-fixture-canary', os.O_RDONLY)
            def join():
                os.setgroups([61005])  # Deleting Setgroups must be observable/refused.
                (scope / 'cgroup.procs').write_text(str(os.getpid()))
            process = None
            try:
                process = subprocess.Popen([str(sealed / (name + '.bin'))], stdin=pipes[0][0], stdout=pipes[1][1],
                                           stderr=pipes[2][1], pass_fds=(inherited,), preexec_fn=join,
                                           env={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin'})
                for fd in (pipes[0][0], pipes[1][1], pipes[2][1]):
                    os.close(fd)
                try:
                    os.write(pipes[0][1], struct.pack('>I', len(data)) + data)
                except BrokenPipeError:
                    pass
                os.close(pipes[0][1])
                code, output, error = read_output(process, (pipes[1][0], pipes[2][0]))
                if name in ('no-groups', 'verify-fault'):
                    require((code, output, error) == (1, b'', b'managed payload refused: credentials\n'), name + ' did not hit exact credential guard')
                else:
                    require(code == 0 and error == b'', name + ' did not execute actual probe')
                    observe(output, canary, stdio, allow_canary=name == 'late-fd-mutant')
                print('EXEC_CAUSAL_' + name.upper().replace('-', '_') + '_PASS', flush=True)
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline and ((scope / 'cgroup.procs').read_text().strip() or 'populated 0' not in (scope / 'cgroup.events').read_text()):
                    time.sleep(0.05)
                require(not (scope / 'cgroup.procs').read_text().strip() and 'populated 0' in (scope / 'cgroup.events').read_text(), 'scope did not naturally quiesce')
            finally:
                # Cleanup never turns a failed liveness assertion into a pass.
                (scope / 'cgroup.kill').write_text('1')
                if process is not None:
                    process.wait(timeout=10)
                for pair in pipes:
                    for fd in pair:
                        try:
                            os.close(fd)
                        except OSError:
                            pass
                os.close(inherited)
                deadline = time.monotonic() + 10
                while 'populated 0' not in (scope / 'cgroup.events').read_text() and time.monotonic() < deadline:
                    time.sleep(0.05)
                require('populated 0' in (scope / 'cgroup.events').read_text(), 'live helper retained')
                scope.rmdir()
    finally:
        for target in reversed(mounted):
            subprocess.run(['umount', target], check=True, timeout=10)
    print('ACTUAL_EXEC_AND_THREE_CAUSAL_MUTATIONS_PASS', flush=True)


if __name__ == '__main__':
    main()
