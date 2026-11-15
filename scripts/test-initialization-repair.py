#!/usr/bin/env python3
"""Opt-in stopped-initialization repair drill in an owned Linux XFS/cgroup fixture.

Imports the frozen public API fixture without changing or running its main drill.
Only supplied binaries, disposable remotes/state, and this fixture's processes run.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shlex
import subprocess

spec = importlib.util.spec_from_file_location("workspace_api_fixture", Path(__file__).with_name("test-workspace-api.py"))
api = importlib.util.module_from_spec(spec)
spec.loader.exec_module(api)


class Drill(api.Drill):
    def configure(self):
        members = super().configure()
        self.block, self.ready, self.pid, self.group = [self.root / name for name in ("block-initialization-b", "initialization-b-ready", "initialization-b-pid", "initialization-b-cgroup")]
        seed = self.root / "seed-b"
        once = "mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log"
        each = "if test -e " + shlex.quote(str(self.block)) + "; then printf '%s' \"$$\" > " + shlex.quote(str(self.pid))
        each += "; cat /proc/self/cgroup > " + shlex.quote(str(self.group)) + "; printf ready > " + shlex.quote(str(self.ready))
        each += "; while test -e " + shlex.quote(str(self.block)) + "; do sleep .02; done; fi; printf 'each:%s\\n' \"$BENCH_SETUP_OFFLINE\" >> .bench/output/each.log"
        (seed / ".bench/config.toml").write_text('[quota]\nsize="32MiB"\n[setup]\nonce=' + json.dumps(once) + '\neach=' + json.dumps(each) + '\n')
        self.git(seed, "add", ".bench/config.toml")
        self.git(seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "owned initialization interruption gate")
        self.git(seed, "push", "origin", "main")
        self.repositories = self.root / "repositories.json"
        self.repositories.write_text(json.dumps(members)); self.repositories.chmod(0o600)
        return members

    def planfile(self, name, plan):
        path = self.root / (name + ".json")
        # Generated approval readers intentionally require canonical fields/order.
        path.write_text(json.dumps(plan, ensure_ascii=False, separators=(",", ":")) + "\n")
        path.chmod(0o600)
        return path

    def local_words(self, operation, key, flags=()):
        return [self.args.bench, "workspace", operation, "--state", self.state, "--data", self.data, "--cgroup-root", self.cg, *flags, key]

    def refuse(self, operation, key, flags=()):
        result = self.run(self.local_words(operation, key, flags), expected=1)
        # Errors may carry a closed local DTO, but must not leak a new owner.
        assert all(token not in result.stdout for token in self.tokens)
        return result

    def claim(self, key, plan, identity, old_token):
        path = self.planfile(identity, plan)
        result = self.cli("claim-initialization", key, remote=False, flags=["--plan", path, "--approve", plan["Digest"], "--repair-id", identity])
        token = result["grant_token"]
        current = self.record(key)
        assert result["version"] == "superbench.initialization-repair.v1" and token != old_token
        assert current["Lease"]["Token"] == token and current["Lease"]["Holder"] == "initialization"
        # Core lease JSON omits false stop flags and empty continuations.
        assert current["Lease"].get("StopRequested", False) is False and current["Lease"].get("Continuation")
        assert current["Workspace"]["InitializationRepair"]["ID"] == identity
        assert result["root_path"] == current["Path"] and not current["Lease"].get("Execution")
        self.tokens.append(token); self.roots[key] = Path(result["root_path"])
        return token

    def continue_ready(self, key, identity, token):
        result = self.cli("continue-initialization", key, remote=False, flags=["--repair-id", identity, "--token", token])
        assert result["phase"] == "ready" and "grant_token" not in result
        status, _ = self.request(self.payload("status", key=key))
        workspace = status["result"]["benches"][0]
        self.sealed({"result": {"workspace": workspace}})
        return self.record(key)

    def old_cli_refusal(self):
        if not self.args.previous_cli:
            print("UNCOVERED schema19-cli-refusal: no previous binary supplied", flush=True)
            return
        before = self.record("repair")
        result = subprocess.run([self.args.previous_cli, "status", "--state", str(self.state), "--data", str(self.data)], capture_output=True, text=True, timeout=20)
        assert result.returncode != 0 and "unsupported state schema 21 (expected 19)" in result.stderr
        assert self.record("repair") == before
        print("PASS: schema19 CLI refuses schema21 without changing stopped initialization state", flush=True)


def uncertain_initialization(d, members):
    d.block.write_text("only this owned initialization pauses")
    request = d.payload("create", key="repair", repositories=members, checkpoint_repository="a", initialize=True)
    d.abandon(request)
    api.wait_for(d.ready.exists, "member B once completed before interruption")
    before = d.record("repair")
    d.roots["repair"] = Path(before["Path"])
    first = before["Workspace"]["Preparation"][0]
    assert first["Name"] == "a" and first["RunID"]
    old_execution = before["Lease"]["Execution"]["ID"]
    pid = int(d.pid.read_text())
    assert "/" + d.cg.name + "/" + old_execution in d.group.read_text() and not api.stopped(pid)
    assert d.events("repair") == {"a": {"once": 1, "each": 1}, "b": {"once": 1, "each": 0}}
    preserved = {p.name: p.read_bytes() for p in (d.root_for("repair") / "a/.bench/output").iterdir()}
    user_files = {name: (d.root_for("repair") / name / "source.txt").read_bytes() for name in ("a", "b")}
    d.stop_daemon(crash=True)
    assert not api.stopped(pid)
    d.start()
    assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt"
    stopped, _ = d.request(d.payload("stop_admin", key="repair")); old_token = d.grant(stopped)
    api.wait_for(lambda: api.stopped(pid), "stopped initialization scope fence")
    assert not (d.cg / old_execution).exists()
    d.block.unlink()  # Only the fixture's external gate; no checkout cleanup.
    d.old_cli_refusal()
    d.refuse("inspect-initialization", "repair", ["--rerun-once", "b"])
    history_plan = d.cli("inspect-history", "repair", remote=False)
    history_file = d.planfile("reviewed-interrupted-history", history_plan)
    observed = d.cli("reconcile-history", "repair", remote=False, flags=["--plan", history_file, "--approve", history_plan["Digest"]])
    assert observed["Interruption"]["LogState"] == "present" and observed["ExitCode"] is None
    assert observed["ReleasedAt"].startswith("0001-")
    d.refuse("inspect-initialization", "repair")
    plan = d.cli("inspect-initialization", "repair", remote=False, flags=["--rerun-once", "b"])
    assert [(m["Name"], m["Action"]) for m in plan["Members"]] == [("a", "preserve"), ("b", "rerun-once")]
    assert all(token not in json.dumps(plan) for token in d.tokens)
    token = d.claim("repair", plan, "repair-first", old_token)
    claimed = d.record("repair")
    assert claimed["Workspace"]["Preparation"][0] == first
    assert old_execution in claimed["Workspace"]["InitializationRepair"]["RetiredRuns"]
    assert claimed["Workspace"]["InitializationRepair"]["UncertainOnce"] == ["b"]
    assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt" and d.record("repair") == claimed
    refusal, _ = d.request(d.payload("setup", key="repair", holder="initialization", lease_token=token), 409)
    assert "result" not in refusal and d.record("repair") == claimed
    d.refuse("create", "repair", ["--repositories", d.repositories, "--checkpoint-repository", "a", "--request-id", request["request_id"]])
    legacy = d.run(d.local("create-cold", "--repo", d.remotes["a"], "repair"), expected=1)
    assert token not in legacy.stdout and d.record("repair") == claimed
    # Claim then stop before any new stage must not erase uncertain-once review.
    d.request(d.payload("stop", key="repair", lease_token=token))
    d.refuse("inspect-initialization", "repair")
    again = d.cli("inspect-initialization", "repair", remote=False, flags=["--rerun-once", "b"])
    assert again["PreviousID"] == "repair-first" and again["Members"][1]["Action"] == "rerun-once"
    token = d.claim("repair", again, "repair-second", token)
    completed = d.continue_ready("repair", "repair-second", token)
    assert completed["Workspace"]["ID"] == before["Workspace"]["ID"] and completed["ProjectID"] == before["ProjectID"]
    assert completed["Workspace"]["Preparation"][0] == first
    assert d.events("repair") == {"a": {"once": 1, "each": 1}, "b": {"once": 2, "each": 1}}
    assert {p.name: p.read_bytes() for p in (d.root_for("repair") / "a/.bench/output").iterdir()} == preserved
    assert {name: (d.root_for("repair") / name / "source.txt").read_bytes() for name in ("a", "b")} == user_files
    unknown = next(r for r in d.history("repair") if r["ID"] == old_execution)
    assert unknown["ExitCode"] is None and unknown["Interruption"] and not unknown["ReleasedAt"].startswith("0001-")
    assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt" and d.record("repair") == completed
    replay = d.cli("continue-initialization", "repair", remote=False, flags=["--repair-id", "repair-second", "--token", token])
    assert replay["phase"] == "ready" and "grant_token" not in replay and d.record("repair") == completed
    print("PASS: stopped partial initialization requires reviewed unknown history and explicit once approval; fresh claims preserve A and persistent uncertainty, then continue reaches sealed ready without old receipt revival", flush=True)


def pending_initialization(d, members):
    key = "pending-init"
    hook = d.remotes["b"] / "hooks/pre-receive"
    hook.write_text('#!/bin/sh\nwhile read old new ref; do if test "$ref" = refs/heads/bench/pending-init; then exit 1; fi; done\n'); hook.chmod(0o700)
    try:
        request = d.payload("create", key=key, repositories=members, checkpoint_repository="a", initialize=True)
        failed, raw = d.request(request, 422)
        old_token = d.grant(failed)
        pending = d.record(key)["Workspace"]["Pending"]
        before_events = d.events(key)
        d.request(d.payload("stop", key=key, lease_token=old_token))
        plan = d.cli("inspect-initialization", key, remote=False)
        assert plan["Pending"] == pending and all(m["Action"] == "preserve" for m in plan["Members"])
        token = d.claim(key, plan, "pending-repair", old_token)
        claimed = d.record(key)
        assert d.request(request, 422)[1] == raw and d.record(key) == claimed and token != old_token
        hook.unlink()
        completed = d.continue_ready(key, "pending-repair", token)
        cp = completed["Workspace"]["Checkpoint"]
        assert cp["GenerationID"] == pending["GenerationID"] and cp["Members"] == pending["Members"]
        assert d.events(key) == before_events
        assert d.request(request, 422)[1] == raw and d.record(key) == completed
        print("PASS: prepared pending initialization repairs the exact published generation without rerunning hooks; completed historical create receipt never exports the fresh grant", flush=True)
    finally:
        if hook.exists():
            hook.unlink()


def receiptless_creation(d, members):
    key, identity = "receiptless", "receiptless-create"
    made = d.cli("create", key, remote=False, flags=["--repositories", d.repositories, "--checkpoint-repository", "a", "--request-id", identity])
    d.roots[key] = Path(made["result"]["root_path"]); d.sealed(made)
    before, events = d.record(key), d.events(key)
    # Local creation has never allocated an HTTP receipt. No evidence is deleted
    # to simulate this condition or reopen work.
    request = d.payload("create", key=key, repositories=members, checkpoint_repository="a", initialize=True, request_id=identity)
    refused, _ = d.request(request, 409)
    assert refused["error"]["code"] == "operation_in_doubt" and "result" not in refused
    assert d.record(key) == before and d.events(key) == events
    print("PASS: matching durable creation without an HTTP receipt refuses redispatch without deleting receipt evidence", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("bench", "benchd", "quota-helper", "trial", "cgroup-root"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--previous-cli", default=os.environ.get("SUPERBENCH_INITIALIZATION_PREVIOUS_CLI", ""))
    parser.add_argument("--corpus", default=str(Path(__file__).resolve().parents[1] / "internal/protocolv2/testdata/workspace-lifecycle.json"))
    args = parser.parse_args()
    root, cg = Path(args.trial).resolve(), Path(args.cgroup_root).resolve()
    assert os.getuid() == os.geteuid() == 0 and not os.environ.get("SUDO_UID")
    assert str(root).startswith("/var/tmp/superbench-initialization-repair.") and str(cg).startswith("/sys/fs/cgroup/superbench-initialization-repair-")
    assert (root / "data").is_mount() and (cg / "cgroup.kill").exists()
    assert any(" " + str(root / "data") + " " in line and " - xfs " in line and "prjquota" in line for line in Path("/proc/self/mountinfo").read_text().splitlines())
    drill = Drill(args)
    try:
        members = drill.configure(); drill.start()
        uncertain_initialization(drill, members)
        pending_initialization(drill, members)
        receiptless_creation(drill, members)
        print("DONE: managed initialization repair drill passed; no suite consumer or production rollout is claimed", flush=True)
    finally:
        drill.stop_daemon(crash=True); drill.log.close()


if __name__ == "__main__":
    main()
