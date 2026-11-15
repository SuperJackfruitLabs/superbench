#!/usr/bin/env python3
"""Owned XFS helper proof; intentionally not a service/cgroup cancel claim."""
import fcntl
import hashlib
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import time

root, binary, evidence = map(Path, sys.argv[1:4])

def preserve_unresolved():
    # Independent of Popen bookkeeping: an orphaned privileged descendant may
    # survive its controller. Any other process in this unique mount namespace
    # prevents removing policy, ledger, mount, or evidence.
    namespace = os.stat('/proc/self/ns/mnt')
    live = []
    for item in Path('/proc').iterdir():
        if not item.name.isdigit() or int(item.name) in (os.getpid(), os.getppid()):
            continue
        try:
            ns = os.stat(item / 'ns/mnt')
            if (ns.st_dev, ns.st_ino) == (namespace.st_dev, namespace.st_ino):
                live.append(int(item.name))
        except FileNotFoundError:
            pass
        except OSError as error:
            live.append({'pid': int(item.name), 'inspection_error': str(error)})
    if live:
        (root / 'PRESERVE-LIVE-HELPER').write_text(json.dumps(live))
        (evidence / 'unresolved-processes.json').write_text(json.dumps(live))
        print('PRESERVE owned fixture: unresolved namespace processes', live, flush=True)

def main():
    assert root.is_absolute() and str(root).startswith('/var/tmp/superbench-cancel-helper-')
    assert os.geteuid() == 0 and root.is_dir() and (root / 'data').is_mount()
    helper = root / 'bench-quota'
    policy = root / 'policy.json'
    ledger = root / 'ledger'
    ledger.mkdir(mode=0o700)
    (root / 'state').mkdir(mode=0o700)
    evidence.mkdir(mode=0o700, exist_ok=True)
    p = dict(Root=str(root / 'data'), Ledger=str(ledger), OwnerUID=0, OwnerGID=0,
             BenchBytes=32<<20, StoreBytes=16<<20, MirrorBytes=16<<20,
             ReserveBytes=16<<20, BenchInodes=8192, StoreInodes=8192,
             MirrorInodes=8192, ReserveInodes=8192)
    policy.write_text(json.dumps(p)); policy.chmod(0o600)

    def children_of(pid):
        children = set()
        for task in Path(f'/proc/{pid}/task').iterdir():
            try:
                children.update(int(value) for value in (task / 'children').read_text().split())
            except FileNotFoundError:
                pass
        return sorted(children)

    def lock_wait(pid):
        # Go may block flock on any runtime OS thread, not its process leader.
        for task in Path(f'/proc/{pid}/task').iterdir():
            try:
                channel = (task / 'wchan').read_text().strip()
                if 'lock' in channel:
                    return task.name+':'+channel
            except FileNotFoundError:
                pass
        return None

    def sha(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def command(args, success=True):
        out = subprocess.run(args, capture_output=True, text=True, timeout=90)
        if (out.returncode == 0) != success:
            raise AssertionError((args, out.returncode, out.stdout, out.stderr))
        return out

    def quota(*args, success=True):
        return command([str(helper), '--policy', str(policy), *args], success)

    def child(key, mode, success=True):
        env = dict(os.environ, BENCH_QUOTA_OPERATION_ROOT=str(root),
                   BENCH_QUOTA_OPERATION_KEY=key, BENCH_QUOTA_OPERATION_MODE=mode)
        return run_child(env, success)

    def run_child(env, success):
        out = subprocess.run([str(binary), '-test.run=^TestManagedQuotaOperationChild$', '-test.v'],
                             env=env, capture_output=True, text=True, timeout=90)
        if (out.returncode == 0) != success:
            raise AssertionError((out.returncode, out.stdout, out.stderr))
        print(out.stdout, end='', flush=True)
        return out

    def proof(key):
        out = child(key, 'cancel')
        raw = next(line[6:] for line in out.stdout.splitlines() if line.startswith('PROOF:'))
        value = json.loads(raw)
        assert value['Cancelled'] is True
        assert value['Identity'] == json.loads((root / ('intent-'+key+'.json')).read_text())['Identity']
        return value

    # Bootstrap only the disposable fixed policy using its ordinary reviewed path.
    quota('apply', 'shared', 'store')
    info = json.loads(quota('operation-info').stdout)
    assert info['BinarySHA256'] == sha(helper) and info['Root'] == str(root / 'data')
    child('normal', 'apply')
    before = sha(ledger / 'assignments.json')
    normal_proof = proof('normal')
    assert sha(ledger / 'assignments.json') == before
    child('normal', 'apply', success=False)
    assert sha(ledger / 'assignments.json') == before
    quota('verify', 'bench', 'normal')
    print('PASS actual Worker typed apply, immutable helper image, deny, no allocation replay', flush=True)

    # Block the real inner allocation ledger while the typed helper holds its
    # outer operation barrier. Cancellation must wait for the entire atomic apply.
    inner = os.open(ledger / 'lock', os.O_RDWR)
    fcntl.flock(inner, fcntl.LOCK_EX)
    inflight_log = open(evidence / 'inflight.log', 'wb')
    apply_env = dict(os.environ, BENCH_QUOTA_OPERATION_ROOT=str(root),
                     BENCH_QUOTA_OPERATION_KEY='inflight', BENCH_QUOTA_OPERATION_MODE='apply')
    apply_process = subprocess.Popen([str(binary), '-test.run=^TestManagedQuotaOperationChild$', '-test.v'],
                                     env=apply_env, stdout=inflight_log, stderr=subprocess.STDOUT)
    cancel_process = None
    try:
        deadline = time.monotonic()+30
        inflight_intent = root / 'intent-inflight.json'
        marker = None
        while time.monotonic() < deadline:
            assert apply_process.poll() is None
            if inflight_intent.exists():
                admitted = json.loads(inflight_intent.read_text())
                marker = ledger / 'operations' / (admitted['Identity']['OperationID']+'.json')
                if marker.exists() and json.loads(marker.read_text())['State'] == '0':
                    break
            time.sleep(.02)
        assert marker is not None and marker.exists() and json.loads(marker.read_text())['State'] == '0'
        probe = os.open(ledger / 'operations.lock', os.O_RDWR)
        try:
            try:
                fcntl.flock(probe, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                pass
            else:
                raise AssertionError('typed apply did not retain its outer barrier')
        finally:
            os.close(probe)
        cancel_env = dict(apply_env, BENCH_QUOTA_OPERATION_MODE='cancel')
        cancel_process = subprocess.Popen([str(binary), '-test.run=^TestManagedQuotaOperationChild$', '-test.v'],
                                         env=cancel_env, stdout=inflight_log, stderr=subprocess.STDOUT)
        deadline = time.monotonic()+30
        waiting = False
        while time.monotonic() < deadline:
            assert cancel_process.poll() is None
            children = children_of(cancel_process.pid)
            for value in children:
                try:
                    if b'cancel-operation' in Path(f'/proc/{value}/cmdline').read_bytes().split(b'\0'):
                        assert sha(Path(f'/proc/{value}/exe')) == info['BinarySHA256']
                        waiting = lock_wait(value) is not None
                except FileNotFoundError:
                    pass
            if waiting:
                break
            time.sleep(.02)
        assert waiting, 'did not observe cancel waiting for atomic apply'
        assert json.loads(marker.read_text())['State'] == '0'
        fcntl.flock(inner, fcntl.LOCK_UN)
        assert apply_process.wait(timeout=90) == 0
        assert cancel_process.wait(timeout=90) == 0
        assert json.loads(marker.read_text())['State'] == '1'
        assert (root / 'data' / 'benches' / 'inflight').is_dir()
        stable = sha(ledger / 'assignments.json')
        child('inflight', 'apply', success=False)
        assert sha(ledger / 'assignments.json') == stable
        print('PASS cancellation waits for whole real in-flight fixed-policy apply', flush=True)
    finally:
        fcntl.flock(inner, fcntl.LOCK_UN); os.close(inner)
        apply_process.wait(timeout=90)
        if cancel_process is not None:
            cancel_process.wait(timeout=90)
        inflight_log.close()
    before = sha(ledger / 'assignments.json')

    # A policy change must not certify the original queued helper against a new policy.
    original_policy = policy.read_bytes()
    p['BenchBytes'] = 16<<20
    policy.write_text(json.dumps(p)); policy.chmod(0o600)
    child('normal', 'cancel', success=False)
    assert sha(ledger / 'assignments.json') == before
    policy.write_bytes(original_policy); policy.chmod(0o600)
    proof('normal')
    print('PASS changed fixed policy refuses original-helper proof without ledger writes', flush=True)

    # Hold the exact real helper outer lock. The controller journals then starts a
    # root helper which queues BEFORE all ApplyRequested effects. SIGSTOP is used
    # only after /proc proves this owned helper is sleeping in the lock syscall.
    # No privileged helper is killed, especially not during an atomic apply.
    lock = os.open(ledger / 'operations.lock', os.O_RDWR)
    fcntl.flock(lock, fcntl.LOCK_EX)
    key = 'controllerloss'
    env = dict(os.environ, BENCH_QUOTA_OPERATION_ROOT=str(root),
               BENCH_QUOTA_OPERATION_KEY=key, BENCH_QUOTA_OPERATION_MODE='apply')
    log = open(evidence / 'controller.log', 'wb')
    controller = subprocess.Popen([str(binary), '-test.run=^TestManagedQuotaOperationChild$', '-test.v'],
                                  env=env, stdout=log, stderr=subprocess.STDOUT)
    controller_fd = os.pidfd_open(controller.pid)
    helper_pid = None
    helper_fd = None
    queued_wchan = None
    resumed = False
    try:
        deadline = time.monotonic()+30
        while time.monotonic() < deadline:
            assert controller.poll() is None, 'controller exited before queued helper'
            children = children_of(controller.pid)
            for value in children:
                candidate = int(value)
                try:
                    cmd = Path(f'/proc/{candidate}/cmdline').read_bytes().split(b'\0')
                    if b'apply-operation' not in cmd:
                        continue
                    wchan = lock_wait(candidate)
                    if wchan is None:
                        continue
                    fd = os.pidfd_open(candidate)
                    assert sha(Path(f'/proc/{candidate}/exe')) == info['BinarySHA256']
                    helper_pid, helper_fd, queued_wchan = candidate, fd, wchan
                    break
                except FileNotFoundError:
                    pass
            if helper_fd is not None:
                break
            time.sleep(.02)
        assert helper_fd is not None, 'did not observe actual helper queued before effects'
        signal.pidfd_send_signal(helper_fd, signal.SIGSTOP)
        deadline = time.monotonic()+5
        while time.monotonic() < deadline:
            if '\nState:\tT' in Path(f'/proc/{helper_pid}/status').read_text():
                break
            time.sleep(.01)
        assert '\nState:\tT' in Path(f'/proc/{helper_pid}/status').read_text()
        journal = json.loads((root / ('intent-'+key+'.json')).read_text())
        assert journal['Identity']['BinarySHA256'] == info['BinarySHA256']
        assert not (root / 'data' / 'benches' / key).exists()
        signal.pidfd_send_signal(controller_fd, signal.SIGKILL)
        assert controller.wait(timeout=10) == -signal.SIGKILL
        assert Path(f'/proc/{helper_pid}').exists(), 'helper did not survive controller loss'
        fcntl.flock(lock, fcntl.LOCK_UN)
        before = sha(ledger / 'assignments.json')
        orphan_proof = proof(key)
        assert sha(ledger / 'assignments.json') == before
        assert not (root / 'data' / 'benches' / key).exists()
        signal.pidfd_send_signal(helper_fd, signal.SIGCONT)
        resumed = True
        poll = select.poll(); poll.register(helper_fd, select.POLLIN)
        assert poll.poll(30000), 'owned helper did not exit after durable deny'
        assert sha(ledger / 'assignments.json') == before
        assert not (root / 'data' / 'benches' / key).exists()
        child(key, 'apply', success=False)
        assert sha(ledger / 'assignments.json') == before
        print('PASS controller SIGKILL leaves queued helper; persisted original deny prevents later effects/retry', flush=True)
    finally:
        fcntl.flock(lock, fcntl.LOCK_UN); os.close(lock)
        if helper_fd is not None:
            if not resumed:
                signal.pidfd_send_signal(helper_fd, signal.SIGCONT)
            poll = select.poll(); poll.register(helper_fd, select.POLLIN)
            if not poll.poll(30000):
                (root / 'PRESERVE-LIVE-HELPER').write_text(str(helper_pid))
                raise RuntimeError('preserve fixture: live root helper must not be killed')
            os.close(helper_fd)
        # Never kill a helper to make cleanup pass. Only the owned controller may
        # need orderly completion if an assertion failed before its deliberate kill.
        if controller.poll() is None:
            controller.wait(timeout=90)
        os.close(controller_fd); log.close()

    # Ordinary untyped allocation remains supported and is explicitly outside the
    # cancellation proof, rather than being silently upgraded into typed authority.
    quota('apply', 'bench', 'ordinary', str(4<<20))
    quota('verify', 'bench', 'ordinary')
    result = dict(scope='typed-helper-only; no service/cgroup/wire completion claim',
                  helper_sha256=sha(helper), test_sha256=sha(binary), info=info,
                  normal_proof=normal_proof, orphan_proof=orphan_proof,
                  queued_helper_pid=helper_pid, queued_wchan=queued_wchan,
                  ledger_sha256=sha(ledger / 'assignments.json'))
    (evidence / 'result.json').write_text(json.dumps(result, indent=2)+'\n')
    print('PASS ordinary Apply remains unchanged; typed proof result saved', flush=True)

try:
    main()
finally:
    preserve_unresolved()
