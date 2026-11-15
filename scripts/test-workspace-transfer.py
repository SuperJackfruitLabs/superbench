#!/usr/bin/env python3
"""Opt-in original task-ref transfer drill in an owned Linux XFS/cgroup fixture.

Only supplied frozen binaries and this trial's daemon, remotes, mount and cgroups
are used. The public API fixture is imported unchanged; its main is never run.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import subprocess

spec = importlib.util.spec_from_file_location("workspace_api_fixture", Path(__file__).with_name("test-workspace-api.py"))
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class Drill(api.Drill):
    def planfile(self, name, plan):
        path = self.root / (name + ".json")
        path.write_text(json.dumps(plan, ensure_ascii=False, separators=(",", ":")) + "\n")
        path.chmod(0o600)
        return path

    def local_words(self, operation, key, flags=()):
        return [self.args.bench, "workspace", operation, "--state", self.state, "--data", self.data, "--cgroup-root", self.cg, *flags, key]

    def refuse(self, operation, key, flags=()):
        result = self.run(self.local_words(operation, key, flags), expected=1)
        assert all(token not in result.stdout for token in self.tokens)
        return result

    def absent_record(self, key):
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            assert db.execute("SELECT count(*) FROM benches WHERE key=?", (key,)).fetchone()[0] == 0

    def receipts(self, identity):
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            return db.execute("SELECT bench,digest,run_id,completed FROM setup_receipts WHERE bench LIKE ? ORDER BY bench", ("workspace:" + identity + ":%",)).fetchall()

    def old_cli_refusal(self):
        if not self.args.previous_cli:
            print("UNCOVERED schema20-cli-refusal: no prior binary supplied", flush=True)
            return
        before = self.record("source")
        result = subprocess.run([self.args.previous_cli, "status", "--state", str(self.state), "--data", str(self.data)], capture_output=True, text=True, timeout=20)
        assert result.returncode != 0 and "unsupported state schema 22 (expected 20)" in result.stderr
        assert self.record("source") == before
        print("PASS: schema20 binary refuses schema22 without altering source authority", flush=True)


def file_snapshot(root):
    result = {}
    for path in sorted(root.rglob("*")):
        if path.is_file():
            digest = hashlib.sha256()
            with path.open("rb") as stream:
                for chunk in iter(lambda: stream.read(1 << 20), b""):
                    digest.update(chunk)
            result[str(path.relative_to(root))] = (path.stat().st_size, digest.hexdigest())
    return result


def stopped_writer_before_review(d):
    held, _ = d.request(d.payload("lease", key="source", holder="source-writer"))
    token = d.grant(held)
    d.refuse("inspect-transfer", "source", ["--target", "target", "--transfer-id", "original-ref-transfer"])
    pidfile, group, ready, release, late = [d.root / ("transfer-writer-" + name) for name in ("pid", "cgroup", "ready", "release", "late")]
    child = 'echo $$ > "$1"; cat /proc/self/cgroup > "$2"; printf ready > "$3"; while ! test -e "$4"; do sleep .02; done; printf forbidden > "$5"'
    command = 'setsid /bin/sh -c "$1" child "$2" "$3" "$4" "$5" "$6" </dev/null >/dev/null 2>&1 & wait'
    request = d.payload("run", key="source", holder="source-writer", lease_token=token, checkpoint=False,
                        arguments=["/bin/sh", "-c", command, "payload", child, str(pidfile), str(group), str(ready), str(release), str(late)])
    d.abandon(request)
    api.wait_for(ready.exists, "source detached writer")
    pid = int(pidfile.read_text())
    execution = d.record("source")["Lease"]["Execution"]["ID"]
    assert "/" + d.cg.name + "/" + execution in group.read_text() and not api.stopped(pid)
    d.refuse("inspect-transfer", "source", ["--target", "target", "--transfer-id", "original-ref-transfer"])
    d.stop_daemon(crash=True)
    assert not api.stopped(pid), "owned supervisor SIGKILL did not leave the writer alive"
    d.start()
    uncertain, _ = d.request(request, 409)
    assert uncertain["error"]["code"] == "operation_in_doubt"
    d.refuse("inspect-transfer", "source", ["--target", "target", "--transfer-id", "original-ref-transfer"])
    admin, _ = d.request(d.payload("stop_admin", key="source"))
    assert d.grant(admin) == token
    api.wait_for(lambda: api.stopped(pid), "source cgroup descendant fence")
    assert not (d.cg / execution).exists() and not d.record("source")["Lease"].get("Continuation")
    release.write_text("only after successful fencing")
    assert not late.exists()
    plan = d.cli("inspect-history", "source", remote=False)
    assert plan["Execution"]["ID"] == execution
    path = d.planfile("source-interrupted-history", plan)
    observed = d.cli("reconcile-history", "source", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
    assert observed["ExitCode"] is None and observed.get("Interruption", {}).get("LogState") == "present"
    d.reconcile("source", token)
    assert d.record("source").get("Lease") is None
    assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt"
    print("PASS: held/live source refuses review; surviving writer is explicitly fenced, unknown history reviewed and checkpoint released before transfer", flush=True)


def blocked_source(d, original_create, original_raw, source_before):
    before = d.record("source")
    assert before.get("Lease") is None and before["Workspace"]["Transfer"]["Phase"] in ("relinquishing", "transferred")
    d.transfer_unavailable("source", before["Workspace"]["Transfer"]["Phase"], before["Workspace"]["Phase"])
    for operation, fields in (("lease", {"holder": "forbidden"}),
                              ("run", {"holder": "forbidden", "arguments": ["/usr/bin/touch", str(d.root / "forbidden-source-launch")]}),
                              ("setup", {"holder": "forbidden"}),
                              ("create", {"repositories": original_create["repositories"], "checkpoint_repository": "a", "initialize": True})):
        failed, _ = d.request(d.payload(operation, key="source", **fields), 409)
        assert not failed["ok"] and "grant" not in failed.get("result", {})
        assert d.record("source") == before
    cached, raw = d.request(original_create)
    assert raw == original_raw and "grant" not in cached.get("result", {})
    assert d.record("source") == before and not (d.root / "forbidden-source-launch").exists()
    legacy = d.run(d.local("create-cold", "--repo", d.remotes["a"], "source"), expected=1)
    assert all(token not in legacy.stdout for token in d.tokens)
    assert all(before[field] == source_before[field] for field in ("Path", "ProjectID", "QuotaBytes", "QuotaInodes"))
    assert before["Workspace"]["Checkpoint"] == source_before["Workspace"]["Checkpoint"]
    assert before["Workspace"]["Preparation"] == source_before["Workspace"]["Preparation"]
    d.prove("original-ref-transfer-availability", 2)


def transfer(d, members, original_create, original_raw):
    source = d.record("source")
    # Warm private bytes exceed the old 64MiB logical snapshot bound without
    # consuming the 32MiB allocation: only the tail of this sparse file is written.
    warm = d.root_for("source") / "a/.bench/output/warm-artifact"
    with warm.open("wb") as stream:
        stream.truncate(65 << 20)
        stream.seek((65 << 20) - 64)
        stream.write(b"owned warm source artifact")
    files, counts = file_snapshot(d.root_for("source")), d.events("source")
    once = d.receipts(source["Workspace"]["ID"])
    assert len(once) == 2
    for member, commit in zip(source["Workspace"]["Members"], source["Workspace"]["Checkpoint"]["Members"]):
        assert member["Name"] == commit["Name"]
        d.git(member["Mirror"], "update-ref", "refs/heads/reviewed-alternative", commit["Commit"])
    plan = d.cli("inspect-transfer", "source", remote=False, flags=["--target", "target", "--transfer-id", "original-ref-transfer"])
    assert plan["SourceID"] == source["Workspace"]["ID"] and plan["TargetID"] != plan["SourceID"]
    assert plan["Checkpoint"] == source["Workspace"]["Checkpoint"] and len(plan["Members"]) == 2
    assert plan["Members"][0]["Worktree"]["TreeBytes"] >= 65 << 20
    assert all(token not in json.dumps(plan) for token in d.tokens)
    path = d.planfile("reviewed-original-ref-transfer", plan)
    started = d.cli("begin-transfer", "source", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
    assert started["version"] == "superbench.workspace-transfer.v1" and started["phase"] == "started"
    assert "grant_token" not in started and "root_path" not in started
    d.absent_record("target")
    d.old_cli_refusal()
    blocked_source(d, original_create, original_raw, source)
    # Fault only the second member's symbolic metadata after approval has become
    # durable. No tracked/index/untracked/ignored bytes or journal are changed.
    second = source["Workspace"]["Members"][1]
    d.git(second["Path"], "symbolic-ref", "HEAD", "refs/heads/reviewed-alternative")
    partial = d.cli("continue-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]], expected=1)
    assert partial["phase"] == "started" and "grant_token" not in partial
    assert (Path(plan["Members"][0]["Worktree"]["Admin"]) / "HEAD").read_text() == plan["Checkpoint"]["Members"][0]["Commit"] + "\n"
    assert d.git(second["Path"], "symbolic-ref", "HEAD") == "refs/heads/reviewed-alternative"
    d.absent_record("target")
    assert d.record("source")["Workspace"]["Transfer"]["Phase"] == "relinquishing"
    assert "bench:target" not in json.loads((d.root / "quota-ledger/assignments.json").read_text())["Assignments"]
    assert not (d.data / "benches/target").exists()
    assert file_snapshot(d.root_for("source")) == files and d.receipts(source["Workspace"]["ID"]) == once
    blocked_source(d, original_create, original_raw, source)
    d.git(second["Path"], "symbolic-ref", "HEAD", "refs/heads/" + second["Branch"])
    completed = d.cli("continue-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]])
    assert completed["phase"] == "completed" and completed["target_id"] == plan["TargetID"] and "grant_token" not in completed
    d.roots["target"] = Path(completed["root_path"])
    target = d.record("target")
    assert source["ProjectID"] > 0 and target["ProjectID"] > 0
    assert target["Workspace"]["ID"] != source["Workspace"]["ID"] and target["ProjectID"] != source["ProjectID"]
    assert target.get("Lease") is None and target["Workspace"]["Phase"] == "ready"
    assert target["Workspace"]["Checkpoint"]["Members"] == plan["Checkpoint"]["Members"] and not target["Workspace"]["Checkpoint"]["Previous"]
    assert [m["Branch"] for m in target["Workspace"]["Members"]] == [m["Branch"] for m in source["Workspace"]["Members"]]
    assert d.events("target") == {n: {"once": 1, "each": 1} for n in ("a", "b")}
    assert d.events("source") == counts and file_snapshot(d.root_for("source")) == files and d.receipts(source["Workspace"]["ID"]) == once
    status, _ = d.request(d.payload("status", key="target"))
    d.sealed({"result": {"workspace": status["result"]["benches"][0]}})
    blocked_source(d, original_create, original_raw, source)
    assert d.cli("continue-transfer", "source", remote=False, flags=["--transfer-id", plan["ID"]]) == completed
    print("PASS: partial metadata-only relinquishment retains source bytes/once evidence and permanent blocking; exact retry initializes independent quota/identity on original task refs", flush=True)
    return source, target, plan, files, once


def advance_and_retire_source(d, source, target, transfer_plan, source_files, once, original_create, original_raw):
    source_cp = source["Workspace"]["Checkpoint"]
    coordinator = next(m for m in source["Workspace"]["Members"] if m["Name"] == source["Workspace"]["CheckpointRepository"])
    old_manifest = json.loads(d.git(coordinator["Mirror"], "show", source_cp["Seal"] + ":checkpoint.json"))
    command = 'printf "destination advancement\\n" >> source.txt; git add source.txt; git -c user.name=Fixture -c user.email=fixture@example.invalid commit -m "owned destination advancement"'
    result = d.cli("run", "target", flags=["--holder", "destination-owner", "--repository-name", "a", "--request-id", "advance-original-ref"], command=["/bin/sh", "-c", command])
    d.sealed(result)
    current = d.record("target")
    assert current["Workspace"]["Checkpoint"]["Members"][0]["Commit"] != source_cp["Members"][0]["Commit"]
    assert file_snapshot(d.root_for("source")) == source_files and d.receipts(source["Workspace"]["ID"]) == once
    target_files = file_snapshot(d.root_for("target"))
    refs = {name: d.git(remote, "show-ref") for name, remote in d.remotes.items()}
    for member in old_manifest["members"]:
        assert d.git(member["repository"], "rev-parse", member["pin_ref"]) == member["commit"]
    archived = d.cli("archive", "source", remote=False, flags=["--archive-grace", "0s"])
    assert archived["result"]["workspace"]["phase"] == "archived"
    assert d.root_for("source").stat().st_mode & 0o222 == 0
    # Use the actual owner UID with DAC bypass capabilities dropped and an
    # inherited cwd, so private ancestor permissions cannot cause a false pass.
    write_probe = subprocess.run(["/usr/bin/setpriv", "--bounding-set=-dac_override,-dac_read_search,-fowner", "/usr/bin/touch", "forbidden-write"], cwd=d.root_for("source"), capture_output=True, timeout=10)
    assert write_probe.returncode != 0 and not (d.root_for("source") / "forbidden-write").exists()
    assert file_snapshot(d.root_for("source")) == source_files
    plan = d.cli("plan-reap", "source", remote=False)
    assert plan["Transfer"]["ID"] == transfer_plan["ID"] and len(plan["Registrations"]) == 2
    assert plan["Checkpoint"] == source_cp
    entries = {entry["Path"]: entry for entry in plan["Tree"]["Entries"]}
    assert not entries["a/.git"]["Recoverable"] and not entries["a/.bench/output/warm-artifact"]["Recoverable"]
    path = d.planfile("reviewed-transferred-source-reap", plan)
    flags = ["--plan", path, "--approve", plan["Digest"]]
    retired = d.cli("reap", "source", remote=False, flags=flags)
    assert retired["kind"] == "workspace" and retired["phase"] == "reaped" and not d.root_for("source").exists()
    assert d.cli("reap", "source", remote=False, flags=flags) == retired
    ledger = json.loads((d.root / "quota-ledger/assignments.json").read_text())["Assignments"]
    assert ledger["bench:source"]["ProjectID"] == source["ProjectID"] and ledger["bench:source"]["Bytes"] == ledger["bench:source"]["Inodes"] == 0
    assert ledger["bench:target"]["ProjectID"] == target["ProjectID"] and ledger["bench:target"]["Bytes"] > 0
    assert d.record("target") == current and file_snapshot(d.root_for("target")) == target_files
    assert d.receipts(source["Workspace"]["ID"]) == once
    for name, remote in d.remotes.items():
        assert d.git(remote, "show-ref") == refs[name]
    for i, member in enumerate(target["Workspace"]["Members"]):
        registrations = d.git(member["Mirror"], "worktree", "list", "--porcelain", "-z")
        assert "worktree " + member["Path"] + "\0" in registrations
        assert not Path(transfer_plan["Members"][i]["Worktree"]["Admin"]).exists()
        assert "worktree " + source["Workspace"]["Members"][i]["Path"] + "\0" not in registrations
        assert d.git(member["Path"], "symbolic-ref", "HEAD") == "refs/heads/" + member["Branch"]
    blocked_source(d, original_create, original_raw, source)
    print("PASS: destination advances original task ref under supervision; source immutable seal/pins permit read-only archive and reviewed retirement of only old allocation/registrations", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("bench", "benchd", "quota-helper", "trial", "cgroup-root"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--previous-cli", default=os.environ.get("SUPERBENCH_TRANSFER_PREVIOUS_CLI", ""))
    parser.add_argument("--corpus", default=str(Path(__file__).resolve().parents[1] / "internal/protocolv2/testdata/workspace-lifecycle.json"))
    args = parser.parse_args()
    root, cg = Path(args.trial).resolve(), Path(args.cgroup_root).resolve()
    assert os.getuid() == os.geteuid() == 0 and not os.environ.get("SUDO_UID")
    assert str(root).startswith("/var/tmp/superbench-workspace-transfer.") and str(cg).startswith("/sys/fs/cgroup/superbench-workspace-transfer-")
    assert (root / "data").is_mount() and (cg / "cgroup.kill").exists()
    assert any(" " + str(root / "data") + " " in line and " - xfs " in line and "prjquota" in line for line in Path("/proc/self/mountinfo").read_text().splitlines())
    d = Drill(args)
    try:
        members = d.configure(); d.start()
        request = d.payload("create", key="source", repositories=members, checkpoint_repository="a", initialize=True)
        made, raw = d.request(request)
        d.roots["source"] = Path(made["result"]["root_path"]); d.sealed(made)
        stopped_writer_before_review(d)
        source, target, plan, files, once = transfer(d, members, request, raw)
        advance_and_retire_source(d, source, target, plan, files, once, request, raw)
        print("UNCOVERED original-ref-transfer: lost/unreachable source host fencing, cross-host quarantine/authority, stale simultaneous takeover, injected quota-retirement interruption; this drill verifies cooperative same-host transfer only", flush=True)
        print("DONE: managed same-host original task-ref transfer assertions passed; no consumer pilot or production rollout is claimed", flush=True)
    finally:
        d.stop_daemon(crash=True); d.log.close()


if __name__ == "__main__":
    main()
