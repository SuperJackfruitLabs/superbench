#!/usr/bin/env python3
"""Draft managed same-host missing-source archive/reap acceptance, owned fixtures only."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3

spec = importlib.util.spec_from_file_location("missing_transfer", Path(__file__).with_name("test-workspace-missing-transfer.py"))
missing = importlib.util.module_from_spec(spec)
spec.loader.exec_module(missing)


def unchanged(d, evidence):
    assert d.source_evidence() == evidence, "history/logs/once receipts/historical HTTP bodies changed"
    source = d.record("source")
    runs = source["Workspace"]["Transfer"]["Recovery"]["Runs"]
    assert {r["ID"] for r in runs} >= {r["ID"] for r in d.history("source") if r["ExitCode"] is None}
    for record in d.history("source"):
        if record["ExitCode"] is None:
            assert record["ReleasedAt"].startswith("0001-") and record.get("Interruption")


def sql_fault(d, enabled):
    with sqlite3.connect(d.state / "bench.db") as db:
        db.execute("DROP TRIGGER IF EXISTS fixture_fail_reaped_insert")
        db.execute("DROP TRIGGER IF EXISTS fixture_fail_reaped_update")
        if enabled:
            for event in ("INSERT", "UPDATE"):
                db.execute("CREATE TRIGGER fixture_fail_reaped_" + event.lower() + " BEFORE " + event + " ON benches WHEN NEW.key='source' AND json_extract(NEW.record,'$.Workspace.Phase')='reaped' BEGIN SELECT RAISE(ABORT,'owned fixture final SQL failure'); END")


def refuse(d, operation, flags=()):
    return d.refuse(operation, "source", flags)


def reap(d, path, plan, expected=0):
    return d.run(d.local_words("reap", "source", ["--plan", path, "--approve", plan["Digest"]]), expected)


def root_return(d, operation, flags=(), member=False):
    root = d.root_for("source")
    root.mkdir()
    container = root / "a" if member else root
    if member:
        container.mkdir()
    marker = container / "returned-sentinel"
    marker.write_bytes(b"returned source retained\x00\xff")
    before = marker.read_bytes()
    refuse(d, operation, flags)
    assert marker.read_bytes() == before and root.is_dir()
    destination = d.root / ("preserved-returned-" + operation + "-" + d.record("source")["Workspace"]["Phase"])
    shutil.copytree(root, destination)
    assert missing.bytes_snapshot(root) == missing.bytes_snapshot(destination)
    marker.unlink()
    if member:
        container.rmdir()
    root.rmdir()


def advance_target(d):
    target = d.record("target")
    token = d.grant(d.request(d.payload("lease", key="target", holder="target-advancer"))[0])
    member = target["Workspace"]["Members"][0]
    path = Path(member["Path"])
    (path / "advanced.txt").write_text("new target only\n")
    d.git(path, "add", "advanced.txt")
    d.git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "advance transferred task ref")
    d.reconcile("target", token)
    old = d.record("source")["Workspace"]["Checkpoint"]
    new = d.record("target")["Workspace"]["Checkpoint"]
    assert old["Members"][0]["Commit"] != new["Members"][0]["Commit"]
    assert d.git(member["Repository"], "rev-parse", "refs/heads/" + member["Branch"]) == new["Members"][0]["Commit"]
    print("PASS: fresh target advances original task refs; source retains immutable old complete seal", flush=True)


def recover(d, whole):
    members = d.configure(); d.start()
    create = d.payload("create", key="source", repositories=members, checkpoint_repository="a", initialize=True)
    made, raw = d.request(create); d.roots["source"] = Path(made["result"]["root_path"]); d.sealed(made)
    uncertain, token, _ = missing.fence_unknown_writer(d)
    source, files, index, b_index, staged, reflog = missing.lose_owned_checkout(d)
    # A's staged blob is deliberately distinct from its actual workfile. Its
    # survival must come from retained administration, never checkout bytes.
    a = source["Workspace"]["Members"][0]
    a_staged = d.git(a["Path"], "rev-parse", ":source.txt")
    (Path(a["Path"]) / "source.txt").write_text("sealed base a\n")
    files = missing.bytes_snapshot(Path(a["Path"]))
    if not whole:
        plan = missing.transfer_and_repair(d, source, files, index, b_index, staged, reflog, create, raw, uncertain, token, run_retention_gc=False)
    else:
        shutil.rmtree(a["Path"])  # Only independently fenced disposable A.
        d.root_for("source").rmdir()
        plan = d.cli("inspect-missing-transfer", "source", remote=False, flags=["--target", "target", "--transfer-id", "whole-root-missing", "--quota-bytes", str(32 << 20)])
        assert all(m.get("Missing") for m in plan["Members"])
        path = d.planfile("whole-transfer", plan)
        d.cli("begin-missing-transfer", "source", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
        # Both historical held source and incomplete transfer refuse archive.
        before = d.record("source")
        refuse(d, "archive", ["--archive-grace", "0"])
        assert d.record("source") == before
        result = d.cli("continue-missing-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]])
        assert result["phase"] == "completed" and "grant_token" not in result
        d.roots["target"] = Path(result["root_path"])
        assert not d.root_for("source").exists()
    return source, plan, a_staged, staged, reflog


def archive_reap(d, whole, returned_before_freeze=False):
    source, transfer, a_staged, b_staged, reflog = recover(d, whole)
    evidence = d.source_evidence()
    advance_target(d)
    unchanged(d, evidence)
    root = d.root_for("source")
    if whole:
        root_return(d, "archive", ["--archive-grace", "0"], member=True)
    else:
        b = root / "b"; b.mkdir(); (b / "sentinel").write_text("return before archive")
        refuse(d, "archive", ["--archive-grace", "0"])
        assert (b / "sentinel").read_text() == "return before archive"
        d.preserve_returned("preserved-before-archive-b")
    if returned_before_freeze:
        root.mkdir()
        (root / "pre-freeze-sentinel").write_bytes(b"explicitly reviewed new retained bytes")
    # No matching concrete plan: deletion must not start.
    refuse(d, "reap")
    d.cli("archive", "source", remote=False, flags=["--archive-grace", "0"])
    archived = d.record("source")
    assert archived["Workspace"]["Phase"] == "archived"
    assert archived["Workspace"]["Checkpoint"] == source["Workspace"]["Checkpoint"]
    plan = d.cli("plan-reap", "source", remote=False)
    assert plan["Version"] == "superbench.workspace-missing-reap.v1" and plan["Missing"]["Root"]["Absent"] == (whole and not returned_before_freeze)
    assert plan["Retirement"]["Storage"]["Assignment"]["ProjectID"] == source["ProjectID"]
    assert not any(token in json.dumps(plan) for token in d.tokens)
    path = d.planfile("reviewed-reap", plan)
    if whole and not returned_before_freeze:
        assert not plan["Tree"]["Entries"]
        root_return(d, "reap", ["--plan", path, "--approve", plan["Digest"]])
    elif returned_before_freeze:
        entries = {e["Path"]: e for e in plan["Tree"]["Entries"]}
        assert entries["pre-freeze-sentinel"]["Recoverable"] is False
        assert (root / "pre-freeze-sentinel").read_bytes() == b"explicitly reviewed new retained bytes"
        assert not (root.stat().st_mode & 0o222)
        print("PASS: root-only pre-freeze return is captured as actual unrecoverable bytes under separate archive authority; no missing-member return adopted", flush=True)
    else:
        entries = {e["Path"]: e for e in plan["Tree"]["Entries"]}
        assert entries["a/source.txt"]["Recoverable"] is True
        assert entries["a/.bench/output/preserved-private"]["Recoverable"] is False
        present = plan["Missing"]["Present"][0]["Plan"]
        assert a_staged in {o["OID"] for o in present["Objects"]}
        assert (Path(present["Admin"]) / "index").read_bytes()
    # Keep a real file descriptor alive across reviewed deletion. Kernel project
    # usage must prevent retirement despite fixed-path absence.
    held = None
    if not whole:
        retained = root / "a/.bench/output/preserved-private"
        held = retained.open("rb")
        reap(d, path, plan, expected=1)
        assert not root.exists() and d.record("source")["Workspace"]["Phase"] == "reaping"
        allocation = d.ledger()["bench:source"]
        assert allocation["Bytes"] == source["QuotaBytes"] and allocation["Inodes"] == source["QuotaInodes"]
        assert held.read() == b"source A ignored bytes\x00\xff"
        held.close()
        print("PASS: actual open-unlinked source bytes retain original quota after approved root removal", flush=True)
    # Fail only the final authoritative row save, after real helper retirement.
    sql_fault(d, True)
    failed = reap(d, path, plan, expected=1)
    assert "owned fixture final SQL failure" in failed.stderr
    tombstone = d.ledger()["bench:source"]
    assert tombstone["ProjectID"] == source["ProjectID"] and tombstone["Bytes"] == tombstone["Inodes"] == 0
    assert not root.exists() and d.record("source")["Workspace"]["Phase"] == "reaping"
    sql_fault(d, False)
    ledger_path = d.root / "quota-ledger/assignments.json"
    ledger_bytes = ledger_path.read_bytes()
    # An empty returned root is ambiguous after the durable removal intent.
    # Ordinary retry must preserve it even though quota is already a tombstone.
    root.mkdir()
    root_stat = root.stat()
    reap(d, path, plan, expected=1)
    assert root.stat().st_ino == root_stat.st_ino and list(root.iterdir()) == []
    removal = d.state / "aggregate-root-removal-source.json"
    removal_bytes = removal.read_bytes()
    review = d.cli("inspect-root-removal", "source", remote=False, flags=["--disposition-id", "review-returned-empty-root"])
    assert review["Action"] == "remove-current-empty-directory" and not review["Root"]["Absent"]
    review_path = d.planfile("reviewed-current-empty-root", review)
    # Fresh review never authorizes deleting subsequently appeared content.
    sentinel = root / "after-review"; sentinel.write_bytes(b"preserved after fresh review")
    refuse(d, "reconcile-root-removal", ["--plan", review_path, "--approve", review["Digest"]])
    assert sentinel.read_bytes() == b"preserved after fresh review"
    shutil.copyfile(sentinel, d.root / "preserved-after-review")
    assert sentinel.read_bytes() == (d.root / "preserved-after-review").read_bytes()
    sentinel.unlink()
    acknowledgement = d.cli("reconcile-root-removal", "source", remote=False, flags=["--plan", review_path, "--approve", review["Digest"]])
    assert acknowledgement["phase"] == "root-absent" and not root.exists()
    assert d.record("source")["Workspace"]["Phase"] == "reaping"
    assert removal.read_bytes() == removal_bytes and ledger_path.read_bytes() == ledger_bytes
    # Same exact original retirement approval completes after SQL failure; it
    # must never rewrite/reconstruct the retired allocation or inherit target's.
    reap(d, path, plan)
    assert d.record("source")["Workspace"]["Phase"] == "reaped" and ledger_path.read_bytes() == ledger_bytes
    reap(d, path, plan)
    assert ledger_path.read_bytes() == ledger_bytes
    unchanged(d, evidence)
    # Terminal replay still checks reappearance, including nonempty bytes.
    root_return(d, "reap", ["--plan", path, "--approve", plan["Digest"]])
    assert ledger_path.read_bytes() == ledger_bytes
    assert d.record("target")["Workspace"]["Phase"] == "ready" and d.record("target")["ProjectID"] != source["ProjectID"]
    for member in transfer["Members"]:
        retained = member.get("Missing")
        if retained:
            assert Path(retained["Admin"]).is_dir() and Path(retained["Evidence"]).is_file()
    # Check all retained objects after approved checkout destruction, then apply
    # destructive maintenance ONLY to these disposable mirrors. Strict replay
    # must refuse its changed reflog rather than silently weakening evidence.
    for name, objects in (("a", [a_staged]), ("b", [b_staged, reflog])):
        mirror = next(m["Mirror"] for m in source["Workspace"]["Members"] if m["Name"] == name)
        roots = set(d.git(mirror, "for-each-ref", "--format=%(objectname)", "refs/superbench/quarantine/").splitlines())
        assert set(objects).issubset(roots), "explicit quarantine refs missing"
        for oid in objects:
            assert d.git(mirror, "cat-file", "-t", oid) in ("blob", "commit")
        d.git(mirror, "reflog", "expire", "--expire=now", "--all")
        d.git(mirror, "gc", "--prune=now")
        for oid in objects:
            assert d.git(mirror, "cat-file", "-t", oid) in ("blob", "commit")
    reap(d, path, plan, expected=1)
    unchanged(d, evidence)
    print("PASS: " + ("pre-freeze returned retained root" if returned_before_freeze else "whole-root absence" if whole else "mixed dirty/index-only evidence") + ": frozen explicit deletion, target-advanced refs, quota tombstone crash retry, returned-root fresh CLI review, terminal refusal, history/receipts and quarantined objects survive GC", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("bench", "benchd", "quota-helper", "trial", "cgroup-root", "scenario"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--previous-cli", default="")
    parser.add_argument("--corpus", default=str(Path(__file__).resolve().parents[1] / "internal/protocolv2/testdata/workspace-lifecycle.json"))
    args = parser.parse_args()
    assert args.scenario in ("mixed", "whole", "returned") and os.getuid() == os.geteuid() == 0 and not os.environ.get("SUDO_UID")
    assert args.trial.startswith("/var/tmp/superbench-workspace-missing-archive.") and args.cgroup_root.startswith("/sys/fs/cgroup/superbench-workspace-missing-archive-")
    d = missing.Drill(args)
    try:
        archive_reap(d, args.scenario != "mixed", args.scenario == "returned")
        print("UNCOVERED: deterministic process crash precisely after initial removal intent and before first unlink remains native fault-injection coverage; runtime uses actual post-unlink return and final SQL failure", flush=True)
        print("UNCOVERED: cross-host fencing, consumer pilot, whole Phase 1 and production rollout", flush=True)
    finally:
        destination = os.environ.get("SUPERBENCH_ARCHIVE_EVIDENCE")
        if destination:
            output = Path(destination); output.mkdir(parents=True, exist_ok=True)
            for pattern in ("reviewed-*.json", "whole-transfer.json"):
                for path in d.root.glob(pattern):
                    shutil.copyfile(path, output / path.name)
            if (d.root / "quota-ledger/assignments.json").exists():
                shutil.copyfile(d.root / "quota-ledger/assignments.json", output / "final-ledger.json")
            for path in d.state.glob("aggregate-*.json"):
                if path.name.startswith(("aggregate-root-removal-", "aggregate-root-disposition-", "aggregate-freeze-", "aggregate-reap-")):
                    shutil.copyfile(path, output / path.name)
            if (d.state / "bench.db").exists():
                result = {}
                for key in ("source", "target"):
                    try:
                        row = d.record(key)
                        if row.get("Lease"):
                            row["Lease"].pop("Token", None); row["Lease"].pop("Continuation", None)
                        result[key] = row
                    except AssertionError:
                        pass
                (output / "final-records-redacted.json").write_text(json.dumps(result, indent=2))
        d.stop_daemon(crash=True); d.log.close()


if __name__ == "__main__":
    main()
