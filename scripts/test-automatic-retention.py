#!/usr/bin/env python3
"""Exercise approved automatic retention on a disposable Linux cgroup/daemon."""
import argparse
from datetime import datetime, timedelta, timezone
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import tempfile
import time
import uuid


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("local", timeout=10)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def wait_for(check, label):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if check():
            return
        time.sleep(.05)
    raise AssertionError("timeout: " + label)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--benchd", required=True)
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("disposable Linux root required")
    root = Path(tempfile.mkdtemp(prefix="sb-auto-retention-", dir="/var/tmp"))
    state, data, sock = root / "state", root / "data", root / "api.sock"
    cg = Path("/sys/fs/cgroup") / ("sb-auto-retention-" + uuid.uuid4().hex)
    cg.mkdir()
    daemon = None
    daemon_log = (root / "daemon.log").open("wb")

    def run(words):
        return subprocess.run(list(map(str, words)), capture_output=True, text=True, timeout=30)

    def good(words):
        result = run(words)
        assert result.returncode == 0, result.stderr
        return result.stdout

    def command(op, *words):
        return [args.bench, op, "--state", state, "--data", data, "--cgroup-root", cg, *words]

    def cli(op, *words):
        return json.loads(good(command(op, *words)))

    def start():
        process = subprocess.Popen([args.benchd, "--state", str(state), "--data", str(data),
                                    "--socket", str(sock), "--cgroup-root", str(cg),
                                    "--retention-interval", "1s"], stdout=daemon_log, stderr=daemon_log)

        def ready():
            assert process.poll() is None, "daemon exited"
            try:
                connection = UnixHTTP(sock)
                connection.request("GET", "/healthz")
                response = connection.getresponse()
                response.read()
                connection.close()
                return response.status == 200
            except (OSError, http.client.HTTPException):
                return False
        wait_for(ready, "daemon ready")
        return process

    def age_fixture():
        # Test-only time travel while the daemon is stopped. No production DB is touched.
        old = (datetime.now(timezone.utc) - timedelta(hours=72)).isoformat().replace("+00:00", "Z")
        with sqlite3.connect(state / "bench.db") as db:
            for identity, raw in db.execute("SELECT id,record FROM run_history").fetchall():
                record = json.loads(raw)
                for field in ("StartedAt", "ReleasedAt", "SealedAt"):
                    if record[field] and not record[field].startswith("0001-"):
                        record[field] = old
                db.execute("UPDATE run_history SET record=? WHERE id=?", (json.dumps(record), identity))

    def preview(max_bytes):
        proposal = cli("retention-preview", "--older-than", "1h", "--metadata-older-than", "24h",
                       "--limit", "10", "--max-collect-bytes", str(max_bytes))
        path = root / "policy.json"
        path.write_text(json.dumps(proposal))
        return proposal, path

    def enable(proposal, path):
        return cli("retention-enable", "--plan", path, "--approve", proposal["Digest"], "--enable-automatic")

    try:
        seed, remote = root / "seed", root / "remote.git"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / "file").write_text("base\n")
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])
        cli("create", "--repo", remote, "task")
        small = cli("run", "--holder", "small", "task", "--", "sh", "-c", "printf evidence")
        big = cli("run", "--holder", "big", "task", "--", "sh", "-c", "head -c 4096 /dev/zero")
        failure = run(command("run", "--holder", "failure", "task", "--", "sh", "-c", "printf failed; exit 7"))
        assert failure.returncode == 7, failure.stderr
        failed = json.loads(failure.stdout)
        held_lease = cli("status", "task")[0]["Lease"]
        assert held_lease["Holder"] == "failure" and held_lease["Execution"]["ID"] == failed["Execution"]["ID"]

        def logfile(result):
            return state / ("run-" + result["Execution"]["ID"] + ".log")

        legacy = state / ("run-" + "f" * 32 + ".log")
        legacy.write_text("unregistered evidence")
        legacy.chmod(0o600)
        age_fixture()
        daemon = start()
        time.sleep(1.2)
        assert logfile(small).exists() and logfile(big).exists()
        proposal, path = preview(1024)
        assert [item["Record"]["ID"] for item in proposal["Initial"]["Logs"]] == [small["Execution"]["ID"]]
        assert run(command("retention-enable", "--plan", path, "--approve", proposal["Digest"])).returncode != 0
        enable(proposal, path)
        wait_for(lambda: not logfile(small).exists(), "approved small log collection")
        assert logfile(big).exists() and logfile(failed).exists() and legacy.exists()
        assert cli("status", "task")[0]["Lease"] == held_lease
        print("PASS: daemon defaults to disabled; approved byte bound collects only eligible small log and preserves held failures/unregistered output", flush=True)

        cli("retention-disable")
        daemon.terminate()
        assert daemon.wait(timeout=15) == 0
        daemon = None
        daemon = start()
        assert cli("retention-run")["Status"] == "disabled" and logfile(big).exists()
        proposal, path = preview(8192)
        daemon.terminate()
        assert daemon.wait(timeout=15) == 0
        daemon = None
        enable(proposal, path)
        # Reproduce death after the automatic batch was fsynced but before core
        # preparation. Startup must replay this exact saved plan, not rediscover.
        pending = state / "retention-auto-pending.json"
        pending.write_text(json.dumps({"PolicyDigest": proposal["Digest"], "Plan": proposal["Initial"]}))
        pending.chmod(0o600)
        daemon = start()
        wait_for(lambda: not logfile(big).exists(), "reviewed larger policy")
        cli("retention-disable")
        assert any(item["Digest"] == proposal["Initial"]["Digest"] and item["Status"] == "completed"
                   for item in cli("collections"))
        assert cli("retention-status")["Pending"] is None
        print("PASS: restart resumes the exact durable automatic batch from before core preparation", flush=True)
        token = failed["Bench"]["Lease"]["Token"]
        cli("checkpoint", "--token", token, "task")
        cli("release", "--token", token, "task")
        daemon.terminate()
        assert daemon.wait(timeout=15) == 0
        daemon = None
        age_fixture()
        daemon = start()
        assert cli("retention-run")["Status"] == "disabled" and logfile(failed).exists()
        proposal, path = preview(8192)
        enable(proposal, path)
        wait_for(lambda: not logfile(failed).exists(), "released failed evidence collection")
        assert legacy.exists() and cli("status", "task")[0].get("Lease") is None
        print("PASS: disabled policy survives restart; changed bounds need fresh approval; failed evidence is collected only after explicit release and age", flush=True)
        cli("retention-disable")
        status = cli("retention-status")
        assert status["Config"]["Enabled"] is False and status["Pending"] is None
        print("DONE: automatic retention daemon integration passed", flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            daemon.terminate()
            try:
                daemon.wait(timeout=15)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait(timeout=10)
        if (cg / "cgroup.kill").exists():
            (cg / "cgroup.kill").write_text("1")
        wait_for(lambda: "populated 0" in (cg / "cgroup.events").read_text(), "cgroup empty")
        for child in cg.iterdir():
            if child.is_dir():
                child.rmdir()
        cg.rmdir()
        daemon_log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
