#!/usr/bin/env python3
"""Actual CLI grace checks and recovery of a pre-upgrade approved deletion."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import sqlite3
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--old-bench", required=True, help="published schema-8 or schema-9 CLI")
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="sb-archive-grace-"))
    state, data = root / "state", root / "data"
    process = None

    def run(words, **kwargs):
        return subprocess.run(list(map(str, words)), capture_output=True,
                              text=True, timeout=30, **kwargs)

    def good(words):
        r = run(words)
        assert r.returncode == 0, (words, r.stderr)
        return r.stdout

    def command(binary, op, *words):
        return [binary, op, "--state", state, "--data", data, *words]

    def cli(binary, op, *words):
        return json.loads(good(command(binary, op, *words)))

    def prepare(binary, key):
        b = cli(binary, "create", "--repo", remote, key)
        held = cli(binary, "lease", "--holder", "writer", key)["Lease"]
        cli(binary, "checkpoint", "--token", held["Token"], key)
        cli(binary, "release", "--token", held["Token"], key)
        (Path(b["Path"]) / "artifact").write_text("retained ignored output")
        return b

    def plan_file(binary, key):
        p = cli(binary, "plan-reap", key)
        path = root / (key + ".json")
        path.write_text(json.dumps(p))
        return p, path

    try:
        seed, remote = root / "seed", root / "remote.git"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / "file").write_text("committed source")
        (seed / ".gitignore").write_text("artifact\n")
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])

        # Capture a genuine v1 journal committed by the old CLI, then SIGKILL
        # after it removed approved data but before registration/finalization.
        previous = prepare(args.old_bench, "legacy")
        with sqlite3.connect(state / "bench.db") as db:
            old_version = db.execute("PRAGMA user_version").fetchone()[0]
        if old_version >= 9:
            cli(args.old_bench, "archive", "--archive-grace", "0s", "legacy")
        else:
            cli(args.old_bench, "archive", "legacy")
        old_plan, old_file = plan_file(args.old_bench, "legacy")
        assert old_plan["Version"] in ("superbench.reap.v1", "superbench.reap.v2")
        marker = root / "retirement-ready"
        wrapper = root / "wrapper"
        wrapper.mkdir()
        real_git = shutil.which("git")
        (wrapper / "git").write_text("#!/bin/sh\nif [ \"$3\" = worktree ] && [ \"$4\" = list ]; then touch " + shlex.quote(str(marker)) + "; exec sleep 90; fi\nexec " + shlex.quote(real_git) + " \"$@\"\n")
        (wrapper / "git").chmod(0o700)
        env = dict(os.environ, PATH=str(wrapper) + os.pathsep + os.environ["PATH"])
        process = subprocess.Popen(list(map(str, command(args.old_bench, "reap", "--plan", old_file,
                                   "--approve", old_plan["Digest"], "legacy"))), env=env,
                                   start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        deadline = time.monotonic() + 15
        while not marker.exists():
            assert process.poll() is None, process.communicate()
            assert time.monotonic() < deadline, "old deletion did not reach finalization"
            time.sleep(.05)
        os.killpg(process.pid, signal.SIGKILL)
        process.communicate(timeout=10)
        process = None
        assert not Path(previous["Path"]).exists()
        cli(args.bench, "migrate")
        migrated = cli(args.bench, "status", "legacy")[0]
        assert migrated["ReapAfter"] and migrated["ReapingDigest"] == old_plan["Digest"]
        result = cli(args.bench, "reap", "--plan", old_file, "--approve", old_plan["Digest"], "legacy")
        assert result["ReapedAt"]
        assert cli(args.bench, "reap", "--plan", old_file, "--approve", old_plan["Digest"], "legacy")["ReapedAt"] == result["ReapedAt"]
        assert run(command(args.old_bench, "status")).returncode != 0
        print(f"PASS: schema-{old_version} SIGKILL leaves a genuine {old_plan['Version']} intent; schema-10 migration resumes exact approval and preserves replay/downgrade guards", flush=True)

        b = prepare(args.bench, "retained")
        for duration in ("-1s", "8760h1s"):
            assert run(command(args.bench, "archive", "--archive-grace", duration, "retained")).returncode != 0
        assert not cli(args.bench, "status", "retained")[0].get("ArchivedAt")
        archived = cli(args.bench, "archive", "retained")
        at = datetime.datetime.fromisoformat(archived["ArchivedAt"].replace("Z", "+00:00"))
        after = datetime.datetime.fromisoformat(archived["ReapAfter"].replace("Z", "+00:00"))
        assert after - at == datetime.timedelta(days=7)
        assert cli(args.bench, "archive", "--archive-grace", "0s", "retained")["ReapAfter"] == archived["ReapAfter"]
        plan, file = plan_file(args.bench, "retained")
        refused = run(command(args.bench, "reap", "--plan", file, "--approve", plan["Digest"], "retained"))
        assert refused.returncode != 0 and "grace has not elapsed" in refused.stderr
        assert (Path(b["Path"]) / "artifact").read_text() == "retained ignored output"
        assert not (state / "reap-retained.json").exists()
        forged = dict(plan, ReapAfter=archived["ArchivedAt"], Digest="")
        forged["Digest"] = hashlib.sha256(json.dumps(forged, separators=(",", ":")).encode()).hexdigest()
        file.write_text(json.dumps(forged))
        refused = run(command(args.bench, "reap", "--plan", file, "--approve", forged["Digest"], "retained"))
        assert refused.returncode != 0 and "deadline conflicts" in refused.stderr, refused.stderr
        print("PASS: default seven-day grace, pre-effect rejection, immutable retry deadline and forged-plan rejection preserve checkout/artifacts", flush=True)

        prepare(args.bench, "explicit-zero")
        cli(args.bench, "archive", "--archive-grace", "0s", "explicit-zero")
        plan, file = plan_file(args.bench, "explicit-zero")
        assert plan["Version"] == "superbench.reap.v3" and plan["ReapAfter"]
        assert cli(args.bench, "reap", "--plan", file, "--approve", plan["Digest"], "explicit-zero")["ReapedAt"]
        with sqlite3.connect(state / "bench.db") as db:
            assert db.execute("PRAGMA user_version").fetchone()[0] == 10
        print("PASS: explicit zero grace still requires an exact reviewed v3 manifest; schema 10 is persisted", flush=True)
    finally:
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate(timeout=10)
        for directory, _, _ in os.walk(root, topdown=True):
            os.chmod(directory, 0o700)
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
