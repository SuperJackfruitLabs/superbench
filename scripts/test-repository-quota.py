#!/usr/bin/env python3
"""Actual bounded repository quotas, legacy upgrade and immutable allocation retry."""
import errno
import json
import os
from pathlib import Path
import shutil
import signal
import sqlite3
import subprocess
import sys
import time


def main():
    root = Path(sys.argv[1])
    bench, helper, old_bench, old_helper = map(str, map(Path, sys.argv[2:6]))
    cgroups = Path(sys.argv[6])
    assert os.getuid() == 0 and os.geteuid() == 0 and not os.environ.get("SUDO_UID"), "direct disposable Linux root required"
    data, state = root / "data", root / "state"
    policy, ledger = root / "policy.json", root / "ledger/assignments.json"
    current_helper = root / "current-helper"
    shutil.copyfile(old_helper, current_helper)
    current_helper.chmod(0o700)
    settings = {
        "Root": str(data), "Ledger": str(root / "ledger"), "OwnerUID": 0, "OwnerGID": 0,
        "BenchBytes": 16 << 20, "StoreBytes": 16 << 20, "MirrorBytes": 32 << 20,
        "ReserveBytes": 64 << 20, "BenchInodes": 1024, "StoreInodes": 8192,
        "MirrorInodes": 8192, "ReserveInodes": 1024,
    }
    policy.write_text(json.dumps(settings))
    policy.chmod(0o600)
    process = None

    def run(words, **kwargs):
        return subprocess.run(list(map(str, words)), capture_output=True, text=True, timeout=60, **kwargs)

    def good(words, **kwargs):
        result = run(words, **kwargs)
        assert result.returncode == 0, (words, result.stderr)
        return result.stdout.strip()

    def command(binary, operation, *words):
        return [binary, operation, "--state", state, "--data", data, "--cgroup-root", cgroups, *words]

    def cli(operation, *words):
        return json.loads(good(command(bench, operation, *words)))

    def previous(operation, *words):
        return json.loads(good(command(old_bench, operation, *words)))

    def git(path, *words):
        return good(["git", "-C", path, *words])

    def allocations():
        return json.loads(ledger.read_text())

    def quota(project, unit="-b"):
        report = good(["xfs_quota", "-x", "-c", f"report -p -n {unit} -N -L {project} -U {project}", data])
        fields = next(line.split() for line in report.splitlines()
                      if line.split() and line.split()[0] == f"#{project}")
        return int(fields[1]), int(fields[3])

    def finish(key, holder="worker", repair=False):
        token = cli("lease-repair" if repair else "lease", "--holder", holder, key)["Lease"]["Token"]
        cli("checkpoint", "--token", token, key)
        cli("release", "--token", token, key)

    try:
        # The old CLI/helper produce a genuine legacy ledger. No synthetic
        # conversion fixture can stand in for older executable compatibility.
        previous("configure-host", "--quota-helper", current_helper, "--quota-policy", policy,
                 "--min-data-free", 8 << 20, "--min-state-free", 1 << 20)
        seed, remote = root / "seed", root / "remote.git"
        git(root, "init", "--bare", remote)
        git(root, "init", "-b", "main", seed)
        (seed / "file").write_text("confirmed source\n")
        (seed / ".gitignore").write_text("fill/\n")
        git(seed, "add", ".")
        git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "legacy fixture")
        git(seed, "remote", "add", "origin", remote)
        git(seed, "push", "origin", "main")
        git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
        legacy = previous("create-cold", "--repo", remote, "legacy")
        legacy_ledger = allocations()
        assert legacy_ledger["VolumeID"] and legacy_ledger.get("FormatVersion", 0) == 0
        with sqlite3.connect(state / "bench.db") as db:
            assert db.execute("PRAGMA user_version").fetchone()[0] == 10
        original = legacy_ledger["Assignments"]["bench:legacy"]
        shutil.copyfile(helper, current_helper)
        current_helper.chmod(0o700)
        cli("migrate")
        finish("legacy")
        upgraded = allocations()
        assert upgraded["FormatVersion"] == 2 and "VolumeID" not in upgraded
        assert upgraded["FilesystemUUID"] == legacy_ledger["VolumeID"]
        assert upgraded["Assignments"]["bench:legacy"] == original
        assert quota(legacy["ProjectID"])[1] == settings["BenchBytes"] // 1024
        refused = run([old_helper, "--policy", policy, "apply", "bench", "legacy"])
        assert refused.returncode != 0 and "ledger" in refused.stderr
        assert allocations() == upgraded
        assert run(command(old_bench, "status", "legacy")).returncode != 0
        print("PASS: genuine legacy state/ledger upgrade preserves IDs, limits and filesystem identity; old CLI/helper refuse without changing allocation", flush=True)

        peer = cli("create-cold", "--repo", remote, "peer")

        def publish(size):
            (seed / ".bench").mkdir(exist_ok=True)
            (seed / ".bench/config.toml").write_text(f"[quota]\nsize='{size}'\n")
            git(seed, "add", ".bench/config.toml")
            git(seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "repository quota")
            git(seed, "push", "origin", "main")
            return git(seed, "rev-parse", "HEAD")

        publish("4M")
        small = cli("create-cold", "--repo", remote, "small")
        assert small["RequestedQuotaBytes"] == small["QuotaBytes"] == 4 << 20
        assert small["QuotaInodes"] == settings["BenchInodes"]
        assert quota(small["ProjectID"])[1] == 4096
        before = allocations()
        assert run([old_helper, "--policy", policy, "apply", "bench", "small"]).returncode != 0
        assert allocations() == before and quota(small["ProjectID"])[1] == 4096
        fill = Path(small["Path"]) / "fill"
        fill.mkdir()
        payload = fill / "quota-test"
        written = 0
        with payload.open("wb", buffering=0) as stream:
            try:
                for _ in range(24):
                    written += stream.write(b"x" * (1 << 20))
            except OSError as error:
                assert error.errno in (errno.EDQUOT, errno.ENOSPC), error
            else:
                raise AssertionError("repository quota did not stop writes")
        assert written < 4 << 20
        (Path(peer["Path"]) / "peer-write").write_bytes(b"y" * (1 << 20))
        (data / "store/independent-write").write_bytes(b"z" * (1 << 20))
        payload.unlink()
        print("PASS: repository 4M request is physically enforced while peer and shared-store projects remain writable", flush=True)

        before = allocations()
        publish("32M")
        refused = run(command(bench, "create-cold", "--repo", remote, "oversized"))
        assert refused.returncode != 0 and "ceiling" in refused.stderr
        assert not (data / "benches/oversized").exists()
        assert allocations() == before
        assert json.loads((state / "create-oversized.json").read_text())["Bench"]["RequestedQuotaBytes"] == 32 << 20
        for requested in ("0", "513", "1048577", str(32 << 20), "-1"):
            refused = run([helper, "--policy", policy, "apply", "bench", "invalid-size", requested])
            assert refused.returncode != 0
            assert allocations() == before
        assert run([helper, "--policy", policy, "apply", "shared", "store", str(4 << 20)]).returncode != 0
        assert allocations() == before
        print("PASS: above-ceiling, fractional, zero and shared-resource requests refuse before assigning projects or materializing checkout data", flush=True)

        # Capture the lost reply after the real helper durably chooses the
        # project and its smaller size. Advance the remote before retry.
        pinned_source = publish("8M")
        wrapper = root / "quota-wrapper"
        wrapper.write_text('''#!/bin/sh
"$QUOTA_REAL_HELPER" "$@"
status=$?
if [ "$status" = 0 ] && [ "$QUOTA_PAUSE_KEY" != "" ]; then
 for word in "$@";do
  if [ "$word" = "$QUOTA_PAUSE_KEY" ];then
   echo ready > "$QUOTA_MARKER"
   while :;do sleep 1;done
  fi
 done
fi
exit "$status"
''')
        wrapper.chmod(0o700)
        # The configured executable path remains fixed. A fixture-only wrapper
        # replaces its bytes after saving the real binary elsewhere.
        real = root / "real-helper"
        shutil.copyfile(helper, real)
        real.chmod(0o700)
        shutil.copyfile(wrapper, current_helper)
        current_helper.chmod(0o700)
        marker = root / "allocation-ready"
        environment = dict(os.environ, QUOTA_REAL_HELPER=str(real), QUOTA_PAUSE_KEY="retry", QUOTA_MARKER=str(marker))
        process = subprocess.Popen(list(map(str, command(bench, "create-cold", "--repo", remote, "retry"))),
                                   env=environment, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   text=True, start_new_session=True)
        deadline = time.monotonic() + 20
        while not marker.exists():
            assert process.poll() is None, process.communicate()
            assert time.monotonic() < deadline, "lost-reply marker timeout"
            time.sleep(.02)
        os.killpg(process.pid, signal.SIGKILL)
        process.communicate(timeout=10)
        process = None
        saved = allocations()
        assigned = saved["Assignments"]["bench:retry"]
        assert assigned["Bytes"] == 8 << 20
        intent = json.loads((state / "create-retry.json").read_text())
        assert intent["Source"] == pinned_source and intent["QuotaRequestPinned"]
        assert intent["Bench"]["RequestedQuotaBytes"] == 8 << 20
        assert not list((data / "benches/retry").iterdir())
        advanced = publish("2M")
        assert advanced != pinned_source
        shutil.copyfile(helper, current_helper)
        current_helper.chmod(0o700)
        resumed = cli("create-cold", "--repo", remote, "retry")
        assert resumed["ProjectID"] == assigned["ProjectID"]
        assert resumed["RequestedQuotaBytes"] == resumed["QuotaBytes"] == 8 << 20
        assert git(resumed["Path"], "rev-parse", "HEAD") == pinned_source
        assert allocations()["Next"] == saved["Next"]
        assert quota(resumed["ProjectID"])[1] == 8192
        same = json.loads(good([helper, "--policy", policy, "apply", "bench", "retry"]))
        assert same["ProjectID"] == assigned["ProjectID"] and same["Bytes"] == 8 << 20
        before = allocations()
        for size in (4 << 20, 16 << 20):
            assert run([helper, "--policy", policy, "apply", "bench", "retry", size]).returncode != 0
            assert allocations() == before
            assert quota(resumed["ProjectID"])[1] == 8192
        print("PASS: SIGKILL after privileged allocation retries the same ID, size and pinned commit after remote advancement; default or explicit requests cannot resize it", flush=True)

        # Repository edits are ordinary saved work, not authority to resize.
        held = cli("lease-repair", "--holder", "edit-quota", "small")["Lease"]
        small_path = Path(small["Path"])
        (small_path / ".bench/config.toml").write_text("[quota]\nsize='8M'\n")
        git(small_path, "add", ".bench/config.toml")
        git(small_path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "request a different allocation")
        checkpoint = cli("checkpoint", "--token", held["Token"], "small")["Checkpoint"]
        cli("release", "--token", held["Token"], "small")
        refused = run(command(bench, "lease", "--holder", "blocked", "small"))
        assert refused.returncode != 0 and "quota request changed" in refused.stderr
        forbidden = root / "forbidden-command"
        refused = run(command(bench, "run", "--holder", "blocked", "small", "--", "touch", forbidden))
        assert refused.returncode != 0 and "quota request changed" in refused.stderr
        assert not forbidden.exists() and not cli("status", "small")[0].get("Lease")
        assert quota(small["ProjectID"])[1] == 4096
        finish("small", holder="repair-edited-quota", repair=True)
        print("PASS: changed repository quota blocks acquisition/run without resizing; a fresh repair lease can checkpoint/release", flush=True)

        cli("archive", "--archive-grace", "0s", "small")
        plan = cli("plan-reap", "small")
        plan_path = root / "small-reap.json"
        plan_path.write_text(json.dumps(plan))
        assert cli("reap", "--plan", plan_path, "--approve", plan["Digest"], "small")["ReapedAt"]
        retired = allocations()["Assignments"]["bench:small"]
        assert retired["ProjectID"] == small["ProjectID"] and retired["Bytes"] == retired["Inodes"] == 0
        assert run([helper, "--policy", policy, "apply", "bench", "small", 4 << 20]).returncode != 0
        print("PASS: reviewed reap retires the smaller allocation and its project ID permanently", flush=True)

        # The original branch can only be attached elsewhere after its first
        # worktree is removed. Its confirmed checkpoint requests the new cap.
        recovered = cli("recover-cold", "--repo", remote, "--branch", small["Branch"], "recovered")
        assert recovered["QuotaBytes"] == recovered["RequestedQuotaBytes"] == 8 << 20
        assert git(recovered["Path"], "rev-parse", "HEAD") == checkpoint
        print("PASS: recovery of the saved branch under a new key gets its new bounded allocation", flush=True)

        # Pin the ceiling independently of whether active allocations happen to
        # fit a newly edited policy; a root policy edit is not an implicit resize.
        changed = root / "changed-policy.json"
        changed.write_text(json.dumps({**settings, "BenchBytes": 32 << 20}))
        changed.chmod(0o600)
        before = allocations()
        assert run([helper, "--policy", changed, "apply", "bench", "policy-drift", 8 << 20]).returncode != 0
        assert allocations() == before
        assert not (data / "benches/policy-drift").exists()
        print("PASS: changing the host ceiling is refused even when all active smaller allocations would fit it", flush=True)

        settings["BenchInodes"] += 128
        policy.write_text(json.dumps(settings))
        policy.chmod(0o600)
        assert run([helper, "--policy", policy, "apply", "bench", "retry"]).returncode != 0
        migrated = json.loads(good([helper, "--policy", policy, "migrate-inodes"]))
        migrated_retry = next(item for item in migrated if item["ProjectID"] == resumed["ProjectID"])
        assert migrated_retry["Bytes"] == 8 << 20 and migrated_retry["Inodes"] == settings["BenchInodes"]
        assert quota(resumed["ProjectID"])[1] == 8192
        assert quota(resumed["ProjectID"], "-i")[1] == settings["BenchInodes"]
        assert allocations()["Assignments"]["bench:small"]["Bytes"] == 0
        print("PASS: explicit inode migration preserves the smaller byte cap and project identity without reviving retired allocations", flush=True)
        print("DONE: repository quota integration passed", flush=True)
    finally:
        if process is not None and process.poll() is None:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate(timeout=10)


if __name__ == "__main__":
    main()
