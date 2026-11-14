#!/usr/bin/env python3
"""Verify legacy archive conversion and real SIGKILL permission recovery."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--old-bench", required=True, help="published schema-9 CLI")
    args = parser.parse_args()
    root = Path(tempfile.mkdtemp(prefix="sb-permission-drill-"))
    state, data = root / "state", root / "data"
    child = None

    def run(words):
        return subprocess.run(list(map(str, words)), capture_output=True, text=True, timeout=60)

    def good(words):
        r = run(words)
        assert r.returncode == 0, (words, r.stderr)
        return r.stdout

    def command(binary, operation, *words):
        return [binary, operation, "--state", state, "--data", data, *words]

    def cli(binary, operation, *words):
        return json.loads(good(command(binary, operation, *words)))

    def prepare(binary, key):
        b = cli(binary, "create", "--repo", remote, key)
        held = cli(binary, "lease", "--holder", "writer", key)["Lease"]
        cli(binary, "checkpoint", "--token", held["Token"], key)
        cli(binary, "release", "--token", held["Token"], key)
        return b

    def save_plan(key):
        plan = cli(args.bench, "plan-reap", key)
        path = root / (key + ".json")
        path.write_text(json.dumps(plan))
        return plan, path

    def wait_for(check, label):
        deadline = time.monotonic() + 60
        while not check():
            assert child.poll() is None, (label, child.communicate())
            assert time.monotonic() < deadline, "timeout: " + label
            time.sleep(.002)

    def kill():
        nonlocal child
        os.killpg(child.pid, signal.SIGKILL)
        child.communicate(timeout=15)
        child = None

    try:
        remote, seed = root / "remote.git", root / "seed"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / "source").write_text("confirmed source")
        (seed / ".gitignore").write_text("bulk/\n")
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])

        legacy = prepare(args.old_bench, "legacy")
        archived = cli(args.old_bench, "archive", "--archive-grace", "0s", "legacy")
        old_plan = cli(args.old_bench, "plan-reap", "legacy")
        assert old_plan["Version"] == "superbench.reap.v2"
        old_file = root / "old-plan.json"
        old_file.write_text(json.dumps(old_plan))
        cli(args.bench, "migrate")
        assert run(command(args.bench, "plan-reap", "legacy")).returncode != 0
        frozen = cli(args.bench, "archive", "legacy")
        assert frozen["ReapAfter"] == archived["ReapAfter"] and frozen["ArchiveFrozenAt"]
        assert (Path(legacy["Path"]) / "source").stat().st_mode & 0o222 == 0
        assert run(command(args.bench, "reap", "--plan", old_file, "--approve", old_plan["Digest"], "legacy")).returncode != 0
        fresh, fresh_file = save_plan("legacy")
        cli(args.bench, "reap", "--plan", fresh_file, "--approve", fresh["Digest"], "legacy")
        print("PASS: schema-9 archives require explicit read-only conversion, preserve the deadline and reject unstarted v2 approvals", flush=True)

        b = prepare(args.bench, "interrupted")
        checkout = Path(b["Path"])
        bulk = checkout / "bulk"
        bulk.mkdir()
        for i in range(4096):
            (bulk / f"entry-{i:05}").write_text("retained output")
        child = subprocess.Popen(list(map(str, command(args.bench, "archive", "--archive-grace", "0s", "interrupted"))),
                                 start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        # Reverse traversal freezes source before the thousands of ignored
        # entries. Observe an actual fchmod, then kill before completion.
        wait_for(lambda: (checkout / "source").stat().st_mode & 0o222 == 0, "freeze started")
        kill()
        pending = cli(args.bench, "status", "interrupted")[0]
        assert pending["ArchiveFreezeDigest"] and not pending.get("ArchiveFrozenAt")
        assert run(command(args.bench, "lease", "--holder", "competitor", "interrupted")).returncode != 0
        assert run(command(args.bench, "plan-reap", "interrupted")).returncode != 0
        assert len(list(bulk.iterdir())) == 4096
        result = cli(args.bench, "archive", "interrupted")
        assert result["ArchiveFrozenAt"] and result["ReapAfter"] == pending["ReapAfter"]
        assert all(p.stat().st_mode & 0o222 == 0 for p in bulk.iterdir())
        print("PASS: SIGKILL after a real permission change retains a pending archive, blocks writers/plans and resumes only the original tree", flush=True)

        plan, plan_path = save_plan("interrupted")
        assert plan["Version"] == "superbench.reap.v3" and plan["FreezeDigest"] == result["ArchiveFreezeDigest"]
        child = subprocess.Popen(list(map(str, command(args.bench, "reap", "--plan", plan_path,
                                 "--approve", plan["Digest"], "interrupted"))),
                                 start_new_session=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        wait_for(lambda: checkout.stat().st_mode & 0o200 != 0, "approved directory thaw started")
        kill()
        pending = cli(args.bench, "status", "interrupted")[0]
        assert pending["ReapingDigest"] == plan["Digest"] and not pending.get("ReapedAt")
        (checkout / "new-work").write_text("keep after interrupted reap")
        assert run(command(args.bench, "reap", "--plan", plan_path, "--approve", plan["Digest"], "interrupted")).returncode != 0
        assert (checkout / "new-work").read_text() == "keep after interrupted reap"
        (checkout / "new-work").rename(root / "preserved-new-work")
        completed = cli(args.bench, "reap", "--plan", plan_path, "--approve", plan["Digest"], "interrupted")
        assert completed["ReapedAt"] and not checkout.exists()
        assert (root / "preserved-new-work").read_text() == "keep after interrupted reap"
        print("PASS: SIGKILL after directory thaw retains exact deletion approval; new files are preserved and the same reviewed intent resumes", flush=True)
    finally:
        if child is not None and child.poll() is None:
            kill()
        for directory, _, _ in os.walk(root, topdown=True):
            os.chmod(directory, 0o700)
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
