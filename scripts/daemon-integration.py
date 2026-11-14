#!/usr/bin/env python3
"""Disposable Linux root drill: actual socket peers, execution and crash recovery.

Pass compiled bench/benchd paths. Creates and removes only its own temporary data
and unique cgroup subtree; never provisions a host service or touches live benches.
"""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import uuid


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=10)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def wait_for(check, description):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(0.05)
    raise AssertionError("timeout: " + description)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--benchd", required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or not Path("/sys/fs/cgroup/cgroup.controllers").is_file():
        raise SystemExit("requires disposable Linux root with cgroup v2")
    root = Path(tempfile.mkdtemp(prefix="sb-api-", dir="/var/tmp"))
    root.chmod(0o755)  # Permit the read-only peer drill to reach the socket.
    cg = Path("/sys/fs/cgroup") / ("sb-api-test-" + uuid.uuid4().hex)
    cg.mkdir()
    daemon = None
    log = (root / "daemon.log").open("wb")
    sock = root / "api.sock"
    state = root / "state"
    data = root / "data"
    remote = root / "remote.git"
    seed = root / "seed"

    def git(directory, *words):
        subprocess.run(["git", "-C", str(directory), *words], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)

    def start():
        proc = subprocess.Popen([args.benchd, "--state", str(state), "--data", str(data),
                                 "--socket", str(sock), "--cgroup-root", str(cg),
                                 "--operator-uids", "0", "--reader-uids", "65534"],
                                stdout=log, stderr=log)
        def ready():
            if proc.poll() is not None:
                raise AssertionError("daemon exited during startup")
            try:
                c = UnixHTTP(sock)
                c.request("GET", "/healthz")
                r = c.getresponse()
                r.read()
                c.close()
                return r.status == 200
            except (OSError, http.client.HTTPException):
                return False
        wait_for(ready, "socket ready")
        return proc

    def body(operation, **fields):
        return {"version": "superbench.v1", "operation": operation, **fields}

    def request(payload, expected=200):
        c = UnixHTTP(sock)
        try:
            c.request("POST", "/v1/operations", json.dumps(payload),
                      {"Content-Type": "application/json"})
            r = c.getresponse()
            result = json.loads(r.read())
            assert r.status == expected, (r.status, result.get("error"))
            return result
        finally:
            c.close()

    def abandon(payload):
        c = UnixHTTP(sock)
        c.request("POST", "/v1/operations", json.dumps(payload),
                  {"Content-Type": "application/json"})
        c.close()

    def stopped(pid):
        try:
            return Path(f"/proc/{pid}/stat").read_text().split(") ", 1)[1][0] == "Z"
        except FileNotFoundError:
            return True

    try:
        git(root, "init", "--bare", str(remote))
        git(root, "init", "-b", "main", str(seed))
        (seed / "file").write_text("base\n")
        git(seed, "add", ".")
        git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
            "commit", "-m", "base")
        git(seed, "remote", "add", "origin", str(remote))
        git(seed, "push", "origin", "main")
        git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
        daemon = start()
        create = body("create", key="task", repository=str(remote), request_id="create")
        original = request(create)
        assert request(create) == original
        print("PASS: socket create and same-request durable replay", flush=True)

        marker = root / "once"
        work = body("run", key="task", holder="disconnect", request_id="disconnect",
                    arguments=["sh", "-c", f"printf x >> {marker}; sleep 0.3"])
        abandon(work)
        # A retry either sees in_progress or the completed receipt; never a second run.
        def retry_finished():
            c = UnixHTTP(sock)
            c.request("POST", "/v1/operations", json.dumps(work), {"Content-Type": "application/json"})
            r = c.getresponse(); response = json.loads(r.read()); c.close()
            if r.status == 409:
                assert response["error"]["code"] == "in_progress"
                return False
            assert r.status == 200, response.get("error")
            return response["ok"]
        wait_for(retry_finished, "disconnected run completed")
        assert marker.read_text() == "x"
        assert "lease" not in request(body("status", key="task"))["result"]["benches"][0]
        print("PASS: client disconnect preserves execution; replay runs command once", flush=True)

        failed = subprocess.run([args.bench, "run", "--socket", str(sock),
                                 "--holder", "exit-test", "--request-id", "exit-test",
                                 "task", "--", "sh", "-c", "exit 7"], capture_output=True)
        assert failed.returncode == 7, (failed.returncode, failed.stderr.decode())
        assert json.loads(failed.stdout)["exit_code"] == 7
        retained = request(body("stop_admin", key="task"))["result"]["bench"]
        token = retained["lease"]["token"]
        request(body("checkpoint", key="task", lease_token=token))
        request(body("release", key="task", lease_token=token))
        print("PASS: CLI preserves command exit status; failed lease reconciles", flush=True)

        pidfile = root / "writer.pid"
        crash = body("run", key="task", holder="crash", request_id="crash",
                     arguments=["sh", "-c", f"setsid sh -c 'echo $$ > {pidfile}; sleep 90' & wait"])
        abandon(crash)
        wait_for(pidfile.exists, "detached writer started")
        pid = int(pidfile.read_text())
        daemon.kill(); daemon.wait(timeout=10)
        assert not stopped(pid), "crash did not leave writer for recovery test"
        daemon = start()
        uncertain = request(crash, 409)
        assert uncertain["error"]["code"] == "operation_in_doubt"
        held = request(body("status", key="task"))["result"]["benches"][0]["lease"]
        assert held["mode"] == "supervised" and "token" not in held
        retained = request(body("stop_admin", key="task"))["result"]["bench"]
        wait_for(lambda: stopped(pid), "admin stop fenced detached writer")
        token = retained["lease"]["token"]
        request(body("checkpoint", key="task", lease_token=token))
        request(body("release", key="task", lease_token=token))
        assert request(crash, 409)["error"]["code"] == "operation_in_doubt"
        print("PASS: daemon SIGKILL retains lease and interrupted receipt; restart fences detached writer", flush=True)

        # A different kernel uid is allowed to read, but forged authority headers do not mutate.
        sock.chmod(0o666)
        probe = '''import http.client,json,socket,sys
class U(http.client.HTTPConnection):
 def connect(self):
  self.sock=socket.socket(socket.AF_UNIX);self.sock.connect(sys.argv[1])
for op,want in [("status",200),("stop_admin",403)]:
 c=U("local");b={"version":"superbench.v1","operation":op}
 if op=="stop_admin":b["key"]="task"
 c.request("POST","/v1/operations",json.dumps(b),{"Content-Type":"application/json","X-Operator-UID":"0"});r=c.getresponse();r.read();assert r.status==want;c.close()
'''
        def reader_identity():
            os.setgroups([]); os.setgid(65534); os.setuid(65534)
        subprocess.run(["python3", "-c", probe, str(sock)], preexec_fn=reader_identity, check=True)
        print("PASS: actual read-only kernel peer cannot forge operator authority", flush=True)
        daemon.terminate(); assert daemon.wait(timeout=15) == 0; daemon = None
        print("DONE: Linux daemon integration passed", flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            daemon.kill(); daemon.wait(timeout=10)
        # cgroup.kill applies only to this test's unique subtree.
        if (cg / "cgroup.kill").exists():
            (cg / "cgroup.kill").write_text("1")
        for child in cg.iterdir():
            if child.is_dir():
                child.rmdir()
        cg.rmdir()
        log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
