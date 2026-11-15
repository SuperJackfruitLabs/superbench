#!/usr/bin/env python3
"""Opt-in private producer cancellation proof in an owned offline Linux fixture.

The ordinary daemon cannot enable cancellation. The supplied service test binary
is the only daemon used here. No service, authentication, or host policy is installed.
"""
import argparse
import fcntl
import hashlib
import http.client
import importlib.util
import json
import os
from pathlib import Path
import select
import socket
import sys
import signal
import sqlite3
import subprocess
import time
from types import SimpleNamespace


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def wait_for(check, label, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(.025)
    raise AssertionError("timeout: " + label)


def dead(fd):
    poll = select.poll()
    poll.register(fd, select.POLLIN)
    return bool(poll.poll(0))


def sql(path, statement, args=()):
    with sqlite3.connect(str(path), timeout=5) as db:
        return db.execute(statement, args).fetchall()


def process_identity(pid):
    raw = Path('/proc', str(pid), 'stat').read_text()
    return raw[raw.rfind(')') + 2:].split()[19]


def preserve_unresolved(root, evidence):
    ns = os.stat('/proc/self/ns/mnt')
    live = []
    for path in Path('/proc').iterdir():
        if not path.name.isdigit() or int(path.name) in (os.getpid(), os.getppid()):
            continue
        try:
            found = os.stat(path / 'ns/mnt')
            if (found.st_dev, found.st_ino) == (ns.st_dev, ns.st_ino):
                live.append(int(path.name))
        except FileNotFoundError:
            pass
        except OSError:
            live.append(path.name + ':unknown')
    if live:
        (root / 'PRESERVE-LIVE-HELPER').write_text(json.dumps(live))
        (evidence / 'unresolved-processes.json').write_text(json.dumps(live))
        print('PRESERVE owned fixture: unresolved namespace processes', flush=True)


def assert_uid(pid, uid):
    status = Path('/proc', str(pid), 'status').read_text().splitlines()
    assert next(line for line in status if line.startswith('Uid:')).split()[1:] == [str(uid)] * 4
    if uid == 1001:
        assert next(line for line in status if line.startswith('CapEff:')).split()[1] == '0000000000000000'


def client_main(path):
    assert_uid(os.getpid(), 1001)
    payload = json.loads(sys.stdin.readline())
    connection = http.client.HTTPConnection('local', timeout=60)
    connection.sock = socket.socket(socket.AF_UNIX)
    connection.sock.settimeout(60)
    connection.sock.connect(path)
    # Root conductor verifies this child's kernel identity independently too.
    print('CLIENT_READY', flush=True)
    assert sys.stdin.readline().strip() == 'GO'
    try:
        connection.request('POST', '/v2/operations', json.dumps(payload), {'Content-Type': 'application/json'})
        response = connection.getresponse()
        raw = response.read((1 << 20) + 1)
        assert len(raw) <= 1 << 20
        print(json.dumps({'status': response.status, 'raw': raw.decode('utf-8')}), flush=True)
    finally:
        connection.close()


class PendingClient:
    def __init__(self, path, payload, environment):
        self.process = subprocess.Popen(['/usr/bin/setpriv', '--reuid=1001', '--regid=1001', '--clear-groups',
            '/usr/bin/python3', '-I', str(Path(__file__).resolve()), '--client', str(path)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, env=environment)
        self.process.stdin.write(json.dumps(payload) + '\n'); self.process.stdin.flush()
        assert self.process.stdout.readline().strip() == 'CLIENT_READY', 'nonroot client failed before connect'
        assert_uid(self.process.pid, 1001)
        self.process.stdin.write('GO\n'); self.process.stdin.close()

    def result(self):
        line = self.process.stdout.readline()
        code = self.process.wait(timeout=65)
        assert code == 0, 'nonroot client failed: ' + self.process.stderr.read()
        return json.loads(line)

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
        self.process.wait(timeout=10)
        self.process.stdout.close(); self.process.stderr.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('trial', 'bench', 'testbin', 'quota-helper', 'api-driver', 'cgroup-root', 'evidence'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    root, cg, evidence = map(lambda p: Path(p).resolve(strict=True), (args.trial, args.cgroup_root, args.evidence))
    assert os.getuid() == os.geteuid() == 0 and not os.environ.get('SUDO_UID')
    assert str(root).startswith('/var/tmp/superbench-cancel-managed.') and (root / 'data').is_mount()
    assert str(cg).startswith('/sys/fs/cgroup/superbench-cancel-')
    module = importlib.util.spec_from_file_location('workspace_api_fixture', args.api_driver)
    api = importlib.util.module_from_spec(module)
    module.loader.exec_module(api)

    class Drill(api.Drill):
        def __init__(self):
            super().__init__(SimpleNamespace(trial=str(root), cgroup_root=str(cg), bench=args.bench,
                benchd=args.testbin, quota_helper=args.quota_helper,
                corpus=str(Path(args.api_driver).resolve().parents[1] / 'internal/protocolv2/testdata/workspace-lifecycle.json')))
            self.evidence = evidence
            self.sock = root / 'run/api.sock'
            self.environment = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'HOME': str(root / 'home'),
                'LANG': 'C', 'LC_ALL': 'C', 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null'}
            for directory in (self.state, root / 'run', root / 'home', root / 'payload'):
                directory.mkdir(mode=0o700, exist_ok=True); os.chown(directory, 1001, 1001)
            self.members = self.configure()

        def run(self, words, expected=0):
            command = ['/usr/bin/setpriv', '--reuid=1001', '--regid=1001', '--clear-groups', *map(str, words)]
            result = subprocess.run(command, capture_output=True, text=True, timeout=45, env=self.environment)
            assert result.returncode == expected, (str(words[0]), result.returncode, result.stderr)
            return result

        def configure(self):
            policy = {'Root': str(self.data), 'Ledger': str(root / 'quota-ledger'), 'OwnerUID': 1001, 'OwnerGID': 1001,
                'BenchBytes': 64 << 20, 'StoreBytes': 32 << 20, 'MirrorBytes': 64 << 20, 'ReserveBytes': 64 << 20,
                'BenchInodes': 4096, 'StoreInodes': 8192, 'MirrorInodes': 8192, 'ReserveInodes': 1024}
            path = Path('/etc/superbench/storage.json'); path.write_text(json.dumps(policy)); path.chmod(0o644)
            self.run(self.local('configure-host', '--quota-helper', args.quota_helper, '--min-data-free', 8 << 20, '--min-state-free', 1 << 20))
            self.run(['/usr/bin/sudo', '-n', '/bin/true'], expected=1)
            self.run(['/usr/bin/sudo', '-n', args.quota_helper, '--policy', str(root / 'unapproved-policy'), 'verify', 'bench', 'fixture'], expected=1)
            members = []
            for name in ('a', 'b'):
                seed, remote = root / ('seed-' + name), root / ('remote-' + name + '.git')
                for directory in (seed, remote):
                    directory.mkdir(mode=0o700); os.chown(directory, 1001, 1001)
                self.run(['git', 'init', '--bare', remote]); self.run(['git', 'init', '-b', 'main', seed])
                (seed / '.bench').mkdir(); os.chown(seed / '.bench', 1001, 1001)
                once = "test $(id -u) = 1001; mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log"
                each = "test $(id -u) = 1001; id -u > .bench/output/uid; printf 'each\\n' >> .bench/output/each.log"
                (seed / '.bench/config.toml').write_text('[quota]\nsize="32MiB"\n[setup]\nonce=' + json.dumps(once) + '\neach=' + json.dumps(each) + '\n')
                (seed / '.gitignore').write_text('/.bench/output/\n'); (seed / 'source.txt').write_text('sealed base ' + name + '\n')
                for file in (seed / '.bench/config.toml', seed / '.gitignore', seed / 'source.txt'):
                    os.chown(file, 1001, 1001)
                self.git(seed, 'add', '.'); self.git(seed, '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid', 'commit', '-m', 'base')
                self.git(seed, 'remote', 'add', 'origin', str(remote)); self.git(seed, 'push', 'origin', 'main'); self.git(remote, 'symbolic-ref', 'HEAD', 'refs/heads/main')
                members.append({'name': name, 'repository': str(remote)}); self.remotes[name] = remote
            return members

        def request(self, payload, expected=200):
            client = PendingClient(self.sock, payload, self.environment)
            try:
                response = client.result()
            finally:
                client.close()
            raw = response['raw'].encode('utf-8')
            allowed = (expected,) if isinstance(expected, int) else expected
            assert response['status'] in allowed, (payload['operation'], response['status'])
            body = json.loads(raw)
            assert body['version'] == 'superbench.v2' and body['operation'] == payload['operation']
            assert body.get('request_id') == payload.get('request_id') and body['ok'] is (response['status'] == 200)
            if payload['operation'] in {'create', 'recover', 'lease', 'run', 'setup', 'cancel'}:
                assert len(raw) <= 65536
            return body, raw

        def start(self):
            assert self.daemon is None
            env = dict(self.environment, SUPERBENCH_CANCELLATION_TRIAL=str(root),
                SUPERBENCH_CANCELLATION_CGROUP_ROOT=str(cg), GOMAXPROCS='2')
            def unprivileged():
                os.setgroups([]); os.setgid(1001); os.setuid(1001)
            self.daemon = subprocess.Popen([args.testbin, '-test.run=^TestManagedCancellationDaemon$',
                '-test.timeout=15m', '-test.v'], env=env, stdout=self.log, stderr=self.log, preexec_fn=unprivileged)
            def ready():
                assert self.daemon.poll() is None, 'private test daemon exited'
                if not self.sock.exists():
                    return False
                try:
                    self.request(self.payload('status'))
                    return True
                except (OSError, http.client.HTTPException):
                    return False
            wait_for(ready, 'private daemon ready')
            assert_uid(self.daemon.pid, 1001)

        def digest(self, payload):
            path = root / 'source-request.json'
            path.write_text(json.dumps(payload)); path.chmod(0o600); os.chown(path, 1001, 1001)
            env = dict(self.environment, SUPERBENCH_CANCELLATION_DIGEST_FILE=str(path))
            result = subprocess.run(['/usr/bin/setpriv', '--reuid=1001', '--regid=1001', '--clear-groups', args.testbin, '-test.run=^TestManagedCancellationSourceDigest$', '-test.v'],
                env=env, capture_output=True, text=True, timeout=10)
            assert result.returncode == 0, 'actual typed digest helper failed'
            values = [line.split('=', 1)[1] for line in result.stdout.splitlines() if line.startswith('CANCELLATION_DIGEST=')]
            assert len(values) == 1 and len(values[0]) == 64
            return values[0]

        def cancel(self, original, expected='cancel_requested'):
            request = {'version': 'superbench.v2', 'operation': 'cancel',
                'target_request_id': original['request_id'], 'target_digest': self.digest(original)}
            body, raw = self.request(request)
            assert set(body) == {'version', 'operation', 'ok', 'result'} and body['ok'] is True
            result = body['result']
            assert set(result) == {'target_request_id', 'target_digest', 'state'}
            assert result['target_request_id'] == request['target_request_id'] and result['target_digest'] == request['target_digest']
            assert result['state'] == expected and len(raw) <= 65536
            assert b'lease_token' not in raw and b'root_path' not in raw
            return body

        def terminal(self, original):
            request = {'version': 'superbench.v2', 'operation': 'cancel',
                'target_request_id': original['request_id'], 'target_digest': self.digest(original)}
            def done():
                body, raw = self.request(request)
                assert b'lease_token' not in raw and b'root_path' not in raw
                result = body.get('result', {})
                assert result.get('target_request_id') == request['target_request_id'] and result.get('target_digest') == request['target_digest']
                assert result.get('state') in ('cancel_requested', 'cancelled')
                return body if result['state'] == 'cancelled' else None
            return wait_for(done, 'independently confirmed cancellation', 45)

        def pending(self, payload):
            return PendingClient(self.sock, payload, self.environment)

        def binding(self, original):
            rows = sql(self.state / 'bench.db', 'SELECT id,epoch,phase,control_state,control_root,control_host,control_boot FROM workspace_operations WHERE owner=1001 AND request_id=?', (original['request_id'],))
            return rows[0] if rows else None

        def helpers(self, original):
            return [json.loads(row[0]) for row in sql(self.state / 'service.db',
                'SELECT invocation FROM operation_helpers WHERE owner=1001 AND id=? ORDER BY kind,key', (original['request_id'],))]

        def prove_denied(self, original):
            helpers = self.helpers(original)
            assert helpers and len(helpers) <= 3
            before = (root / 'quota-ledger/assignments.json').read_bytes()
            for helper in helpers:
                identity = helper['Identity']
                assert identity['BinarySHA256'] == sha(args.quota_helper)
                marker = json.loads((root / 'quota-ledger/operations' / (identity['OperationID'] + '.json')).read_text())
                assert marker['State'] == '1' and marker['Identity'] == identity
                words = ['/usr/bin/sudo', '-n', helper['Command'], 'apply-operation', helper['Kind'], helper['Key'],
                    str(helper['Requested']), *(identity[k] for k in ('OperationID', 'Epoch', 'StateIdentity', 'Authority', 'BinarySHA256'))]
                retry = subprocess.run(['/usr/bin/setpriv', '--reuid=1001', '--regid=1001', '--clear-groups', *words], capture_output=True, timeout=20, env=self.environment)
                assert retry.returncode != 0, 'denied original helper became executable'
            assert (root / 'quota-ledger/assignments.json').read_bytes() == before

        def start_writer(self, key, label, token=None):
            marker = root / 'payload' / (label + '.json')
            writer = "import json,os,time;assert os.getuid()==os.geteuid()==1001;from pathlib import Path;p=Path(" + repr(str(marker)) + ");p.write_text(json.dumps({'pid':os.getpid(),'start':Path('/proc/self/stat').read_text().split()[21],'sid':os.getsid(0)}));\nwhile True: print('retained descendant partial output',flush=True);time.sleep(.05)"
            payload = "import os,subprocess,time;assert os.getuid()==os.geteuid()==1001;subprocess.Popen(['/usr/bin/setsid','/usr/bin/python3','-c'," + repr(writer) + "]);\nwhile True: time.sleep(1)"
            request = self.payload('run', key=key, holder='payload-owner', repository_name='a', checkpoint=False,
                arguments=['/usr/bin/python3', '-c', payload], request_id=label)
            if token is not None:
                request['lease_token'] = token
            connection = self.pending(request)
            wait_for(marker.exists, 'detached writer started')
            identity = json.loads(marker.read_text()); pid = identity['pid']
            assert process_identity(pid) == identity['start'] and os.getsid(pid) == pid == identity['sid']
            assert_uid(pid, 1001)
            fd = os.pidfd_open(pid)
            b = self.record(key); execution = b['Lease']['Execution']
            assert execution['Phase'] == 'running'
            assert ('/' + cg.name + '/' + execution['ID']) in Path('/proc', str(pid), 'cgroup').read_text()
            binding = self.binding(request)
            assert binding and binding[2] == 'bound' and binding[3] == str(self.state) and binding[4] == str(cg)
            assert binding[5] == execution['HostID'] and binding[6] == execution['BootID']
            return request, connection, fd, execution

        def stopped(self, original, execution, fd, unknown):
            assert dead(fd), 'exact detached writer pidfd remains live'
            b = self.record(original['key']); lease = b['Lease']
            assert lease['StopRequested'] is True and not lease.get('Continuation')
            assert lease['Execution']['Phase'] == 'stopped'
            scope = cg / execution['ID']
            assert not scope.exists() or 'populated 0' in (scope / 'cgroup.events').read_text()
            runs = [r for r in self.history(original['key']) if r['ID'] == execution['ID']]
            assert len(runs) == 1
            r = runs[0]; path = self.state / ('run-' + r['ID'] + '.log')
            assert r['File'] and r['File']['SHA256'] == sha(path) and r['Size'] == path.stat().st_size > 0
            assert not r['SealedAt'].startswith('0001-') and r['ReleasedAt'].startswith('0001-')
            if unknown:
                assert r['ExitCode'] is None and r['Interruption']['LogState'] == 'present'
                assert b'retained descendant partial output' in path.read_bytes()
            assert self.binding(original)[2] == 'stopped'
            self.prove_denied(original)
            return b

    d = None
    try:
        d = Drill(); d.start()
        create = d.payload('create', key='warm', checkpoint_repository='a', repositories=d.members, initialize=True,
            quota_bytes=32 << 20, request_id='warm-create')
        _, historical = d.request(create)
        for name in ('a', 'b'):
            assert (d.data / 'benches/warm' / name / '.bench/output/uid').read_text().strip() == '1001'
        d.cancel(create, 'already_completed')
        d.stop_daemon(crash=True); d.start()
        _, replay = d.request(create)
        assert replay == historical
        print('PASS: durable completed cutoff survives lost delivery and daemon restart; verified cancel ACK permits exact historical replay only', flush=True)

        # Crash leaves the actual setsid descendant and inherited capture open.
        original, connection, fd, execution = d.start_writer('warm', 'crash-payload')
        original_count = len(d.history('warm'))
        d.stop_daemon(crash=True); connection.close()
        assert not dead(fd)
        before = (d.state / ('run-' + execution['ID'] + '.log')).stat().st_size
        wait_for(lambda: (d.state / ('run-' + execution['ID'] + '.log')).stat().st_size > before, 'writer survived controller death')
        binding = d.binding(original)
        trigger = 'cancel_finalization_fixture'
        sql(d.state / 'bench.db', "CREATE TRIGGER " + trigger + " BEFORE UPDATE OF phase ON workspace_operations WHEN OLD.id='" + binding[0] + "' AND NEW.phase IN ('finished','stopped') BEGIN SELECT RAISE(IGNORE); END")
        d.start(); assert not dead(fd)
        d.cancel(original)
        wait_for(lambda: dead(fd), 'restart cancellation fenced setsid descendant')
        wait_for(lambda: next(r for r in d.history('warm') if r['ID'] == execution['ID'])['SealedAt'][:5] != '0001-', 'interrupted partial history sealed')
        d.cancel(original)
        assert d.binding(original)[2] == 'bound', 'ignored SQL finalization was falsely confirmed'
        retained = (d.state / ('run-' + execution['ID'] + '.log')).read_bytes()
        sql(d.state / 'bench.db', 'DROP TRIGGER ' + trigger)
        d.terminal(original)
        held = d.stopped(original, execution, fd, True); os.close(fd)
        assert (d.state / ('run-' + execution['ID'] + '.log')).read_bytes() == retained
        assert len(d.history('warm')) == original_count
        d.request(original, (409, 503))
        assert len(d.history('warm')) == original_count
        print('PASS: controller-loss writer fenced; actual partial capture sealed with unknown exit; SQL finalization failure never confirmed; exact retry launches nothing', flush=True)

        # Explicit owner reconciliation is distinct from cancellation and uses
        # the retained capability only inside this privileged disposable fixture.
        token = held['Lease']['Token']
        d.request(d.payload('checkpoint', key='warm', lease_token=token))
        d.request(d.payload('release', key='warm', lease_token=token))
        lease = d.payload('lease', key='warm', holder='payload-owner', request_id='later-lease')
        body, _ = d.request(lease); later_token = d.grant(body)
        later = d.record('warm')
        d.cancel(original, 'cancelled')
        assert d.record('warm') == later, 'old cancelled epoch touched later owner'
        current, connection, fd, execution = d.start_writer('warm', 'live-payload', later_token)
        d.cancel(lease, 'already_completed')
        assert not dead(fd), 'completed lease cancellation stopped future same-token run'
        connection.close()  # A lost client is not a cancellation intent.
        time.sleep(.1); assert not dead(fd)
        d.cancel(current); d.terminal(current)
        d.stopped(current, execution, fd, False); os.close(fd)
        print('PASS: old epoch cannot stop later owner or same-token future run; disconnect does not cancel; exact current operation fences without admin-stop fallback', flush=True)

        # A fixed helper queued before the outer policy lock may outlive its Go
        # controller. Never kill the helper; cancellation waits the full barrier.
        lock = os.open(root / 'quota-ledger/operations.lock', os.O_RDWR)
        fcntl.flock(lock, fcntl.LOCK_EX)
        partial = d.payload('create', key='partial', checkpoint_repository='a', repositories=d.members,
            initialize=True, quota_bytes=32 << 20, request_id='queued-create')
        connection = d.pending(partial)
        helper_fd = None
        try:
            def queued():
                if not d.helpers(partial):
                    return None
                for process in Path('/proc').iterdir():
                    if not process.name.isdigit():
                        continue
                    try:
                        if os.readlink(process / 'exe') != args.quota_helper:
                            continue
                        words = (process / 'cmdline').read_bytes().split(b'\0')
                        if b'apply-operation' not in words or b'partial' not in words:
                            continue
                        waits = [(task / 'wchan').read_text().strip() for task in (process / 'task').iterdir()]
                        if any('lock' in value for value in waits):
                            return int(process.name)
                    except (FileNotFoundError, ProcessLookupError):
                        continue
                return None
            helper_pid = wait_for(queued, 'original helper queued before policy lock')
            assert_uid(helper_pid, 0)
            helper_fd = os.pidfd_open(helper_pid)
            original_binding = d.binding(partial)
            d.stop_daemon(crash=True); connection.close()
            assert not dead(helper_fd), 'queued helper did not survive controller loss'
            d.start(); d.cancel(partial)
            assert not dead(helper_fd)
            assert d.binding(partial)[2] == 'bound'
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN); os.close(lock)
        wait_for(lambda: dead(helper_fd), 'original helper completed its atomic section'); os.close(helper_fd)
        d.terminal(partial); d.prove_denied(partial)
        b = d.record('partial')
        assert b['Workspace']['Phase'] == 'materializing' and b['Lease']['StopRequested'] is True and not b['Lease'].get('Continuation')
        assert not b['Lease'].get('Execution') and not b['Workspace'].get('Checkpoint')
        assert d.binding(partial)[0:2] == original_binding[0:2] and d.binding(partial)[2] == 'stopped'
        d.request(partial, (409, 503))
        print('PASS: durable helper intent precedes Start; queued fixed helper survives controller death; ACK independent of outer lock; exact deny/quiescence precedes stopped partial-materialization terminal', flush=True)
        print('UNCOVERED: explicit repair of stopped materialization; public enablement; cross-host cancellation; macOS descendant fencing', flush=True)
        d.stop_daemon()
        d.log.flush()
        for source in (root / 'daemon.log', Path('/etc/superbench/storage.json')):
            (evidence / source.name).write_bytes(source.read_bytes())
        report = {'scope': 'private managed producer cancellation only', 'uid': 1001, 'privileged_helper_uid': 0,
            'helper_sha256': sha(args.quota_helper), 'test_daemon_sha256': sha(args.testbin),
            'bench_sha256': sha(args.bench), 'cgroup_root': str(cg), 'trial': str(root)}
        (evidence / 'runtime-result.json').write_text(json.dumps(report, indent=2) + '\n')
    finally:
        if d is not None and d.daemon is not None:
            try:
                d.stop_daemon()
            except BaseException:
                (root / 'PRESERVE-LIVE-HELPER').write_text('daemon/helper quiescence unresolved')
        preserve_unresolved(root, evidence)


if __name__ == '__main__':
    if len(sys.argv) == 3 and sys.argv[1] == '--client':
        client_main(sys.argv[2])
    else:
        main()
