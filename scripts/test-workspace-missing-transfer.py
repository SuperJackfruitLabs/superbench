#!/usr/bin/env python3
"""Opt-in same-host missing-checkout transfer in an owned XFS/cgroup fixture.

This destroys only member B of this disposable fixture after independently
fencing its writer. It never treats lost files or unknown history as successful
work, never installs services, and makes no cross-host or source-reap claim.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shlex
import shutil
import sqlite3
import subprocess

spec = importlib.util.spec_from_file_location("workspace_api_fixture", Path(__file__).with_name("test-workspace-api.py"))
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class Drill(api.Drill):
    def configure(self):
        members = super().configure()
        self.reappear = self.root / "reappear-during-target-hook"
        self.returned = self.root_for("source") / "b"
        # The hook is sealed into the source checkpoint. Its external gate stays
        # absent for source initialization/acquisition, then tests target guards.
        seed = self.root / "seed-a"
        once = "mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log"
        each = "printf 'each:%s\\n' \"$BENCH_SETUP_OFFLINE\" >> .bench/output/each.log; "
        each += "if test -e " + shlex.quote(str(self.reappear)) + "; then mkdir -p " + shlex.quote(str(self.returned))
        each += "; printf 'returned source from A hook\\n' > " + shlex.quote(str(self.returned / "sentinel")) + "; fi"
        (seed / ".bench/config.toml").write_text('[quota]\nsize="32MiB"\n[setup]\nonce=' + json.dumps(once) + '\neach=' + json.dumps(each) + '\n')
        self.git(seed, "add", ".bench/config.toml")
        self.git(seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "owned missing-source reappearance gate")
        self.git(seed, "push", "origin", "main")
        return members

    def planfile(self, name, plan):
        path = self.root / (name + ".json")
        path.write_text(json.dumps(plan, ensure_ascii=False, separators=(",", ":")) + "\n")
        path.chmod(0o600)
        return path

    def local_words(self, operation, key, flags=()):
        return [self.args.bench, "workspace", operation, "--state", self.state, "--data", self.data, "--cgroup-root", self.cg, *flags, key]

    def refuse(self, operation, key, flags=(), allow_root=False):
        result = self.run(self.local_words(operation, key, flags), expected=1)
        assert all(token not in result.stdout for token in self.tokens)
        if result.stdout:
            value = json.loads(result.stdout)
            assert "grant_token" not in value
            if allow_root:
                assert value.get("root_path") in (None, str(self.root_for("target")))
            else:
                assert "root_path" not in value
        return result

    def absent_target(self):
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            assert db.execute("SELECT count(*) FROM benches WHERE key='target'").fetchone()[0] == 0
        assert "bench:target" not in self.ledger()
        assert not self.root_for("target").exists()

    def ledger(self):
        return json.loads((self.root / "quota-ledger/assignments.json").read_text())["Assignments"]

    def source_evidence(self):
        identity = self.record("source")["Workspace"]["ID"]
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            history = db.execute("SELECT id,record FROM run_history ORDER BY id").fetchall()
            history = [(key, raw) for key, raw in history if json.loads(raw)["Key"] == "source"]
            once = db.execute("SELECT bench,digest,run_id,completed FROM setup_receipts WHERE bench LIKE ? ORDER BY bench", ("workspace:" + identity + ":%",)).fetchall()
        logs = {key: (self.state / ("run-" + key + ".log")).read_bytes() for key, _ in history}
        with sqlite3.connect(f"file:{self.state / 'service.db'}?mode=ro", uri=True) as db:
            receipts = db.execute("SELECT owner,id,digest,state,body,status FROM receipts ORDER BY owner,id").fetchall()
        if not hasattr(self, "protected_receipts"):
            self.protected_receipts = {(row[0], row[1]) for row in receipts}
        receipts = [row for row in receipts if (row[0], row[1]) in self.protected_receipts]
        return history, once, logs, receipts

    def old_cli_refusal(self):
        if not self.args.previous_cli:
            print("UNCOVERED schema21-cli-refusal: no previous binary supplied", flush=True)
            return
        before = self.record("source")
        result = subprocess.run([self.args.previous_cli, "status", "--state", str(self.state), "--data", str(self.data)], capture_output=True, text=True, timeout=20)
        assert result.returncode != 0 and "unsupported state schema 22 (expected 21)" in result.stderr
        assert self.record("source") == before
        print("PASS: schema21 binary refuses schema22 without changing source authority", flush=True)


def bytes_snapshot(root):
    return {str(p.relative_to(root)): (p.stat().st_size, hashlib.sha256(p.read_bytes()).hexdigest())
            for p in sorted(root.rglob("*")) if p.is_file()}


def admin_for(member):
    text = (Path(member["Path"]) / ".git").read_text()
    assert text.startswith("gitdir: ")
    return Path(text[len("gitdir: "):].strip()).resolve()


def fence_unknown_writer(d):
    held, _ = d.request(d.payload("lease", key="source", holder="source-writer"))
    token = d.grant(held)
    pidfile, group, ready, release, late = [d.root / ("missing-writer-" + name) for name in ("pid", "cgroup", "ready", "release", "late")]
    child = 'echo $$ > "$1"; cat /proc/self/cgroup > "$2"; printf ready > "$3"; while ! test -e "$4"; do sleep .02; done; printf forbidden > "$5"'
    command = 'setsid /bin/sh -c "$1" child "$2" "$3" "$4" "$5" "$6" </dev/null >/dev/null 2>&1 & wait'
    request = d.payload("run", key="source", holder="source-writer", lease_token=token, checkpoint=False,
                        arguments=["/bin/sh", "-c", command, "payload", child, str(pidfile), str(group), str(ready), str(release), str(late)])
    d.abandon(request)
    api.wait_for(ready.exists, "source detached writer")
    pid = int(pidfile.read_text())
    execution = d.record("source")["Lease"]["Execution"]["ID"]
    assert "/" + d.cg.name + "/" + execution in group.read_text() and not api.stopped(pid)
    d.stop_daemon(crash=True)
    assert not api.stopped(pid), "daemon death did not leave the controlled writer alive"
    d.start()
    assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt"
    admin, _ = d.request(d.payload("stop_admin", key="source"))
    assert d.grant(admin) == token
    api.wait_for(lambda: api.stopped(pid), "explicit source descendant fence")
    assert not (d.cg / execution).exists()
    stopped = d.record("source")
    assert stopped["Lease"].get("StopRequested") and not stopped["Lease"].get("Continuation")
    release.write_text("only after verified fencing")
    assert not late.exists()
    plan = d.cli("inspect-history", "source", remote=False)
    path = d.planfile("source-unknown-history", plan)
    observed = d.cli("reconcile-history", "source", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
    assert observed["ID"] == execution and observed["ExitCode"] is None and observed["Interruption"]["LogState"] == "present"
    assert observed["ReleasedAt"].startswith("0001-") and not observed.get("Checkpoint")
    assert d.record("source") == stopped, "history review released or replaced the stopped owner"
    print("PASS: daemon SIGKILL leaves setsid writer alive; explicit admin fence empties scope and reviewed history remains unknown, held and uncheckpointed", flush=True)
    return request, token, execution


def lose_owned_checkout(d):
    source = d.record("source")
    a, b = source["Workspace"]["Members"]
    (Path(a["Path"]) / ".bench/output/preserved-private").write_bytes(b"source A ignored bytes\x00\xff")
    (Path(a["Path"]) / "source.txt").write_text("source A unpublished staged bytes\n")
    d.git(a["Path"], "add", "source.txt")
    a_admin, b_admin = admin_for(a), admin_for(b)
    a_files, a_index = bytes_snapshot(Path(a["Path"])), (a_admin / "index").read_bytes()
    (Path(b["Path"]) / "source.txt").write_text("source B lost staged bytes\n")
    d.git(b["Path"], "add", "source.txt")
    staged = d.git(b["Path"], "rev-parse", ":source.txt")
    commit = source["Workspace"]["Checkpoint"]["Members"][1]["Commit"]
    tree = d.git(b["Mirror"], "rev-parse", commit + "^{tree}")
    reflog = d.git(b["Mirror"], "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-p", commit, "-m", "unpublished reflog-only B")
    ref = "refs/heads/" + b["Branch"]
    for new, old in ((reflog, commit), (commit, reflog)):
        d.git(b["Path"], "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "update-ref", "-m", "owned pre-loss evidence", ref, new, old)
    b_index = (b_admin / "index").read_bytes()
    assert Path(b["Path"]) == d.root_for("source") / "b" and Path(b["Path"]).is_relative_to(d.data)
    shutil.rmtree(b["Path"])  # Deliberate loss of this fixture's B only, after fencing.
    assert not Path(b["Path"]).exists() and (b_admin / "index").read_bytes() == b_index
    return source, a_files, a_index, b_index, staged, reflog


def assert_source(d, source, files, index, evidence):
    current = d.record("source")
    assert current.get("Lease") is None and current["Workspace"]["Transfer"]["Recovery"]["Mode"] == "missing"
    assert current["Workspace"]["Checkpoint"] == source["Workspace"]["Checkpoint"]
    assert current["ProjectID"] == source["ProjectID"] and current["QuotaBytes"] == source["QuotaBytes"]
    allocation = d.ledger()["bench:source"]
    assert allocation["ProjectID"] == source["ProjectID"] and allocation["Bytes"] == source["QuotaBytes"] and allocation["Inodes"] == source["QuotaInodes"]
    a = source["Workspace"]["Members"][0]
    assert bytes_snapshot(Path(a["Path"])) == files and (admin_for(a) / "index").read_bytes() == index
    assert d.source_evidence() == evidence, "source history, logs, setup receipts or HTTP receipts changed"


def blocked_source(d, create, raw, uncertain):
    before = d.record("source")
    for operation, fields in (("lease", {"holder": "forbidden"}), ("setup", {"holder": "forbidden"}),
                              ("create", {"repositories": create["repositories"], "checkpoint_repository": "a", "initialize": True}),
                              ("run", {"holder": "forbidden", "arguments": ["/usr/bin/touch", str(d.root / "forbidden-source-launch")]})):
        failed, _ = d.request(d.payload(operation, key="source", **fields), 409)
        assert "result" not in failed and d.record("source") == before
    assert d.request(create)[1] == raw
    assert d.request(uncertain, 409)[0]["error"]["code"] == "operation_in_doubt"
    legacy = d.run(d.local("create-cold", "--repo", d.remotes["a"], "source"), expected=1)
    assert all(token not in legacy.stdout for token in d.tokens)
    assert d.record("source") == before and not (d.root / "forbidden-source-launch").exists()


def transfer_and_repair(d, source, a_files, a_index, b_index, staged, reflog, create, raw, uncertain, old_token):
    evidence = d.source_evidence()
    plan = d.cli("inspect-missing-transfer", "source", remote=False, flags=["--target", "target", "--transfer-id", "missing-original-refs", "--quota-bytes", str(32 << 20)])
    assert plan["Version"] == "superbench.workspace-missing-transfer.v1" and plan["SourceID"] == source["Workspace"]["ID"]
    assert plan["TargetID"] != plan["SourceID"]
    assert plan["Checkpoint"] == source["Workspace"]["Checkpoint"] and plan["Recovery"]["Holder"] == "source-writer"
    assert plan["Members"][0].get("Present") and plan["Members"][1].get("Missing")
    missing = plan["Members"][1]["Missing"]
    assert {staged, reflog}.issubset({o["OID"] for o in missing["Objects"]})
    assert all(token not in json.dumps(plan) for token in d.tokens)
    path = d.planfile("reviewed-missing-transfer", plan)
    assert d.record("source")["Lease"]["Token"] == old_token
    started = d.cli("begin-missing-transfer", "source", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
    assert started["phase"] == "started" and "grant_token" not in started and "root_path" not in started
    assert_source(d, source, a_files, a_index, evidence)
    d.old_cli_refusal()
    a_admin = Path(plan["Members"][0]["Present"]["Admin"])
    b_admin = Path(missing["Admin"])
    # Whole-vector preflight: stale B must not detach A or allocate target.
    (b_admin / "index").write_bytes(b_index + b"owned index fault")
    d.refuse("continue-missing-transfer", "source", ["--transfer-id", plan["ID"]])
    assert (a_admin / "HEAD").read_text() == "ref: refs/heads/" + source["Workspace"]["Members"][0]["Branch"] + "\n"
    d.absent_target()
    (b_admin / "index").write_bytes(b_index)
    # A returned directory is preserved and blocks transfer before any effects.
    d.returned.mkdir(); sentinel = d.returned / "sentinel"; sentinel.write_text("independent returned source\n")
    returned = sentinel.read_bytes()
    d.refuse("continue-missing-transfer", "source", ["--transfer-id", plan["ID"]])
    assert sentinel.read_bytes() == returned and not (d.root / "forbidden-source-launch").exists()
    assert (a_admin / "HEAD").read_text().startswith("ref: ")
    d.absent_target()
    d.returned.rename(d.root / "preserved-returned-before-stage")
    # Now A's target hook recreates the missing source while preparation runs.
    d.reappear.write_text("only the owned target hook returns B")
    d.refuse("continue-missing-transfer", "source", ["--transfer-id", plan["ID"]])
    target = d.record("target")
    d.roots["target"] = Path(target["Path"])
    assert target["Workspace"]["ID"] == plan["TargetID"] and target["ProjectID"] != source["ProjectID"]
    assert target["Lease"].get("StopRequested") and not target["Lease"].get("Continuation")
    assert target["Workspace"]["Phase"] != "ready" and not target["Workspace"].get("Checkpoint")
    assert d.events("target")["a"] == {"once": 1, "each": 1} and d.events("target")["b"] == {"once": 0, "each": 0}
    assert all(not (d.cg / run["ID"]).exists() for run in d.history("target"))
    sentinel = d.returned / "sentinel"; returned = sentinel.read_bytes()
    assert returned == b"returned source from A hook\n"
    d.refuse("continue-missing-transfer", "source", ["--transfer-id", plan["ID"]])
    assert sentinel.read_bytes() == returned and d.events("target")["b"] == {"once": 0, "each": 0}
    d.returned.rename(d.root / "preserved-returned-from-target-hook")
    d.reappear.unlink()
    # Clearing the fixture fault cannot revive a revoked private continuation.
    before = d.events("target")
    d.refuse("continue-missing-transfer", "source", ["--transfer-id", plan["ID"]], allow_root=True)
    assert d.events("target") == before
    repair = d.cli("inspect-initialization", "target", remote=False)
    assert repair["Members"][0]["Action"] in ("preserve", "salvage") and repair["Members"][1]["Action"] == "prepare"
    repair_path = d.planfile("reviewed-target-repair", repair)
    claimed = d.cli("claim-initialization", "target", remote=False, flags=["--plan", repair_path, "--approve", repair["Digest"], "--repair-id", "missing-target-repair"])
    token = claimed["grant_token"]; d.tokens.append(token)
    assert token not in (old_token, target["Lease"]["Token"]) and d.record("target")["Lease"]["Token"] == token
    assert claimed["root_path"] == target["Path"]
    done = d.cli("continue-initialization", "target", remote=False, flags=["--repair-id", "missing-target-repair", "--token", token])
    assert done["phase"] == "ready" and "grant_token" not in done
    complete = d.cli("continue-missing-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]])
    assert complete["phase"] == "completed" and "grant_token" not in complete
    assert complete["root_path"] == target["Path"]
    target = d.record("target")
    assert target.get("Lease") is None and target["Workspace"]["Phase"] == "ready"
    assert target["Workspace"]["Checkpoint"]["Members"] == plan["Checkpoint"]["Members"] and not target["Workspace"]["Checkpoint"]["Previous"]
    assert [m["Branch"] for m in target["Workspace"]["Members"]] == [m["Branch"] for m in source["Workspace"]["Members"]]
    assert target["QuotaBytes"] == 32 << 20 and d.ledger()["bench:target"]["ProjectID"] == target["ProjectID"]
    assert d.events("target") == {n: {"once": 1, "each": 1} for n in ("a", "b")}
    assert_source(d, source, a_files, a_index, evidence)
    blocked_source(d, create, raw, uncertain)
    assert_source(d, source, a_files, a_index, evidence)
    status, _ = d.request(d.payload("status", key="target")); d.sealed({"result": {"workspace": status["result"]["benches"][0]}})
    assert d.cli("continue-missing-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]]) == complete
    # Destructive maintenance is restricted to this disposable mirror, after
    # completion. Explicit quarantine refs must retain unpublished B objects.
    mirror = source["Workspace"]["Members"][1]["Mirror"]
    prefix = "refs/superbench/quarantine/" + plan["ID"] + "/" + b_admin.name + "/"
    refs = d.git(mirror, "for-each-ref", "--format=%(objectname)", prefix).splitlines()
    assert {staged, reflog}.issubset(set(refs))
    evidence_bytes = Path(missing["Evidence"]).read_bytes()
    d.git(mirror, "-c", "core.hooksPath=/dev/null", "reflog", "expire", "--expire=now", "--all")
    d.git(mirror, "-c", "core.hooksPath=/dev/null", "gc", "--prune=now")
    assert d.git(mirror, "cat-file", "-t", staged) == "blob" and d.git(mirror, "cat-file", "-t", reflog) == "commit"
    assert (b_admin / "index").read_bytes() == b_index and Path(missing["Evidence"]).read_bytes() == evidence_bytes
    assert_source(d, source, a_files, a_index, evidence)
    print("PASS: mixed present/missing whole preflight preserves A; reviewed retirement protects unknown source history; source reappearance fences target before B; explicit repair reaches fresh sealed readiness on original refs; quarantine survives owned GC", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("bench", "benchd", "quota-helper", "trial", "cgroup-root"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--previous-cli", default=os.environ.get("SUPERBENCH_MISSING_TRANSFER_PREVIOUS_CLI", ""))
    parser.add_argument("--corpus", default=str(Path(__file__).resolve().parents[1] / "internal/protocolv2/testdata/workspace-lifecycle.json"))
    args = parser.parse_args()
    root, cg = Path(args.trial).resolve(), Path(args.cgroup_root).resolve()
    assert os.getuid() == os.geteuid() == 0 and not os.environ.get("SUDO_UID")
    assert str(root).startswith("/var/tmp/superbench-workspace-missing-transfer.") and str(cg).startswith("/sys/fs/cgroup/superbench-workspace-missing-transfer-")
    assert (root / "data").is_mount() and (cg / "cgroup.kill").exists()
    assert any(" " + str(root / "data") + " " in line and " - xfs " in line and "prjquota" in line for line in Path("/proc/self/mountinfo").read_text().splitlines())
    d = Drill(args)
    try:
        members = d.configure(); d.start()
        create = d.payload("create", key="source", repositories=members, checkpoint_repository="a", initialize=True)
        made, raw = d.request(create); d.roots["source"] = Path(made["result"]["root_path"]); d.sealed(made)
        uncertain, token, _ = fence_unknown_writer(d)
        source, files, index, b_index, staged, reflog = lose_owned_checkout(d)
        transfer_and_repair(d, source, files, index, b_index, staged, reflog, create, raw, uncertain, token)
        print("UNCOVERED missing-source archive/reap integration: retained-history retirement, quarantine roots/registration cleanup and source allocation release are not exercised or claimed", flush=True)
        print("UNCOVERED cross-host/unreachable-source infrastructure fencing and return quarantine: this is same-host independently fenced recovery only", flush=True)
        print("DONE: managed missing-checkout transfer assertions passed; no whole Phase 1 completion, consumer pilot or production rollout is claimed", flush=True)
    finally:
        d.stop_daemon(crash=True); d.log.close()


if __name__ == "__main__":
    main()
