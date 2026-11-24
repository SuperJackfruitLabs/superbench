#!/usr/bin/env python3
"""Privileged drill: remote access to benches through the real installed stack.

Run as direct root on a disposable Linux host where this revision was installed
with deploy/linux/install.py --root / --managed --prepare --identity --activation
--storage, and superbench.service has never been started. The drill owns that
host's /etc/superbench/activation.json and restarts superbench.service several
times, so it refuses to run without --disposable, on a host that already has
workspaces, or where benchd already created its remote key directory. Never run
it on a host whose benches or benchd someone else uses.

It drives a stock OpenSSH client (ssh, sftp, ssh-keygen) against the installed
benchd, so sessions go through the real sudo rule, bench-payload session mode,
the lease's delegated cgroup scope and bench-sftp. Every lease token stays in
this process: it is passed to the CLI only through BENCH_LEASE_TOKEN, and the
last check searches everything the drill printed or captured, and the unit's
journal, for every token it was given.

docs/remote-access-output.txt was recorded in a disposable systemd container
(Ubuntu 26.04, --privileged with its own cgroup namespace, so systemd delegates
superbench.service a real cgroup v2 subtree) on a staging host whose own benchd
was never touched: the container has its own accounts, /etc, sudo rule, unit,
socket and loopback network. /srv/superbench-data was a fresh XFS image on a
loop device mounted with prjquota; /var/lib/superbench and
/var/lib/superbench-quota were bind mounts of ext4 directories (the ledger
needs a filesystem UUID). After the installer, `bench configure-host` ran as
the service account and `bench-quota scan-projects` as root, as
docs/pilot-installation.md describes; then this drill, which takes about 33
minutes because it waits for a certificate to expire.

Notes for operators, from what this drill shows:
- A remote access failure stops benchd. If the fleet address cannot be bound
  (another process holds it, or the address does not exist yet), benchd exits
  non-zero and its local socket goes away with it; systemd's Restart=on-failure
  starts it again every 3 seconds until the bind succeeds. At boot, the fleet
  address (for example a tailnet address) may appear only after benchd starts,
  so benchd restarts until it does.
- A session's detached leftovers cannot outlive it: the session runs in its own
  PID namespace, which the kernel empties when the session ends. A process that
  is in the lease's remote scope by any other route is killed by the scope's
  cgroup.kill fence on checkpoint, release, stop and stop_admin.
"""
import argparse
from datetime import datetime
import http.client
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time

STATE = Path("/var/lib/superbench/state")
DATA = Path("/srv/superbench-data")
ACTIVATION = Path("/etc/superbench/activation.json")
UNIT = "superbench.service"
PAYLOAD_ACCOUNT = "superbench-payload"
DAEMON_ACCOUNT = "superbench"
FRESH = {"fresh_payload": True, "finite_io": True, "receipt_lookup": True,
         "cancellation": True, "registration_observation": True,
         "fresh_reclaim_idle_hours": 0}

SECRETS = []   # every lease token the drill was given
CAPTURED = []  # everything printed or captured, searched for SECRETS at the end


def say(line):
    for secret in SECRETS:
        if secret in line:
            raise AssertionError("refusing to print a lease token")
    CAPTURED.append(line)
    print(line, flush=True)


def passed(line):
    say("PASS: " + line)


def keep(text):
    if isinstance(text, bytes):
        text = text.decode("utf-8", "replace")
    CAPTURED.append(text)
    return text


def wait_for(check, description, timeout=20.0, interval=0.05):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(interval)
    raise AssertionError("timeout: " + description)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path, timeout=60):
        super().__init__("localhost", timeout=timeout)
        self.unix = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.unix)


class Drill:
    def __init__(self, args, work):
        self.bench, self.socket, self.work = args.bench, Path(args.socket), work
        self.host, port = args.fleet_address.rsplit(":", 1)
        self.port = int(port)
        self.fleet = args.fleet_address
        self.payload_uid = pwd.getpwnam(PAYLOAD_ACCOUNT).pw_uid
        self.daemon = pwd.getpwnam(DAEMON_ACCOUNT)
        self.known_hosts = work / "known_hosts"
        self.keys = 0

    # --- socket ---------------------------------------------------------
    def post(self, path, body, timeout=120, grant=False):
        """grant: the reply is a grant, where a lease token is meant to be
        (lease and stop_admin); it is not added to the searched corpus."""
        c = UnixHTTP(self.socket, timeout)
        try:
            c.request("POST", path, json.dumps(body), {"Content-Type": "application/json"})
            r = c.getresponse()
            raw = r.read().decode() if grant else keep(r.read())
            return r.status, json.loads(raw) if raw else None
        finally:
            c.close()

    def op(self, operation, expected=200, grant=False, **fields):
        status, body = self.post("/v2/operations", {"version": "superbench.v2", "operation": operation, **fields},
                                 grant=grant)
        assert status == expected, (operation, status, body and body.get("error"))
        return body

    def remote_status(self, key=None):
        body = {"version": "superbench.remote.v1", "operation": "remote_status"}
        if key:
            body["key"] = key
        status, reply = self.post("/v2/remote/status", body)
        assert status == 200, (status, reply)
        return reply["result"]

    def healthy(self):
        try:
            c = UnixHTTP(self.socket, 2)
            c.request("GET", "/healthz")
            r = c.getresponse()
            r.read()
            c.close()
            return r.status == 200
        except (OSError, http.client.HTTPException):
            return False

    # --- service ----------------------------------------------------------
    def activate(self, **switches):
        document = {"version": "superbench.activation.v1", **switches}
        temporary = ACTIVATION.with_name(".activation.json.drill")
        temporary.write_text(json.dumps(document, indent=2) + "\n")
        os.chmod(temporary, 0o644)
        os.replace(temporary, ACTIVATION)

    def systemctl(self, *words, check=True):
        return subprocess.run(["systemctl", *words], check=check, capture_output=True, text=True)

    def restart(self):
        self.systemctl("restart", UNIT)
        wait_for(self.healthy, "benchd socket ready", timeout=60)

    def main_pid(self):
        return int(self.systemctl("show", "-p", "MainPID", "--value", UNIT).stdout.strip())

    def benchd_listeners(self):
        out = keep(subprocess.run(["ss", "-H", "-ltnp"], check=True, capture_output=True, text=True).stdout)
        return [line for line in out.splitlines() if '"benchd"' in line]

    def unit_cgroup(self):
        return Path("/sys/fs/cgroup" + self.systemctl("show", "-p", "ControlGroup", "--value", UNIT).stdout.strip())

    # --- workspaces -------------------------------------------------------
    def create(self, key, repositories):
        result = self.op("create", key=key, request_id="create-" + key, initialize=True,
                         checkpoint_repository=repositories[0]["name"],
                         repositories=repositories)["result"]
        return result["workspace"]["bench_id"], result.get("root_path", "")

    def lease(self, key, holder="drill"):
        self.leases = getattr(self, "leases", 0) + 1
        result = self.op("lease", grant=True, key=key, holder=holder, request_id=f"lease-{key}-{self.leases}")["result"]
        token = result["grant"]["lease_token"]
        SECRETS.append(token)
        return token

    # --- client keys and certificates -------------------------------------
    def client_key(self):
        self.keys += 1
        path = self.work / f"client-{self.keys}"
        subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", str(path)],
                       check=True, capture_output=True)
        return path

    def credential(self, key, token, client=None, expect_ok=True):
        client = client or self.client_key()
        cert = Path(str(client) + "-cert.pub")
        env = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "BENCH_LEASE_TOKEN": token}
        r = subprocess.run([self.bench, "workspace", "remote-credential", "--socket", str(self.socket),
                            "--public-key", str(client) + ".pub", "--certificate-out", str(cert), key],
                           env=env, capture_output=True, text=True)
        keep(r.stdout), keep(r.stderr)
        if not expect_ok:
            assert r.returncode != 0, "credential unexpectedly issued"
            return r.stderr
        assert r.returncode == 0, r.stderr
        c = json.loads(r.stdout)
        c["client"], c["cert"] = client, cert
        self.known_hosts.write_text(f"[{self.host}]:{self.port} {c['host_key']}\n")
        return c

    def options(self, c):
        return ["-F", "/dev/null", "-i", str(c["client"]), "-o", "CertificateFile=" + str(c["cert"]),
                "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "UserKnownHostsFile=" + str(self.known_hosts),
                "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"]

    def ssh_command(self, c, user=None, tty=False, extra=()):
        return ["ssh", *self.options(c), "-p", str(self.port), *extra, "-tt" if tty else "-T",
                (user or c["user"]) + "@" + self.host]

    def ssh(self, c, command, user=None, timeout=60, stdin=None, tty=False):
        r = subprocess.run(self.ssh_command(c, user, tty) + ([command] if command else []),
                           capture_output=True, text=True, timeout=timeout, input=stdin)
        keep(r.stdout), keep(r.stderr)
        return r

    def refused(self, c, user=None):
        r = self.ssh(c, "true", user)
        return r.returncode == 255 and "Permission denied" in r.stderr

    def accepted(self, c, user=None):
        r = self.ssh(c, "echo accepted", user)
        return r.returncode == 0 and r.stdout == "accepted\n"

    def background(self, c, command):
        log = tempfile.TemporaryFile(dir=self.work)
        words = self.ssh_command(c, extra=("-N",) if command is None else ())
        proc = subprocess.Popen(words + ([command] if command else []), stdin=subprocess.DEVNULL,
                                stdout=log, stderr=subprocess.STDOUT)
        proc.log = log
        return proc

    def output(self, proc):
        proc.log.seek(0)
        return keep(proc.log.read())

    # --- scopes -----------------------------------------------------------
    def execution(self, key):
        remote = self.remote_status(key)["remote"]
        assert len(remote) == 1, remote
        return remote[0]

    def scope(self, execution_id):
        found = [p for p in self.unit_cgroup().rglob(execution_id) if p.is_dir()]
        assert len(found) == 1, found
        return found[0]

    def scope_pids(self, scope):
        pids = []
        for path in [scope, *[p for p in scope.rglob("*") if p.is_dir()]]:
            try:
                pids += [int(x) for x in (path / "cgroup.procs").read_text().split()]
            except FileNotFoundError:
                pass
        return pids

    def open_sessions(self, key):
        return [s for s in self.remote_status(key)["sessions"] if not s["end_reason"]]

    def cut(self, key, c, action, description):
        """A live `sleep 60` session ends within 2 s of action, its scope goes."""
        execution = self.execution(key)["execution_id"]
        scope = self.scope(execution)
        proc = self.background(c, "sleep 60")
        wait_for(lambda: self.open_sessions(key), "session open")
        wait_for(lambda: any(cmdline(p) == ["sleep", "60"] for p in self.scope_pids(scope)), "sleep in scope")
        started = time.monotonic()
        action()
        code = proc.wait(timeout=10)
        elapsed = time.monotonic() - started
        self.output(proc)
        assert code != 0, code
        assert elapsed < 2.0, elapsed
        wait_for(lambda: not scope.exists(), "scope removed", timeout=5)
        assert not self.open_sessions(key)
        # The fence kills the scope before it writes "fenced", so the session's
        # own end ("exit") usually lands first. Report what the audit holds.
        reason = self.remote_status(key)["sessions"][0]["end_reason"]
        assert self.refused(c), "certificate still admitted after " + description
        passed(f"{description} cut a live `sleep 60` session in {elapsed:.2f}s (ssh exit {code}) and "
               f"removed its scope; audit end_reason {reason!r}; the certificate is refused afterwards")


def cmdline(pid):
    try:
        return Path(f"/proc/{pid}/cmdline").read_bytes().rstrip(b"\0").decode().split("\0")
    except (FileNotFoundError, ProcessLookupError):
        return None


def alive(pid):
    try:
        state = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1][0]
        return state != "Z"
    except FileNotFoundError:
        return False


def git(directory, *words):
    subprocess.run(["git", "-C", str(directory), "-c", "user.name=Drill", "-c", "user.email=drill@example.invalid",
                    *words], check=True, capture_output=True)


def repositories(base):
    found = []
    for name in ("api", "web"):
        # Bare, like a forge: create pushes the checkpoint repository's seal.
        repo, seed = base / (name + ".git"), base / ("seed-" + name)
        git(base, "init", "-q", "--bare", "-b", "main", str(repo))
        git(base, "init", "-q", "-b", "main", str(seed))
        (seed / "source.txt").write_text(name + " base\n")
        git(seed, "add", ".")
        git(seed, "commit", "-q", "-m", "base")
        git(seed, "push", "-q", str(repo), "main")
        shutil.rmtree(seed)
        found.append({"name": name, "repository": str(repo)})
    subprocess.run(["chown", "-R", DAEMON_ACCOUNT + ":" + DAEMON_ACCOUNT, str(base)], check=True)
    return found


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--bench", required=True, help="the installed bench CLI")
    parser.add_argument("--socket", required=True, help="the installed benchd socket")
    parser.add_argument("--fleet-address", default="127.0.0.1:2222",
                        help="remote_fleet_address for the drill; loopback keeps it off every network")
    parser.add_argument("--disposable", action="store_true",
                        help="confirm this host is disposable: the drill rewrites its activation file")
    args = parser.parse_args()
    if not args.disposable:
        parser.error("--disposable is required: the drill rewrites /etc/superbench/activation.json")
    if os.geteuid() != 0 or os.getuid() != 0 or "SUDO_UID" in os.environ:
        raise SystemExit("requires direct root on the disposable host")
    for tool in ("ssh", "sftp", "ssh-keygen", "ss", "systemctl", "git"):
        if shutil.which(tool) is None:
            raise SystemExit("requires " + tool)
    if (STATE / "remote").exists():
        raise SystemExit("refusing: benchd already created " + str(STATE / "remote") + "; use a fresh install")
    if (DATA / "benches").exists() and any((DATA / "benches").iterdir()):
        raise SystemExit("refusing: this host already has benches")

    work = Path(tempfile.mkdtemp(prefix="remote-drill-", dir="/root"))
    # The unit can write only its own paths (ProtectSystem=strict), and create
    # pushes to the checkpoint repository, so the drill's origin repositories
    # live beside the state directory, outside state and data.
    sources = Path(tempfile.mkdtemp(prefix="drill-origin-", dir=STATE.parent))
    d = Drill(args, work)
    say("drill: " + subprocess.run(["ssh", "-V"], capture_output=True, text=True).stderr.strip())
    say(f"drill: fleet address {args.fleet_address}; payload uid {d.payload_uid}; daemon uid {d.daemon.pw_uid}")

    # 1. Off costs nothing. The first start, every switch off, migrates state.
    d.activate()
    d.restart()
    d.activate(**FRESH)
    d.restart()
    pid = d.main_pid()
    assert d.benchd_listeners() == [], d.benchd_listeners()
    assert not (STATE / "remote").exists()
    status, body = d.post("/v2/remote/status", {"version": "superbench.remote.v1", "operation": "remote_status"})
    assert status == 404, (status, body)
    passed(f"remote_access off on fresh state: benchd (pid {pid}) has no TCP listener in `ss -ltnp`, "
           f"{STATE / 'remote'} is absent and /v2/remote/status answers 404")

    # 2. Two Fresh workspaces with two members each.
    repos = repositories(sources)
    drill_id, drill_root = d.create("drill", repos)
    other_id, other_root = d.create("other", repos)
    say(f"drill: workspaces drill={drill_id} other={other_id}")

    # 3. A remote access failure stops benchd; systemd restarts it until the bind works.
    on = dict(FRESH, remote_access=True, remote_fleet_address=args.fleet_address)
    d.activate(**on)
    blocker = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    blocker.bind((d.host, d.port))
    blocker.listen(1)
    before = int(d.systemctl("show", "-p", "NRestarts", "--value", UNIT).stdout)
    d.systemctl("restart", UNIT, check=False)
    wait_for(lambda: int(d.systemctl("show", "-p", "NRestarts", "--value", UNIT).stdout) >= before + 2,
             "benchd restarted after the failed bind", timeout=30)
    assert not d.healthy()
    blocker.close()
    wait_for(d.healthy, "benchd serves once the address is free", timeout=30)
    restarts = int(d.systemctl("show", "-p", "NRestarts", "--value", UNIT).stdout) - before
    passed(f"with the fleet address taken benchd exits and its socket is gone; systemd restarted it "
           f"{restarts} times and it served once the address was free")

    listeners = d.benchd_listeners()
    assert len(listeners) == 1 and args.fleet_address in listeners[0], listeners
    remote = STATE / "remote"
    assert remote.stat().st_mode & 0o777 == 0o700 and remote.stat().st_uid == d.daemon.pw_uid
    for key in remote.iterdir():
        assert key.stat().st_mode & 0o777 == 0o600, key
    host_status = d.remote_status()
    assert host_status["address"] == args.fleet_address
    passed(f"remote_access on: benchd listens only on {args.fleet_address}; {remote} is 0700 with "
           f"{len(list(remote.iterdir()))} keys at 0600; host key {host_status['host_key_fingerprint']}")

    # 4. A writer certificate for a real lease, then exec, PTY shell and SFTP.
    token = d.lease("drill")
    c = d.credential("drill", token)
    inspected = keep(subprocess.run(["ssh-keygen", "-L", "-f", str(c["cert"])], capture_output=True,
                                    text=True, check=True).stdout)
    assert c["user"] == "b-" + drill_id and c["lease_id"] in inspected and "b-" + drill_id in inspected
    assert c["generation"] >= 1 and c["address"] == args.fleet_address
    passed(f"writer certificate for lease {c['lease_id']} generation {c['generation']}, user {c['user']}, "
           f"valid before {c['valid_before']}")

    r = d.ssh(c, "id -u")
    assert r.returncode == 0 and r.stdout.strip() == str(d.payload_uid), (r.returncode, r.stdout, r.stderr)
    passed(f"ssh {c['user']}@{args.fleet_address} id -u prints the payload uid {d.payload_uid}")
    r = d.ssh(c, "pwd; ls")
    assert r.returncode == 0, r.stderr
    cwd, *entries = r.stdout.split()
    assert sorted(entries) == ["api", "web"], entries
    passed(f"exec starts in the workspace root {cwd}, which holds its members api and web")
    r = d.ssh(c, f"ls {DATA}/benches")
    host_benches = sorted(p.name for p in (DATA / "benches").iterdir())
    assert r.returncode == 0 and r.stdout.split() == ["drill"] and host_benches == ["drill", "other"], (r.stdout, r.stderr)
    passed(f"ls {DATA}/benches in the session shows only {r.stdout.strip()}; the host has {host_benches}")
    r = d.ssh(c, "cat /proc/self/cgroup")
    execution = d.execution("drill")
    assert r.returncode == 0 and execution["execution_id"] in r.stdout, (r.stdout, execution)
    passed(f"/proc/self/cgroup is {r.stdout.strip()}, the lease's remote scope {execution['execution_id']}")

    r = d.ssh(c, None, tty=True, stdin="tty; test -t 0 && echo TTY_OK; pwd; exit\n")
    assert r.returncode == 0 and "TTY_OK" in r.stdout and "/dev/pts/" in r.stdout and cwd in r.stdout, r.stdout
    passed("PTY shell: tty is a /dev/pts terminal and the login shell starts in the workspace root")

    upload = work / "upload.txt"
    upload.write_text("uploaded over sftp\n")
    batch = work / "batch"
    batch.write_text(f"pwd\nput {upload} uploaded.txt\nget api/source.txt {work / 'downloaded.txt'}\nls\n"
                     "-get /etc/passwd " + str(work / "passwd") + "\n-cd ..\npwd\n")
    r = subprocess.run(["sftp", "-b", str(batch), *d.options(c), "-P", str(d.port), c["user"] + "@" + d.host],
                       capture_output=True, text=True, timeout=60)
    keep(r.stdout), keep(r.stderr)
    assert r.returncode == 0, (r.stdout, r.stderr)
    say("drill: sftp transcript (stdout, then stderr):\n" +
        "\n".join("  " + line for line in (r.stdout + r.stderr).splitlines()))
    assert (work / "downloaded.txt").read_text() == "api base\n"
    assert not (work / "passwd").exists()
    found = subprocess.run(["find", str(DATA / "benches" / "drill"), "-name", "uploaded.txt"],
                           capture_output=True, text=True).stdout.split()
    assert len(found) == 1 and Path(found[0]).read_text() == "uploaded over sftp\n", found
    assert os.stat(found[0]).st_uid == d.payload_uid
    assert d.ssh(c, "rm uploaded.txt").returncode == 0  # checkpoint refuses untracked files
    passed(f"sftp put and get in the workspace root ({found[0]} is owned by the payload uid); "
           "the server's / is the workspace root: /etc/passwd is not there and cd .. stays at /")

    r = subprocess.run(d.ssh_command(c, extra=("-v",)) + ["true"], capture_output=True, text=True, timeout=60)
    keep(r.stdout), keep(r.stderr)
    assert r.returncode == 0 and "Server accepts key" in r.stderr and "SuperBench" not in r.stdout, r.stderr[-400:]
    banner = [line for line in r.stderr.splitlines() if "Remote protocol version" in line]
    say("drill: " + (banner[0].removeprefix("debug1: ") if banner else "no server version line"))

    # 5. A benchd restart refuses the old certificate; a new one for the same lease works.
    assert d.accepted(c)
    old_pid = d.main_pid()
    d.restart()
    assert d.main_pid() != old_pid
    assert d.refused(c), "certificate admitted after a benchd restart"
    c = d.credential("drill", token)
    assert d.accepted(c)
    passed("after `systemctl restart superbench.service` the earlier certificate is refused (its scope "
           "went with the old daemon); a new certificate for the same lease is accepted")

    # 6. Wrong bench. The other workspace's certificate also starts the expiry check.
    other_token = d.lease("other")
    e = d.credential("other", other_token)
    expiry = datetime.fromisoformat(e["valid_before"].replace("Z", "+00:00")).timestamp()
    idle = d.background(e, None)  # ssh -N: an authenticated connection with no session
    time.sleep(2)
    assert idle.poll() is None, d.output(idle)
    assert d.accepted(e) and d.accepted(c)
    assert d.refused(e, user=c["user"]), "other's certificate admitted to drill"
    assert d.refused(c, user=e["user"]), "drill's certificate admitted to other"
    passed(f"wrong bench refused both ways: {e['user'][:10]}...'s certificate as {c['user'][:10]}... and "
           "the reverse; each is accepted for its own bench")

    # 7. checkpoint, release, stop and stop_admin each cut a live session.
    d.cut("drill", c, lambda: d.op("checkpoint", key="drill", lease_token=token), "checkpoint")
    c = d.credential("drill", token)
    d.cut("drill", c, lambda: d.op("release", key="drill", lease_token=token), "release")
    token = d.lease("drill")
    c = d.credential("drill", token)
    d.cut("drill", c, lambda: d.op("stop", key="drill", lease_token=token), "stop")
    refusal = d.credential("drill", token, expect_ok=False)
    say("drill: a certificate for the stopped lease is refused: " + refusal.strip().splitlines()[-1])
    d.op("release", key="drill", lease_token=token)
    token = d.lease("drill")
    c = d.credential("drill", token)
    # stop_admin hands the operator the retained lease grant, like lease does.
    d.cut("drill", c, lambda: d.op("stop_admin", grant=True, key="drill"), "stop_admin")

    # 8. renew_admin gives the stopped lease a new token and generation: the old
    # generation's certificate stays refused, the new one is accepted.
    old = c
    r = subprocess.run([d.bench, "workspace", "renew_admin", "--state", str(STATE), "--data", str(DATA),
                        "--cgroup-root", str(d.unit_cgroup()), "drill"], capture_output=True, text=True)
    assert r.returncode == 0, r.stderr
    renewed = json.loads(r.stdout)
    token = find_token(renewed)
    SECRETS.append(token)
    keep(r.stderr)  # stdout is renew_admin's grant: the token is meant to be there
    c = d.credential("drill", token)
    assert c["lease_id"] == old["lease_id"] and c["generation"] == old["generation"] + 1, (c["generation"], old["generation"])
    assert d.refused(old) and d.accepted(c) and d.refused(old)
    passed(f"renew_admin moved lease {c['lease_id']} from generation {old['generation']} to {c['generation']}: "
           "the old generation's certificate is refused while the new one is accepted")

    # 9. Abrupt disconnect, then release: the real cgroup.kill fence.
    execution = d.execution("drill")["execution_id"]
    scope = d.scope(execution)
    leftover_marker = "3607"
    proc = d.background(c, f"setsid nohup sleep {leftover_marker} >/dev/null 2>&1 </dev/null & echo LEFTOVER; exec sleep 600")
    wait_for(lambda: b"LEFTOVER" in (proc.log.seek(0) or proc.log.read()), "leftover started")
    leftover = wait_for(lambda: [p for p in d.scope_pids(scope) if cmdline(p) == ["sleep", leftover_marker]],
                        "leftover in scope")[0]
    proc.kill()  # SIGKILL the client: no channel close, no exit request
    proc.wait(timeout=10)
    d.output(proc)
    wait_for(lambda: not d.open_sessions("drill"), "server noticed the disconnect", timeout=30)
    gone_at_disconnect = wait_for(lambda: not alive(leftover), "leftover ends with its session", timeout=10)
    passed(f"abrupt disconnect (ssh client SIGKILLed): the detached `setsid nohup sleep` (host pid {leftover}) "
           "ended with its session's PID namespace; the session is audited closed")
    # A process that reaches the scope by any other route stays until the fence.
    planted = subprocess.Popen(["sleep", "3613"], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL, start_new_session=True)
    (scope / "cgroup.procs").write_text(str(planted.pid))
    time.sleep(2)  # longer than the server's sweep: nothing but the fence ends it
    assert planted.poll() is None and planted.pid in d.scope_pids(scope)
    started = time.monotonic()
    d.op("release", key="drill", lease_token=token)
    assert planted.wait(timeout=5) == -signal.SIGKILL
    elapsed = time.monotonic() - started
    wait_for(lambda: not scope.exists(), "scope removed", timeout=5)
    passed(f"a process placed in the lease's remote scope (pid {planted.pid}) outlived the disconnect and "
           f"the sweep, and release's cgroup.kill fence killed it (SIGKILL, {elapsed:.2f}s) and removed {scope.name}")

    # 10. Expiry: the idle connection closes at valid_before; the certificate is refused after.
    say(f"drill: waiting for {e['user'][:10]}...'s certificate to expire at {e['valid_before']} "
        f"({max(0, expiry - time.time()):.0f}s)")
    while idle.poll() is None and time.time() < expiry - 5:
        time.sleep(1)
    assert idle.poll() is None, "idle connection closed before its certificate expired"
    closed = wait_for(lambda: idle.poll() is not None and time.time(), "idle connection closed at expiry", timeout=70)
    d.output(idle)
    assert closed >= expiry - 5, (closed, expiry)
    wait_for(lambda: time.time() > expiry + 1, "past expiry", timeout=10)
    assert d.refused(e), "expired certificate admitted"
    fresh = d.credential("other", other_token)
    assert d.accepted(fresh)
    passed(f"expired certificate: its idle connection closed {closed - expiry:+.1f}s from valid_before and a "
           "new connection with it is refused; a fresh certificate for the same lease is accepted")

    # 11. Off again: the keys stay, nothing listens.
    d.activate(**FRESH)
    d.restart()
    assert d.benchd_listeners() == [], d.benchd_listeners()
    status, _ = d.post("/v2/remote/status", {"version": "superbench.remote.v1", "operation": "remote_status"})
    assert status == 404 and (STATE / "remote").is_dir()
    passed("remote_access off again: no benchd TCP listener and /v2/remote/status answers 404; the key directory stays")

    # 12. No lease token anywhere.
    journal = keep(subprocess.run(["journalctl", "-u", UNIT, "--no-pager", "-o", "cat"],
                                  capture_output=True, text=True, check=True).stdout)
    corpus = "\n".join(CAPTURED)
    leaked = sum(secret in corpus for secret in SECRETS)
    in_journal = sum(secret in journal for secret in SECRETS)
    assert leaked == 0 and in_journal == 0 and len(SECRETS) == 5, (leaked, in_journal, len(SECRETS))
    passed(f"none of the {len(SECRETS)} lease tokens appears in {len(CAPTURED)} captured outputs "
           f"({len(corpus)} bytes: credential CLI replies, every other socket reply including remote status "
           f"and its audit rows, ssh -v, exec, PTY and sftp transcripts, refusals, this drill's own lines) "
           f"or the unit's journal ({len(journal.splitlines())} lines); only the lease, stop_admin and renew_admin grants carry them")
    say("DONE: remote access drill passed")
    shutil.rmtree(work)


def find_token(document):
    """The one lease token in renew_admin's reply, wherever its field sits."""
    found = []
    def walk(value, key=""):
        if isinstance(value, dict):
            for k, v in value.items():
                walk(v, k)
        elif isinstance(value, list):
            for v in value:
                walk(v, key)
        elif isinstance(value, str) and "token" in key.lower() and re.fullmatch(r"[a-f0-9]{32}", value):
            found.append(value)
    walk(document)
    assert len(set(found)) == 1, len(found)
    return found[0]


if __name__ == "__main__":
    main()
