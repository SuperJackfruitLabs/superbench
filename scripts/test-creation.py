#!/usr/bin/env python3
"""Exercise canonical warm-creation scenarios on a disposable Linux host."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import uuid

spec = importlib.util.spec_from_file_location("repository_setup", Path(__file__).with_name("test-repository-setup.py"))
support = importlib.util.module_from_spec(spec)
spec.loader.exec_module(support)
UnixHTTP, wait_for = support.UnixHTTP, support.wait_for


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--benchd", required=True)
    parser.add_argument("--old-benchd", required=True,
                        help="published pre-initialize service")
    parser.add_argument("--corpus", type=Path, default=Path(__file__).resolve().parents[1]
                        / "internal/protocol/testdata/creation.json")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("disposable Linux root required")
    corpus = json.loads(args.corpus.read_text())
    assert corpus["version"] == "superbench.creation.v1"
    root = Path(tempfile.mkdtemp(prefix="sb-creation-", dir="/var/tmp"))
    cg = Path("/sys/fs/cgroup") / ("sb-creation-" + uuid.uuid4().hex)
    cg.mkdir()
    daemon = None
    log = (root / "daemon.log").open("wb")

    def good(words):
        result = subprocess.run(list(map(str, words)), capture_output=True,
                                text=True, timeout=30)
        assert result.returncode == 0, (words, result.stderr)
        return result.stdout

    def fixture(name, scenario):
        home = root / name
        home.mkdir()
        seed, remote = home / "seed", home / "remote.git"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / ".gitignore").write_text(".setup-events\n.hook-ready\n.hook-release\n")
        if scenario == "ambiguous-lockfiles":
            (seed / "package-lock.json").write_text("{}\n")
            (seed / "pnpm-lock.yaml").write_text("lockfileVersion: '9.0'\n")
        elif scenario == "invalid-config":
            (seed / ".bench").mkdir()
            (seed / ".bench/config.toml").write_text("[setup]\nonse='touch .setup-events'\n")
        elif scenario != "none":
            (seed / ".bench").mkdir()
            command = {
                "configured": "printf once >> .setup-events",
                "failure": "printf failed >> .setup-events; exit 7",
                "dirty": "printf dirty > preserved-work",
                "blocked": "touch .hook-ready; while ! test -e .hook-release; do sleep .05; done; printf once >> .setup-events",
            }[scenario]
            (seed / ".bench/config.toml").write_text("[setup]\nonce=" + json.dumps(command) + "\n")
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c",
              "user.email=test@example.invalid", "commit", "-m", "fixture"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])
        return home, seed, remote

    def cli(home, op, *words):
        return json.loads(good([args.bench, op, "--state", home / "state",
                                "--data", home / "data", "--cgroup-root", cg, *words]))

    def rpc(home, request):
        connection = UnixHTTP(home / "api.sock")
        connection.request("POST", "/v1/operations", json.dumps(request),
                           {"Content-Type": "application/json"})
        response = connection.getresponse()
        reply = json.loads(response.read())
        connection.close()
        return response.status, reply

    def start(home, binary):
        nonlocal daemon
        daemon = subprocess.Popen([binary, "--state", str(home / "state"),
                                   "--data", str(home / "data"), "--socket",
                                   str(home / "api.sock"), "--cgroup-root", str(cg)],
                                  stdout=log, stderr=log)

        def ready():
            assert daemon.poll() is None, "daemon exited"
            try:
                c = UnixHTTP(home / "api.sock")
                c.request("GET", "/healthz")
                r = c.getresponse()
                r.read()
                c.close()
                return r.status == 200
            except OSError:
                return False
        wait_for(ready, "daemon ready")

    def stop():
        nonlocal daemon
        daemon.terminate()
        assert daemon.wait(timeout=15) == 0
        daemon = None

    try:
        for case in corpus["cases"]:
            home, seed, remote = fixture(case["name"], case["scenario"])
            request = dict(case["request"])
            # Bind only the abstract repository location to this disposable Git
            # fixture. Keep the canonical operation, key, ID and flags unchanged.
            request["repository"] = str(remote)
            if request["operation"] == "recover":
                good(["git", "-C", seed, "push", "origin", "HEAD:refs/heads/" + request["branch"]])
            start(home, args.benchd)
            status, reply = rpc(home, request)
            expected = case["expect"]
            assert status == expected["status"] and reply["ok"] == expected["ok"], (case["name"], status, reply)
            bench = reply["result"]["bench"]
            token = bench.get("lease", {}).get("token")
            assert bool(token) == expected["held_lease_token"]
            assert ("lease" not in bench) == expected["ready_for_acquisition"]
            assert rpc(home, request) == (status, reply), "retry changed the recorded result"
            key = request["key"]
            public = rpc(home, {"version": "superbench.v1", "operation": "status", "key": key})[1]
            assert "token" not in public["result"]["benches"][0].get("lease", {})
            history = cli(home, "history", key)
            code = expected["setup_exit_code"]
            assert len(history) == (0 if code is None else 1), history
            if history:
                assert history[0]["ExitCode"] == code, history
                assert bench.get("lease", {}).get("execution", {}).get("phase", "stopped") == "stopped"
            elif token:
                assert "execution" not in bench["lease"], "planning failure invented a supervised execution"
            if expected["ok"] and code is not None:
                assert bench["checkpoint"] == good(["git", "-C", remote, "rev-parse", "refs/heads/" + bench["branch"]]).strip()
                assert not history[0]["ReleasedAt"].startswith("0001-")
            path = home / "data/benches" / key
            if case["scenario"] == "dirty":
                assert (path / "preserved-work").read_text() == "dirty"
                assert "?? preserved-work" in good(["git", "-C", path, "status", "--porcelain"])
            elif case["scenario"] in ("none", "invalid-config", "ambiguous-lockfiles"):
                assert not (path / ".setup-events").exists()
            else:
                assert (path / ".setup-events").read_text() == ("failed" if code == 7 else "once")
            with sqlite3.connect(home / "state/bench.db") as db:
                receipt = db.execute("SELECT run_id FROM setup_receipts WHERE bench=?", (key,)).fetchone()
            assert bool(receipt) == (code == 0)
            again = dict(request, request_id=request["request_id"] + "-new")
            assert rpc(home, again)[0] == 409, "registered creation executed again"
            print("PASS: canonical " + case["name"] + " including replay, ownership, output and checkpoint evidence", flush=True)
            stop()

        # New clients must fail closed against old services. The same service
        # must still accept an omitted initialize field as a cold legacy request.
        home, _, remote = fixture("compatibility", "configured")
        request = {"version": "superbench.v1", "operation": "create", "key": "task",
                   "repository": str(remote), "request_id": "warm", "initialize": True}
        start(home, args.old_benchd)
        assert rpc(home, request)[0] == 400
        assert not (home / "data/benches/task").exists()
        stop()
        start(home, args.benchd)
        for value in (False, None, "true"):
            assert rpc(home, dict(request, initialize=value))[0] == 400
        legacy = dict(request)
        del legacy["initialize"]
        assert rpc(home, legacy)[0] == 200
        assert not (home / "data/benches/task/.setup-events").exists()
        assert cli(home, "history", "task") == []
        stop()
        print("PASS: old service rejects explicit initialization before effects; new service preserves cold omission and rejects invalid flags", flush=True)

        home, seed, remote = fixture("cli-defaults", "configured")
        created = cli(home, "create", "--repo", remote, "local")
        assert created.get("Lease") is None and created["Checkpoint"]
        assert (Path(created["Path"]) / ".setup-events").read_text() == "once"
        assert len(cli(home, "history", "local")) == 1
        cold = cli(home, "create-cold", "--repo", remote, "cold")
        assert not (Path(cold["Path"]) / ".setup-events").exists()
        assert cli(home, "history", "cold") == []
        good(["git", "-C", seed, "push", "origin", "HEAD:refs/heads/bench/recovered"])
        recovered = cli(home, "recover", "--repo", remote,
                        "--branch", "bench/recovered", "recovered")
        assert recovered.get("Lease") is None and recovered["Checkpoint"]
        assert (Path(recovered["Path"]) / ".setup-events").read_text() == "once"
        start(home, args.benchd)
        output = good([args.bench, "create", "--socket", home / "api.sock",
                       "--request-id", "cli-socket-create", "--repo", remote, "socket"])
        created = json.loads(output)["bench"]
        assert "lease" not in created and created["checkpoint"]
        assert (Path(created["path"]) / ".setup-events").read_text() == "once"
        stop()
        print("PASS: local create/recover and socket CLI prepare by default; explicit local cold creation runs no setup", flush=True)

        home, _, remote = fixture("cli-old-service", "configured")
        start(home, args.old_benchd)
        refused = subprocess.run(list(map(str, [args.bench, "create", "--socket",
                                 home / "api.sock", "--request-id", "cli-old",
                                 "--repo", remote, "task"])), capture_output=True, timeout=15)
        assert refused.returncode != 0 and not (home / "data/benches/task").exists()
        stop()
        print("PASS: socket CLI never falls back to cold creation after old-service rejection", flush=True)

        home, _, remote = fixture("interrupted", "blocked")
        request = dict(request, repository=str(remote), request_id="interrupted-create")
        path = home / "data/benches/task"
        start(home, args.benchd)
        # Submit without awaiting completion; killing the service must leave the
        # registered reservation and supervised process discoverable.
        connection = UnixHTTP(home / "api.sock")
        connection.request("POST", "/v1/operations", json.dumps(request),
                           {"Content-Type": "application/json"})
        connection.close()
        wait_for((path / ".hook-ready").exists, "initial setup started")
        state = cli(home, "status", "task")[0]
        assert state["Lease"]["Execution"]["Phase"] == "running", state
        denied = subprocess.run(list(map(str, [args.bench, "lease", "--state", home / "state",
                                "--data", home / "data", "--cgroup-root", cg,
                                "--holder", "competitor", "task"])), capture_output=True, timeout=15)
        assert denied.returncode != 0
        assert "token" not in rpc(home, {"version": "superbench.v1", "operation": "status", "key": "task"})[1]["result"]["benches"][0]["lease"]
        daemon.kill()
        daemon.wait(timeout=10)
        daemon = None
        held = cli(home, "stop", "--admin", "task")["Lease"]
        assert held["Execution"]["Phase"] == "stopped"
        assert not (path / ".setup-events").exists()
        cli(home, "checkpoint", "--token", held["Token"], "task")
        cli(home, "release", "--token", held["Token"], "task")
        start(home, args.benchd)
        status, reply = rpc(home, request)
        assert status == 409 and reply["error"]["code"] == "operation_in_doubt", reply
        assert not (path / ".setup-events").exists()
        stop()
        print("PASS: initialization blocks competing ownership; SIGKILL retains a fenceable lease and interrupted request never redispatches", flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            daemon.kill()
            daemon.wait(timeout=10)
        (cg / "cgroup.kill").write_text("1")
        wait_for(lambda: "populated 0" in (cg / "cgroup.events").read_text(), "cgroup empty")
        for child in cg.iterdir():
            if child.is_dir():
                child.rmdir()
        cg.rmdir()
        log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
