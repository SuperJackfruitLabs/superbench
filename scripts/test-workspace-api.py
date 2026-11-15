#!/usr/bin/env python3
"""Opt-in public v2 lifecycle drill inside an owned XFS/cgroup fixture.

The shell wrapper owns mounts and final deletion. This driver starts only the
supplied frozen benchd, operates only its trial state/remotes, and reports exact
lifecycle assertions still requiring separate evidence. It never installs a service.
"""
import argparse
import hashlib
import http.client
import json
import os
from pathlib import Path
import shlex
import socket
import sqlite3
import stat
import subprocess
import time


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("local", timeout=30)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def wait_for(check, label, seconds=20):
    until = time.monotonic() + seconds
    while time.monotonic() < until:
        if check():
            return
        time.sleep(.025)
    raise AssertionError("timeout: " + label)


class Drill:
    def __init__(self, args):
        self.args = args
        self.root = Path(args.trial).resolve()
        self.cg = Path(args.cgroup_root).resolve()
        self.state, self.data = self.root / "state", self.root / "data"
        self.sock = self.root / "api.sock"
        self.daemon = None
        self.log = (self.root / "daemon.log").open("wb")
        self.sequence = 0
        self.tokens = []
        self.roots = {}
        self.remotes = {}
        self.fail = self.root / "fail-second-hook"
        corpus_bytes = Path(args.corpus).read_bytes()
        assert hashlib.sha256(corpus_bytes).hexdigest() == "74524b71743780bd4e8f1d4f646c4193ade8508e32709c32017144212880dfeb", "lifecycle corpus pin changed; review runtime obligations"
        self.corpus = json.loads(corpus_bytes)
        assert self.corpus["version"] == "superbench.workspace-lifecycle-fixtures.v1"
        self.covered = {s["name"]: set() for s in self.corpus["scenarios"]}
        self.obligations = {s["name"]: s["assertions"] for s in self.corpus["scenarios"]}

    def prove(self, scenario, *indices):
        for index in indices:
            assert 0 <= index < len(self.obligations[scenario])
            self.covered[scenario].add(index)

    def run(self, words, expected=0):
        result = subprocess.run(list(map(str, words)), capture_output=True, text=True, timeout=45)
        assert result.returncode == expected, (str(words[0]), result.returncode, result.stderr)
        return result

    def git(self, directory, *words):
        return self.run(["git", "-C", directory, *words]).stdout.strip()

    def local(self, operation, *words):
        return [self.args.bench, operation, "--state", self.state, "--data", self.data,
                "--cgroup-root", self.cg, *words]

    def cli(self, operation, key=None, flags=(), command=(), expected=0, remote=True):
        words = [self.args.bench, "workspace", operation]
        if remote:
            words += ["--socket", self.sock]
        else:
            words += ["--state", self.state, "--data", self.data, "--cgroup-root", self.cg]
        words += list(flags)
        if key is not None:
            words.append(key)
        if command:
            words += ["--", *command]
        output = self.run(words, expected).stdout
        return json.loads(output)

    def payload(self, operation, **fields):
        if operation in {"create", "recover", "lease", "run", "setup"} and "request_id" not in fields:
            self.sequence += 1
            fields["request_id"] = "workspace-api-" + str(self.sequence)
        return {"version": "superbench.v2", "operation": operation, **fields}

    def request(self, payload, expected=200):
        c = UnixHTTP(self.sock)
        try:
            c.request("POST", "/v2/operations", json.dumps(payload), {"Content-Type": "application/json"})
            r = c.getresponse()
            raw = r.read((1 << 20) + 1)
            allowed = (expected,) if isinstance(expected, int) else expected
            assert r.status in allowed, (payload["operation"], r.status, "unexpected HTTP status; private body withheld")
            assert len(raw) <= 1 << 20
            if payload["operation"] in {"create", "recover", "lease", "run", "setup"}:
                assert len(raw) <= 64 << 10
            body = json.loads(raw)
            assert body["version"] == "superbench.v2" and body["operation"] == payload["operation"]
            assert body.get("request_id") == payload.get("request_id")
            assert body["ok"] is (r.status == 200)
            return body, raw
        finally:
            c.close()

    def abandon(self, payload):
        c = UnixHTTP(self.sock)
        c.request("POST", "/v2/operations", json.dumps(payload), {"Content-Type": "application/json"})
        c.close()

    def start(self):
        self.daemon = subprocess.Popen([self.args.benchd, "--state", str(self.state), "--data", str(self.data),
                                       "--socket", str(self.sock), "--cgroup-root", str(self.cg),
                                       "--operator-uids", "0,65533", "--reader-uids", "65534"], stdout=self.log, stderr=self.log)
        def ready():
            assert self.daemon.poll() is None, "trial daemon exited"
            c = UnixHTTP(self.sock)
            try:
                c.request("GET", "/healthz")
                response = c.getresponse()
                response.read()
                return response.status == 200
            except (OSError, http.client.HTTPException):
                return False
            finally:
                c.close()
        wait_for(ready, "trial daemon ready")

    def stop_daemon(self, crash=False):
        if self.daemon is not None and self.daemon.poll() is None:
            self.daemon.kill() if crash else self.daemon.terminate()
            code = self.daemon.wait(timeout=20)
            if not crash:
                assert code == 0
        self.daemon = None

    def record(self, key):
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            row = db.execute("SELECT record FROM benches WHERE key=?", (key,)).fetchone()
        assert row is not None
        return json.loads(row[0])

    def history(self, key):
        with sqlite3.connect(f"file:{self.state / 'bench.db'}?mode=ro", uri=True) as db:
            rows = db.execute("SELECT record FROM run_history").fetchall()
        return [json.loads(raw) for (raw,) in rows if json.loads(raw)["Key"] == key]

    def root_for(self, key):
        return self.roots.get(key, self.data / "benches" / key)

    def events(self, key):
        result = {}
        for name in ("a", "b"):
            result[name] = {}
            for event in ("once", "each"):
                path = self.root_for(key) / name / ".bench/output" / (event + ".log")
                result[name][event] = len(path.read_text().splitlines()) if path.exists() else 0
        return result

    def delta(self, key, before, once=(), each=()):
        after = self.events(key)
        for name in ("a", "b"):
            assert after[name]["once"] == before[name]["once"] + once.count(name), (key, name, before, after)
            assert after[name]["each"] == before[name]["each"] + each.count(name), (key, name, before, after)

    def grant(self, body):
        result = body["result"]
        token = result["grant"]["lease_token"]
        assert len(token) in (32, 48) and result["workspace"].get("lease")
        self.tokens.append(token)
        key = result["workspace"]["key"]
        self.roots[key] = Path(result["root_path"])
        assert self.record(key)["Lease"]["Token"] == token
        return token

    def sealed(self, body):
        w = body["result"]["workspace"]
        assert "lease" not in w and "pending" not in w and len(w["checkpoint"]["members"]) == 2
        record = self.record(w["key"])
        assert record.get("Lease") is None
        cp = w["checkpoint"]
        for member in record["Workspace"]["Members"]:
            # Pin spelling is the core's retained manifest, not guessed from task refs.
            metadata = self.git(member["Mirror"], "show", cp["seal"] + ":checkpoint.json") if member["Name"] == "a" else None
            if metadata is not None:
                manifest = json.loads(metadata)
                assert hashlib.sha256(metadata.encode()).hexdigest() == cp["manifest_sha256"] or hashlib.sha256((metadata + "\n").encode()).hexdigest() == cp["manifest_sha256"]
                self.current_manifest = manifest
            vector = next(v for v in cp["members"] if v["name"] == member["Name"])
            assert self.git(member["Repository"], "rev-parse", "refs/heads/" + member["Branch"]) == vector["commit"]
        for m in self.current_manifest["members"]:
            assert self.git(m["repository"], "rev-parse", m["pin_ref"]) == m["commit"]
        for run in self.history(w["key"]):
            assert run.get("File") and not run["SealedAt"].startswith("0001-") and not run["ReleasedAt"].startswith("0001-")
            assert not (self.cg / run["ID"]).exists()
        return w

    def reconcile(self, key, token):
        complete, _ = self.request(self.payload("checkpoint", key=key, lease_token=token))
        released, _ = self.request(self.payload("release", key=key, lease_token=token))
        assert complete["result"]["workspace"]["checkpoint"] == released["result"]["workspace"]["checkpoint"]
        return self.sealed(released)

    def configure(self):
        policy = {"Root": str(self.data), "Ledger": str(self.root / "quota-ledger"), "OwnerUID": 0, "OwnerGID": 0,
                  "BenchBytes": 64 << 20, "StoreBytes": 32 << 20, "MirrorBytes": 64 << 20, "ReserveBytes": 64 << 20,
                  "BenchInodes": 4096, "StoreInodes": 8192, "MirrorInodes": 8192, "ReserveInodes": 1024}
        path = self.root / "policy.json"
        path.write_text(json.dumps(policy)); path.chmod(0o600)
        self.run(self.local("configure-host", "--quota-helper", self.args.quota_helper, "--quota-policy", path,
                            "--min-data-free", 8 << 20, "--min-state-free", 1 << 20))
        members = []
        for name in ("a", "b"):
            seed, remote = self.root / ("seed-" + name), self.root / ("remote-" + name + ".git")
            self.run(["git", "init", "--bare", remote]); self.run(["git", "init", "-b", "main", seed])
            (seed / ".bench").mkdir()
            once = "mkdir -p .bench/output; printf 'once\\n' >> .bench/output/once.log"
            each = "printf 'each:%s\\n' \"$BENCH_SETUP_OFFLINE\" >> .bench/output/each.log"
            if name == "b":
                each = "if test -e " + shlex.quote(str(self.fail)) + "; then exit 17; fi; " + each
            (seed / ".bench/config.toml").write_text('[quota]\nsize="32MiB"\n[setup]\nonce=' + json.dumps(once) + '\neach=' + json.dumps(each) + '\n')
            (seed / ".gitignore").write_text("/.bench/output/\n")
            (seed / "source.txt").write_text("sealed base " + name + "\n")
            self.git(seed, "add", "."); self.git(seed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "base")
            self.git(seed, "remote", "add", "origin", str(remote)); self.git(seed, "push", "origin", "main"); self.git(remote, "symbolic-ref", "HEAD", "refs/heads/main")
            members.append({"name": name, "repository": str(remote)})
            self.remotes[name] = remote
        return members

    def coverage(self):
        for name, assertions in self.obligations.items():
            missing = [text for index, text in enumerate(assertions) if index not in self.covered[name]]
            if missing:
                print("UNCOVERED " + name + ": " + "; ".join(missing), flush=True)
            else:
                print("COVERED " + name, flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("bench", "benchd", "quota-helper", "trial", "cgroup-root"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--corpus", default=str(Path(__file__).resolve().parents[1] / "internal/protocolv2/testdata/workspace-lifecycle.json"))
    args = parser.parse_args()
    root, cg = Path(args.trial).resolve(), Path(args.cgroup_root).resolve()
    if os.geteuid() != 0 or os.getuid() != 0 or os.environ.get("SUDO_UID") or not str(root).startswith("/var/tmp/superbench-workspace-api.") or not str(cg).startswith("/sys/fs/cgroup/superbench-workspace-api-"):
        raise SystemExit("requires the direct-root owned mount/cgroup wrapper")
    assert root.is_dir() and (root / "data").is_mount() and (cg / "cgroup.kill").exists()
    mountinfo = Path("/proc/self/mountinfo").read_text()
    assert any(" " + str(root / "data") + " " in line and " - xfs " in line and "prjquota" in line for line in mountinfo.splitlines()), "owned XFS project-quota mount required"
    drill = Drill(args)
    try:
        exercise(drill)
        drill.coverage()
        print("DONE: public workspace API drill passed; uncovered obligations above remain separate", flush=True)
    finally:
        drill.stop_daemon(crash=True)
        drill.log.close()


def exercise(d):
    members = d.configure()
    d.start()
    zero = {name: {"once": 0, "each": 0} for name in ("a", "b")}
    create = d.payload("create", key="card", repositories=members, checkpoint_repository="a", initialize=True, quota_bytes=32 << 20)
    d.original_create = create
    created, create_raw = d.request(create)
    d.roots["card"] = Path(created["result"]["root_path"])
    initial = d.sealed(created)
    d.delta("card", zero, once=("a", "b"), each=("a", "b"))
    assert "grant" not in created["result"]
    record = d.record("card"); project = record["ProjectID"]
    assert project and record["QuotaBytes"] == 32 << 20
    for m in record["Workspace"]["Members"]:
        out = d.run(["xfs_io", "-c", "stat", m["Path"]]).stdout
        assert "projid = " + str(project) in out
    d.prove("warm-creation", 2, 3, 4)

    # New-owner preparation runs each exactly once across raw acquisition + prepare.
    before = d.events("card")
    lease_request = d.payload("lease", key="card", holder="first-owner")
    leased, lease_raw = d.request(lease_request); token = d.grant(leased)
    d.delta("card", before, each=("a", "b"))
    refused, _ = d.request(d.payload("lease", key="card", holder="other-owner"), 409)
    assert "result" not in refused
    d.prove("new-owner", 1, 2)
    commit = 'printf "payload\\n" >> source.txt; git add source.txt; git -c user.name=Fixture -c user.email=fixture@example.invalid commit -m payload'
    before = d.events("card")
    reused, _ = d.request(d.payload("run", key="card", holder="first-owner", lease_token=token, repository_name="a", checkpoint=False, arguments=["/bin/sh", "-c", commit]))
    assert "grant" not in reused["result"] and "root_path" not in reused["result"] and reused["result"]["workspace"].get("lease")
    d.delta("card", before)
    d.reconcile("card", token)
    d.prove("execution-retained", 0, 2)

    for selector, checkpoint in (("a", True), (None, None)):
        before = d.events("card")
        fields = dict(key="card", holder="successful", arguments=["/bin/sh", "-c", 'test "$PWD" = "$1"', "payload", str(d.root_for("card") / selector) if selector else str(d.root_for("card"))])
        if selector:
            fields["repository_name"] = selector
        if checkpoint is not None:
            fields["checkpoint"] = checkpoint
        completed, _ = d.request(d.payload("run", **fields)); d.sealed(completed)
        d.delta("card", before, each=("a", "b"))
    d.prove("execution-release", 0, 1)
    before = d.events("card")
    failed, _ = d.request(d.payload("run", key="card", holder="failed-owner", arguments=["/bin/sh", "-c", "exit 7"]), 422)
    token = d.grant(failed)
    assert failed["result"]["exit_code"] == 7 and failed["result"]["execution"]["phase"] == "stopped"
    d.delta("card", before, each=("a", "b")); d.reconcile("card", token)
    d.prove("execution-retained", 1)

    selected_setup(d)
    partial_initialization(d, members, zero)
    pending_publication(d)
    detached_recovery(d, crash=False)
    detached_recovery(d, crash=True)

    before = d.events("card")
    current, _ = d.request(d.payload("lease", key="card", holder="later-owner")); token = d.grant(current)
    state = d.record("card")
    for payload, raw in ((create, create_raw), (lease_request, lease_raw)):
        assert d.request(payload)[1] == raw
    d.stop_daemon(); d.start()
    assert d.request(lease_request)[1] == lease_raw and d.record("card") == state
    d.delta("card", before, each=("a", "b"))
    selected, _ = d.request(d.payload("setup", key="card", holder="later-owner", lease_token=token, repository_name="b"))
    assert selected["result"]["preparation"] == [{"name": "b", "status": "prepared"}]
    d.sealed(selected)
    d.prove("receipt-replay", 1, 2, 3)
    cli_and_recovery(d, members)
    status_and_retention(d, members)


def selected_setup(d):
    before = d.events("card")
    selected, _ = d.request(d.payload("setup", key="card", holder="fresh-selected", repository_name="b", offline=True), (409, 422))
    token = d.grant(selected)
    assert not selected["ok"] and d.record("card").get("Lease")
    d.delta("card", before, each=("b",))
    full, _ = d.request(d.payload("setup", key="card", holder="fresh-selected", lease_token=token, offline=True))
    d.sealed(full); d.delta("card", before, each=("a", "b"))
    before = d.events("card")
    held, _ = d.request(d.payload("lease", key="card", holder="stale-other")); token = d.grant(held)
    d.delta("card", before, each=("a", "b"))
    a = d.root_for("card") / "a"
    (a / "package.json").write_text('{"name":"owned-input-change"}\n')
    d.git(a, "add", "package.json"); d.git(a, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "changed preparation input")
    refused, _ = d.request(d.payload("setup", key="card", holder="stale-other", lease_token=token, repository_name="b"), (409, 422))
    assert not refused["ok"] and "grant" not in refused.get("result", {}) and d.record("card")["Lease"]["Token"] == token
    before = d.events("card")
    repaired, _ = d.request(d.payload("setup", key="card", holder="stale-other", lease_token=token))
    d.delta("card", before, once=("a",), each=("a",)); d.sealed(repaired)
    d.prove("setup-completion", 0, 1)
    print("PASS: fresh selected setup cannot release missing proofs; stale unselected inputs retain the owner until full preparation", flush=True)


def partial_initialization(d, members, zero):
    d.fail.write_text("fail only this owned trial")
    request = d.payload("create", key="partial", repositories=members, checkpoint_repository="a", initialize=True)
    failed, failed_raw = d.request(request, 422)
    token = d.grant(failed)
    partial = d.record("partial")
    assert partial["Workspace"]["Phase"] == "initializing" and partial["Lease"]["Holder"] == "initialization"
    first = partial["Workspace"]["Preparation"][0]
    assert first["Name"] == "a" and first["RunID"]
    first_files = {p.name: p.read_bytes() for p in (d.root_for("partial") / "a/.bench/output").iterdir()}
    d.delta("partial", zero, once=("a", "b"), each=("a",))
    status, status_raw = d.request(d.payload("status", key="partial"))
    assert token.encode() not in status_raw and b"root_path" not in status_raw
    d.fail.unlink()  # Only this driver's explicit failure flag; never checkout work.
    repaired, _ = d.request(d.payload("setup", key="partial", holder="initialization", lease_token=token))
    d.sealed(repaired)
    assert d.record("partial")["Workspace"]["Preparation"][0] == first
    assert {p.name: p.read_bytes() for p in (d.root_for("partial") / "a/.bench/output").iterdir()} == first_files
    d.delta("partial", zero, once=("a", "b", "b"), each=("a", "b"))
    assert d.request(request, 422)[1] == failed_raw and d.record("partial").get("Lease") is None
    d.partial_create = request
    d.prove("partial-initialization", 0, 2)
    print("PASS: later-member initialization failure preserves the first member and receipt; same owned initialization repairs without repeating that member", flush=True)


def pending_publication(d):
    held, _ = d.request(d.payload("lease", key="card", holder="pending-owner")); token = d.grant(held)
    old = held["result"]["workspace"]["checkpoint"]
    expected = []
    for name in ("a", "b"):
        path = d.root_for("card") / name
        with (path / "source.txt").open("a") as f:
            f.write("pending vector " + name + "\n")
        d.git(path, "add", "source.txt"); d.git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "pending vector")
        expected.append({"Name": name, "Commit": d.git(path, "rev-parse", "HEAD")})
    expected_file = d.root / "expected-pending.json"
    expected_file.write_text(json.dumps(expected))
    observed = d.root / "pending-before-first-push"
    verifier = d.root / "verify-pending.py"
    verifier.write_text('''import json,sqlite3,sys
from pathlib import Path
updates=[line.split() for line in sys.stdin]
if any(fields[2]=="refs/heads/bench/card" for fields in updates):
 with sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True) as db:
  record=json.loads(db.execute("SELECT record FROM benches WHERE key='card'").fetchone()[0])
 assert record["Workspace"]["Pending"]["Members"]==json.loads(Path(sys.argv[2]).read_text())
 Path(sys.argv[3]).write_text("durable-before-push")
''')
    hook_a, hook_b = d.remotes["a"] / "hooks/pre-receive", d.remotes["b"] / "hooks/pre-receive"
    hook_a.write_text("#!/bin/sh\nexec python3 " + " ".join(shlex.quote(str(p)) for p in (verifier, d.state / "bench.db", expected_file, observed)) + "\n")
    hook_b.write_text('#!/bin/sh\nwhile read old new ref; do if test "$ref" = refs/heads/bench/card; then exit 1; fi; done\n')
    hook_a.chmod(0o700); hook_b.chmod(0o700)
    try:
        partial, _ = d.request(d.payload("checkpoint", key="card", lease_token=token), (409, 422))
        pending = partial["result"]["workspace"]["pending"]
        assert observed.read_text() == "durable-before-push"
        assert pending["members"] == [{"name": x["Name"], "commit": x["Commit"]} for x in expected]
        assert partial["result"]["workspace"]["checkpoint"] == old
        assert d.git(d.remotes["a"], "rev-parse", "refs/heads/bench/card") == expected[0]["Commit"]
        assert d.git(d.remotes["b"], "rev-parse", "refs/heads/bench/card") != expected[1]["Commit"]
        refused, _ = d.request(d.payload("release", key="card", lease_token=token), (409, 422))
        assert not refused["ok"] and d.record("card")["Lease"]["Token"] == token
        hook_b.unlink()
        completed, _ = d.request(d.payload("checkpoint", key="card", lease_token=token))
        cp = completed["result"]["workspace"]["checkpoint"]
        assert cp["generation_id"] == pending["generation_id"] and cp["members"] == pending["members"]
        released, _ = d.request(d.payload("release", key="card", lease_token=token)); d.sealed(released)
    finally:
        for hook in (hook_a, hook_b):
            if hook.exists():
                hook.unlink()
    d.prove("pending-publication", 0, 1, 2)
    print("PASS: first remote hook independently observes the durable whole pending vector; second-member failure cannot release and retries the same generation", flush=True)


def stopped(pid):
    try:
        return Path(f"/proc/{pid}/stat").read_text().split(") ", 1)[1][0] == "Z"
    except FileNotFoundError:
        return True


def detached_recovery(d, crash):
    label = "crashed" if crash else "stopped"
    key = "crash" if crash else "card"
    if crash:
        made, _ = d.request(d.payload("create", key=key, repositories=[{"name": n, "repository": str(r)} for n, r in d.remotes.items()], checkpoint_repository="a", initialize=True))
        d.roots[key] = Path(made["result"]["root_path"]); d.sealed(made)
    pidfile, group, ready, release, late = [d.root / (label + "-" + suffix) for suffix in ("pid", "cgroup", "ready", "release", "late")]
    child = 'echo $$ > "$1"; cat /proc/self/cgroup > "$2"; printf ready > "$3"; while ! test -e "$4"; do sleep .02; done; printf forbidden > "$5"'
    command = 'setsid /bin/sh -c "$1" child "$2" "$3" "$4" "$5" "$6" </dev/null >/dev/null 2>&1 & wait'
    request = d.payload("run", key=key, holder=label, arguments=["/bin/sh", "-c", command, "payload", child, str(pidfile), str(group), str(ready), str(release), str(late)])
    d.abandon(request)
    wait_for(ready.exists, "detached " + label + " writer")
    pid = int(pidfile.read_text())
    execution_id = d.record(key)["Lease"]["Execution"]["ID"]
    assert "/" + d.cg.name + "/" + execution_id in group.read_text() and not stopped(pid)
    if crash:
        d.stop_daemon(crash=True)
        assert not stopped(pid), "fixture did not retain a writer after supervisor SIGKILL"
        d.start()
        uncertain, _ = d.request(request, 409)
        assert uncertain["error"]["code"] == "operation_in_doubt"
    admin, _ = d.request(d.payload("stop_admin", key=key)); token = d.grant(admin)
    assert admin["result"]["workspace"]["lease"]["stop_requested"]
    wait_for(lambda: stopped(pid), "verified detached descendant fence")
    assert not (d.cg / execution_id).exists() and not d.record(key)["Lease"].get("Continuation")
    release.write_text("only after successful fencing")
    assert not late.exists()
    revival = d.payload("run", key=key, holder=label, lease_token=token, arguments=["/usr/bin/touch", str(late)])
    refused, _ = d.request(revival, (403, 409, 422))
    assert not refused["ok"] and not late.exists()
    if not crash:
        def settled():
            reply, _ = d.request(request, (409, 422))
            if reply.get("error", {}).get("code") == "in_progress":
                return False
            assert not reply["ok"]
            return True
        wait_for(settled, "stopped operation retained outcome")
    if crash:
        d.request(d.payload("checkpoint", key=key, lease_token=token))
        refused, _ = d.request(d.payload("release", key=key, lease_token=token), (409, 422))
        assert not refused["ok"] and d.record(key)["Lease"]["Token"] == token
        assert d.request(request, 409)[0]["error"]["code"] == "operation_in_doubt"
        print("UNCOVERED crash-history-reconciliation: fenced interrupted payload history remains unsealed and owned; release safely refuses", flush=True)
    else:
        d.reconcile(key, token)
    d.prove("admin-recovery", 0, 1, 2)
    print("PASS: " + label + " supervisor retains ownership; explicit admin fence stops setsid descendants and revoked continuation cannot revive", flush=True)


def cli_and_recovery(d, members):
    before = d.events("card")
    cli = d.cli("run", "card", flags=["--holder", "cli-owner", "--request-id", "cli-held", "--repository-name", "a", "--checkpoint=false"], command=["/bin/sh", "-c", 'test "$PWD" = "$1"', "payload", str(d.root_for("card") / "a")])
    assert cli["version"] == "superbench.v2" and cli["ok"]
    token = d.grant(cli); d.delta("card", before, each=("a", "b"))
    failed = d.cli("run", "card", flags=["--holder", "cli-owner", "--request-id", "cli-failed", "--token", token], command=["/bin/sh", "-c", "exit 7"], expected=7)
    assert failed["result"]["exit_code"] == 7 and "grant" not in failed["result"]
    d.reconcile("card", token)
    source = d.record("card"); cp = source["Workspace"]["Checkpoint"]
    source_refs = {name: d.git(remote, "show-ref") for name, remote in d.remotes.items()}
    original = d.record("card")
    source_identity = dict(repository=str(d.remotes["a"]), bench_id=source["Workspace"]["ID"], commit=cp["Seal"], manifest_sha256=cp["Digest"])
    recovered, _ = d.request(d.payload("recover", key="recovered", repositories=members, source=source_identity, initialize=True))
    d.roots["recovered"] = Path(recovered["result"]["root_path"])
    d.sealed(recovered)
    target = d.record("recovered")
    assert target["Workspace"]["ID"] != source["Workspace"]["ID"] and target["ProjectID"] != source["ProjectID"]
    assert target["Workspace"]["RecoveredFrom"] == {"Repository": source_identity["repository"], "BenchID": source_identity["bench_id"], "Commit": source_identity["commit"], "ManifestSHA256": source_identity["manifest_sha256"]}
    assert d.events("recovered") == {n: {"once": 1, "each": 1} for n in ("a", "b")}
    assert d.record("card") == original
    for name, remote in d.remotes.items():
        for line in source_refs[name].splitlines():
            commit, ref = line.split()
            assert d.git(remote, "rev-parse", ref) == commit
    d.prove("exact-recovery", 0, 2, 3)
    # Local explicit approval uses zero grace on a separate target. Remote archive
    # cannot shorten the persisted default grace of the original workspace.
    d.cli("archive", "recovered", remote=False, flags=["--archive-grace", "0s"])
    plan = d.cli("plan-reap", "recovered", remote=False)
    assert len(plan["Members"]) == 2 and plan["Checkpoint"] == target["Workspace"]["Checkpoint"]
    reviewed = {e["Path"]: e["Recoverable"] for e in plan["Tree"]["Entries"]}
    for name in ("a", "b"):
        assert reviewed[name + "/.git"] is False and reviewed[name + "/.bench/output/once.log"] is False
    surviving = {key: d.record(key) for key in ("card", "partial")}
    before_refs = {n: d.git(r, "show-ref") for n, r in d.remotes.items()}
    planfile = d.root / "reviewed-reap.json"; planfile.write_text(json.dumps(plan)); planfile.chmod(0o600)
    flags = ["--plan", planfile, "--approve", plan["Digest"]]
    reaped = d.cli("reap", "recovered", remote=False, flags=flags)
    assert reaped["kind"] == "workspace" and reaped["phase"] == "reaped" and not d.root_for("recovered").exists()
    assert d.cli("reap", "recovered", remote=False, flags=flags) == reaped
    ledger = json.loads((d.root / "quota-ledger/assignments.json").read_text())["Assignments"]
    retired = ledger["bench:recovered"]
    assert retired["ProjectID"] == target["ProjectID"] and retired["Bytes"] == retired["Inodes"] == 0
    for n, remote in d.remotes.items():
        assert d.git(remote, "show-ref") == before_refs[n]
    for member in target["Workspace"]["Members"]:
        registration = d.git(member["Mirror"], "worktree", "list", "--porcelain", "-z")
        assert "worktree " + member["Path"] + "\0" not in registration
        for key, untouched in surviving.items():
            assert d.record(key) == untouched
            other = next(m for m in untouched["Workspace"]["Members"] if m["Name"] == member["Name"])
            assert "worktree " + other["Path"] + "\0" in registration and Path(other["Path"]).is_dir()
    source_identity = dict(repository=str(d.remotes["a"]), bench_id=target["Workspace"]["ID"], commit=target["Workspace"]["Checkpoint"]["Seal"], manifest_sha256=target["Workspace"]["Checkpoint"]["Digest"])
    again, _ = d.request(d.payload("recover", key="from-reaped", repositories=members, source=source_identity, initialize=True))
    d.roots["from-reaped"] = Path(again["result"]["root_path"]); d.sealed(again)
    assert d.record("card") == original
    d.prove("reap-retry", 0, 1, 3, 5)
    print("PASS: actual v2 CLI preserves member cwd/exit7/token reuse; distinct recovery gets fresh hooks/project; local reviewed reap and reaped-seal recovery preserve source refs", flush=True)


def peer_request(d, uid, payload):
    # Real kernel peer credentials, not an asserted HTTP identity header.
    code = '''import http.client,json,socket,sys
class C(http.client.HTTPConnection):
 def connect(self):
  self.sock=socket.socket(socket.AF_UNIX);self.sock.connect(sys.argv[1])
c=C("local",timeout=20)
c.request("POST","/v2/operations",sys.stdin.read(),{"Content-Type":"application/json","X-Operator-UID":"0"})
r=c.getresponse();print(json.dumps({"status":r.status,"body":json.loads(r.read())}))
'''
    def identity():
        os.setgroups([]); os.setgid(uid); os.setuid(uid)
    result = subprocess.run(["python3", "-c", code, str(d.sock)], input=json.dumps(payload), capture_output=True, text=True, timeout=25, preexec_fn=identity)
    assert result.returncode == 0, "owned alternate-UID client failed"
    return json.loads(result.stdout)


def status_and_retention(d, members):
    d.run(d.local("create-cold", "--repo", d.remotes["a"], "scalar"))
    status, raw = d.request(d.payload("status"))
    entries = status["result"]["benches"]
    with sqlite3.connect(f"file:{d.state / 'bench.db'}?mode=ro", uri=True) as db:
        keys = [r[0] for r in db.execute("SELECT key FROM benches ORDER BY key")]
    assert [b["key"] for b in entries] == keys
    assert next(b for b in entries if b["key"] == "scalar")["kind"] == "scalar"
    assert all(b["kind"] == "workspace" for b in entries if b["key"] != "scalar")
    forbidden = {"token", "lease_token", "continuation", "root_path", "path", "repository", "mirror", "project_id", "arguments", "environment", "log"}
    def public(value):
        if isinstance(value, dict):
            assert not forbidden.intersection(value)
            for child in value.values():
                public(child)
        elif isinstance(value, list):
            for child in value:
                public(child)
    public(status)
    assert all(t.encode() not in raw for t in d.tokens)
    assert str(d.root).encode() not in raw
    d.prove("mixed-status", 0, 1)

    # Socket access is opened only on this disposable fixture; server policy
    # remains authoritative for real reader/operator kernel credentials.
    d.root.chmod(0o755); d.sock.chmod(0o666)
    read = peer_request(d, 65534, d.payload("status"))
    assert read["status"] == 200 and read["body"] == status
    refused = peer_request(d, 65534, d.payload("stop_admin", key="card"))
    assert refused["status"] == 403 and "result" not in refused["body"]
    for request in (d.original_create, d.partial_create):
        refused = peer_request(d, 65533, request)
        assert refused["status"] == 409 and "result" not in refused["body"]
    changed = dict(d.original_create, key="body-conflict")
    refused, _ = d.request(changed, 409)
    assert "result" not in refused and not d.root_for("body-conflict").exists()
    d.prove("receipt-replay", 0); d.prove("partial-initialization", 1)
    c = UnixHTTP(d.sock)
    try:
        c.request("POST", "/v2/operations", json.dumps({"version": "superbench.v2", "operation": "reap", "key": "card"}), {"Content-Type": "application/json"})
        response = c.getresponse(); rejected = json.loads(response.read())
        assert response.status == 400 and "result" not in rejected
    finally:
        c.close()
    d.prove("complete-public-lifecycle", 1, 2)
    print("PASS: mixed status is complete and private; real reader/operator UID gates and canonical request binding prevent capability adoption", flush=True)

    held, _ = d.request(d.payload("lease", key="card", holder="capacity-owner")); token = d.grant(held)
    before = d.record("card")
    # Exhaust only this owned daemon's disposable receipt database. Evidence is
    # never deleted to reopen growth; all following operations reconcile state.
    with sqlite3.connect(d.state / "service.db") as db:
        db.execute("WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO receipts(owner,id,digest,state,body,status) SELECT 9999,'fill-'||x,'unused','done',X'7B7D',200 FROM n")
        count = db.execute("SELECT count(*) FROM receipts").fetchone()[0]
    refusal, bounded = d.request(d.payload("create", key="capacity-new", repositories=members, checkpoint_repository="a", initialize=True), 503)
    assert refusal["error"]["code"] == "unavailable" and len(bounded) <= 64 << 10
    refusal, _ = d.request(d.payload("setup", key="card", holder="capacity-owner", lease_token=token), 503)
    assert refusal["error"]["code"] == "unavailable" and d.record("card") == before and not d.root_for("capacity-new").exists()
    d.request(d.payload("stop", key="card", lease_token=token))
    admin, _ = d.request(d.payload("stop_admin", key="card")); assert d.grant(admin) == token
    d.reconcile("card", token)
    archived, _ = d.request(d.payload("archive", key="card"))
    workspace = archived["result"]["workspace"]
    assert workspace["phase"] == "archived" and workspace["archive"]["frozen_at"] and "grant" not in archived["result"]
    assert d.record("card").get("Lease") is None
    for path in [d.root_for("card"), *d.root_for("card").rglob("*")]:
        if not path.is_symlink():
            assert stat.S_IMODE(path.stat().st_mode) & 0o222 == 0
    # Concrete local review remains usable without a growth receipt.
    plan = d.cli("plan-reap", "card", remote=False)
    assert any(not entry["Recoverable"] for entry in plan["Tree"]["Entries"])
    grace_plan = d.root / "grace-reviewed-plan.json"; grace_plan.write_text(json.dumps(plan)); grace_plan.chmod(0o600)
    before_grace = d.record("card")
    refused = d.run([d.args.bench, "workspace", "reap", "--state", d.state, "--data", d.data, "--cgroup-root", d.cg, "--plan", grace_plan, "--approve", plan["Digest"], "card"], expected=1)
    assert d.record("card") == before_grace and d.root_for("card").is_dir()
    d.cli("archive", "partial", remote=False, flags=["--archive-grace", "0s"])
    plan = d.cli("plan-reap", "partial", remote=False)
    path = d.root / "capacity-reviewed-plan.json"; path.write_text(json.dumps(plan)); path.chmod(0o600)
    reaped = d.cli("reap", "partial", remote=False, flags=["--plan", path, "--approve", plan["Digest"]])
    assert reaped["phase"] == "reaped" and not d.root_for("partial").exists()
    with sqlite3.connect(d.state / "service.db") as db:
        assert db.execute("SELECT count(*) FROM receipts").fetchone()[0] == count
    d.prove("capacity-reconciliation", 0, 1, 2)
    d.prove("archive-review", 0, 1, 3)
    print("PASS: bounded receipt exhaustion blocks growth while stop/admin/checkpoint/release/archive and explicitly approved local reap remain usable", flush=True)


if __name__ == "__main__":
    main()
