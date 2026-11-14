#!/usr/bin/env python3
"""Verify explicit repository setup hooks using disposable Linux state/cgroups."""
import argparse
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
    parser.add_argument("--old-bench", required=True, help="published schema-6 binary")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("disposable Linux root required")
    root = Path(tempfile.mkdtemp(prefix="sb-setup-", dir="/var/tmp"))
    state, data, sock = root / "state", root / "data", root / "api.sock"
    cg = Path("/sys/fs/cgroup") / ("sb-setup-" + uuid.uuid4().hex)
    cg.mkdir()
    daemon = process = None
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

    def receipt(key):
        with sqlite3.connect(state / "bench.db") as db:
            return db.execute("SELECT digest,run_id FROM setup_receipts WHERE bench=?", (key,)).fetchone()

    def replace_config(key, value):
        token = cli("lease", "--holder", "configure", key)["Lease"]["Token"]
        path = data / "benches" / key
        (path / ".bench/config.toml").write_text(value)
        good(["git", "-C", path, "add", ".bench/config.toml"])
        good(["git", "-C", path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "setup configuration"])
        cli("checkpoint", "--token", token, key)
        cli("release", "--token", token, key)
        return path

    try:
        seed, remote = root / "seed", root / "remote.git"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / ".bench").mkdir()
        config = "[setup]\nonce = 'printf once >> .setup-events'\neach = 'printf each:$BENCH_SETUP_OFFLINE >> .setup-events'\n"
        (seed / ".bench/config.toml").write_text(config)
        (seed / ".gitignore").write_text(".setup-events\n.hook-ready\n.hook-pid\n")
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])
        good([args.old_bench, "create", "--state", state, "--data", data, "--repo", remote, "task"])
        with sqlite3.connect(state / "bench.db") as db:
            assert db.execute("PRAGMA user_version").fetchone()[0] == 6
        first = cli("setup", "--holder", "first", "task")
        assert first["ExitCode"] == 0 and first["Bench"].get("Lease") is None and receipt("task")
        old = run([args.old_bench, "status", "--state", state, "--data", data])
        assert old.returncode != 0 and "unsupported state schema" in old.stderr
        daemon = subprocess.Popen([args.benchd, "--state", str(state), "--data", str(data),
                                   "--socket", str(sock), "--cgroup-root", str(cg)],
                                  stdout=daemon_log, stderr=daemon_log)

        def ready():
            assert daemon.poll() is None, "daemon exited"
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
        good([args.bench, "setup", "--socket", sock, "--request-id", "setup-second",
              "--holder", "second", "--offline", "task"])
        path = data / "benches/task"
        assert (path / ".setup-events").read_text() == "onceeach:0each:1"
        print("PASS: schema-6 state upgrades; old binary refuses; once persists across CLI/daemon and each honors explicit offline environment", flush=True)
        replace_config("task", config + "# reviewed configuration revision\n")
        cli("setup", "--holder", "changed-config", "task")
        assert (path / ".setup-events").read_text() == "onceeach:0each:1onceeach:0"
        print("PASS: changed configuration invalidates the successful once receipt", flush=True)
        # A failed different configuration must also invalidate the former success.
        replace_config("task", "[setup]\nonce='printf partialB >> .setup-events; exit 7'\n")
        partial = run(command("setup", "--holder", "partial", "task"))
        assert partial.returncode == 7 and receipt("task") is None
        token = json.loads(partial.stdout)["Bench"]["Lease"]["Token"]
        cli("checkpoint", "--token", token, "task")
        cli("release", "--token", token, "task")
        replace_config("task", config + "# reviewed configuration revision\n")
        cli("setup", "--holder", "restored", "task")
        assert (path / ".setup-events").read_text().count("once") == 3
        print("PASS: failed alternate preparation revokes the prior once receipt before effects; restoring old config reruns once", flush=True)

        cli("create", "--repo", remote, "failure")
        failed_path = replace_config("failure", "[setup]\nonce='printf once >> .setup-events'\neach='exit 7'\n")
        failed = run(command("setup", "--holder", "failure", "failure"))
        assert failed.returncode == 7, failed.stderr
        result = json.loads(failed.stdout)
        assert result["Bench"]["Lease"]["Execution"]["Phase"] == "stopped" and receipt("failure") is None
        assert (failed_path / ".setup-events").read_text() == "once"
        assert run(command("setup", "--holder", "other", "failure")).returncode != 0
        assert (failed_path / ".setup-events").read_text() == "once"
        print("PASS: each failure retains a fenced lease and output, records no success, and blocks another setup", flush=True)

        cli("create", "--repo", remote, "changed")
        changed = replace_config("changed", "[setup]\nonce='printf changed > .bench/config.toml'\n")
        assert run(command("setup", "--holder", "changed", "changed")).returncode != 0
        assert receipt("changed") is None and (changed / ".bench/config.toml").read_text() == "changed"
        assert cli("status", "changed")[0]["Lease"]["Execution"]["Phase"] == "stopped"
        print("PASS: config mutation during hooks is preserved and refuses a setup success receipt", flush=True)

        cli("create", "--repo", remote, "cancel")
        canceled = replace_config("cancel", "[setup]\nonce='sleep 90 & echo $! > .hook-pid; touch .hook-ready; wait'\n")
        process = subprocess.Popen(list(map(str, command("setup", "--holder", "cancel", "cancel"))),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        wait_for((canceled / ".hook-ready").exists, "hook child ready")
        process.terminate()
        _, error = process.communicate(timeout=15)
        assert process.returncode != 0, error
        process = None
        assert receipt("cancel") is None
        status = cli("status", "cancel")[0]
        assert status["Lease"]["Execution"]["Phase"] == "stopped"
        assert not (cg / status["Lease"]["Execution"]["ID"]).exists()
        print("PASS: cancellation fences hook descendants, retains the lease and records no successful preparation", flush=True)
        daemon.terminate()
        assert daemon.wait(timeout=15) == 0
        daemon = None
        print("DONE: explicit repository setup integration passed", flush=True)
    finally:
        for child in (process, daemon):
            if child is not None and child.poll() is None:
                child.kill()
                child.wait(timeout=10)
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
