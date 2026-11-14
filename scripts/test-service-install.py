#!/usr/bin/env python3
"""Verify a staged payload in a unique disposable Linux systemd unit/account."""
import argparse
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import time
import uuid


def command(*args, **kwargs):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("local", timeout=15)
        self.path = str(path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--source", type=Path, required=True)
    p.add_argument("--bin-dir", type=Path, required=True)
    args = p.parse_args()
    if os.geteuid() != 0:
        p.error("requires disposable Linux root")
    name = "sb-svc-test-" + uuid.uuid4().hex[:12]
    base = Path("/opt") / name
    state = Path("/var/lib") / name
    runtime = Path("/run") / name
    unit = Path("/run/systemd/system") / (name + ".service")
    sudoers = Path("/etc/sudoers.d") / name
    created_user = False
    base.mkdir(mode=0o755)
    try:
        image = base / "image"
        command("python3", str(args.source / "deploy/linux/install.py"),
                "--bin-dir", str(args.bin_dir), "--root", str(image), "--managed")
        command("python3", str(args.source / "deploy/linux/install.py"),
                "--bin-dir", str(args.bin_dir), "--root", str(image), "--managed")
        command("visudo", "-cf", str(image / "etc/sudoers.d/superbench"))
        print("PASS: managed payload staged idempotently; fixed sudo policy validates", flush=True)
        command("useradd", "--system", "--user-group", "--no-create-home",
                "--home-dir", str(state), "--shell", "/usr/sbin/nologin", name)
        created_user = True
        # Install only a unique fixture rule, never the real product rule/account.
        helper = image / "usr/local/libexec/bench-quota"
        sudoers.write_text(f"{name} ALL=(root) NOPASSWD: {helper}\n")
        sudoers.chmod(0o440)
        command("visudo", "-cf", str(sudoers))
        data = base / "data"
        ledger = base / "quota-ledger"
        data.mkdir(); ledger.mkdir(mode=0o700)
        uid = int(command("id", "-u", name).stdout)
        gid = int(command("id", "-g", name).stdout)
        os.chown(data, uid, gid)
        wrapper = image / "usr/local/libexec/superbench-start"
        text = wrapper.read_text()
        for old, new in [
            ("/usr/local/bin/benchd", str(image / "usr/local/bin/benchd")),
            ("/var/lib/superbench", str(state)),
            ("/srv/superbench-data", str(data)),
            ("/run/superbench", str(runtime)),
        ]:
            text = text.replace(old, new)
        wrapper.write_text(text)
        service = (image / "etc/systemd/system/superbench.service").read_text()
        for old, new in [
            ("/var/lib/superbench-quota", str(ledger)),
            ("/var/lib/superbench", str(state)),
            ("/srv/superbench-data", str(data)),
            ("/etc/superbench/benchd.env", str(image / "etc/superbench/benchd.env")),
            ("/usr/local/libexec/superbench-start", str(wrapper)),
            ("User=superbench", "User=" + name),
            ("Group=superbench", "Group=" + name),
            ("StateDirectory=superbench", "StateDirectory=" + name),
            ("RuntimeDirectory=superbench", "RuntimeDirectory=" + name),
        ]:
            service = service.replace(old, new)
        unit.write_text(service)
        command("systemd-analyze", "verify", str(unit))
        command("systemctl", "daemon-reload")
        command("systemctl", "start", unit.name)
        sock = runtime / "benchd.sock"
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                c = UnixHTTP(sock); c.request("GET", "/healthz"); r = c.getresponse();r.read();c.close()
                if r.status == 200:
                    break
            except (OSError, http.client.HTTPException):
                pass
            time.sleep(0.05)
        else:
            raise AssertionError("fixture service failed to become ready")
        pid = int(command("systemctl", "show", "-p", "MainPID", "--value", unit.name).stdout)
        process_status = Path(f"/proc/{pid}/status").read_text()
        assert f"Uid:\t{uid}\t{uid}" in process_status
        print("PASS: installed systemd settings start an unprivileged daemon", flush=True)
        remote = data / "remote.git"
        seed = base / "seed"
        command("git", "init", "--bare", str(remote))
        command("git", "init", "-b", "main", str(seed))
        (seed / "file").write_text("base\n")
        # The fixture verifies both preparation stages under the service uid.
        (seed / ".bench").mkdir()
        (seed / ".bench/config.toml").write_text("[setup]\nonce='printf once >> .setup-events'\neach='printf each >> .setup-events'\n")
        (seed / ".gitignore").write_text(".setup-events\n")
        command("git", "-C", str(seed), "add", ".")
        command("git", "-C", str(seed), "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "base")
        command("git", "-C", str(seed), "remote", "add", "origin", str(remote))
        command("git", "-C", str(seed), "push", "origin", "main")
        command("git", "-C", str(remote), "symbolic-ref", "HEAD", "refs/heads/main")
        command("chown", "-R", name + ":" + name, str(remote))

        def request(op, **fields):
            c = UnixHTTP(sock)
            c.request("POST", "/v1/operations", json.dumps({"version":"superbench.v1", "operation":op, **fields}), {"Content-Type":"application/json"})
            r = c.getresponse(); result = json.loads(r.read()); c.close()
            if r.status != 200:
                # Fixture-only diagnostics contain no repository credentials.
                cgpath = Path("/sys/fs/cgroup") / command("systemctl", "show", "-p", "ControlGroup", "--value", unit.name).stdout.decode().strip().lstrip("/")
                print("cgroup mount:", [line for line in Path(f"/proc/{pid}/mountinfo").read_text().splitlines() if " - cgroup2 " in line], flush=True)
                print("cgroup ownership:", [(str(path), path.stat().st_uid, oct(path.stat().st_mode & 0o777)) for path in [cgpath, cgpath / "cgroup.procs", cgpath / "cgroup.subtree_control"]], flush=True)
                print("cgroup controllers:", (cgpath / "cgroup.subtree_control").read_text(), flush=True)
                print("retained execution:", result.get("result", {}).get("execution"), flush=True)
                # Retry a different bench directly as the same uid/cgroup to
                # expose its local operator error, without public API disclosure.
                if op == "run":
                    request("create", key="diagnostic", repository=str(remote), request_id="diagnostic-create")
                    diagnostic = subprocess.run(["nsenter", "-t", str(pid), "-m", "--", "sh", "-c", 'echo $$ > "$1/cgroup.procs"; shift; exec "$@"', "diagnostic", str(cgpath), "runuser", "-u", name, "--", str(image / "usr/local/bin/bench"), "run", "--state", str(state / "state"), "--data", str(data), "--cgroup-root", str(cgpath), "--holder", "diagnostic", "diagnostic", "--", "true"], capture_output=True, text=True)
                    print("local diagnostic:", diagnostic.stderr, flush=True)
                raise AssertionError((r.status, result.get("error")))
            return result["result"]
        request("create", key="task", repository=str(remote), request_id="create")
        request("run", key="task", holder="service-test", request_id="run", arguments=["sh", "-c", 'test "$(cat .setup-events)" = onceeach'])
        assert "lease" not in request("status", key="task")["benches"][0]
        print("PASS: service delegation launches, fences, checkpoints and releases a Linux run", flush=True)
        print("PASS: unprivileged service fences preparation before launching the requested command", flush=True)
        # Fail closed if any unit hardening silently prevents the narrow sudo path.
        check = f"test \"$(cat .setup-events)\" = onceeacheach || exit 9; sudo -n {helper} --policy /invalid apply bench task 2>quota-error; code=$?; test $code -ne 0 && grep -q 'policy override requires a direct root caller' quota-error && rm quota-error"
        request("run", key="task", holder="helper-test", request_id="helper", arguments=["sh", "-c", check])
        print("PASS: fixed helper sudo works inside service; policy override remains forbidden", flush=True)
        service_cgroup = Path("/sys/fs/cgroup") / command("systemctl", "show", "-p", "ControlGroup", "--value", unit.name).stdout.decode().strip().lstrip("/")
        command("systemctl", "stop", unit.name)
        assert not sock.exists()
        assert not service_cgroup.exists()
        print("PASS: stopping service removes its socket and delegated execution tree", flush=True)
        print("DONE: service installation integration passed", flush=True)
    except Exception:
        if unit.exists():
            log = subprocess.run(["journalctl", "-u", unit.name, "-n", "30", "--no-pager"], capture_output=True, text=True)
            print(log.stdout, flush=True)
        raise
    finally:
        subprocess.run(["systemctl", "stop", unit.name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        unit.unlink(missing_ok=True)
        sudoers.unlink(missing_ok=True)
        subprocess.run(["systemctl", "daemon-reload"], check=False)
        subprocess.run(["systemctl", "reset-failed", unit.name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if created_user:
            command("userdel", name)
        for path in [base, state, runtime]:
            if path.exists():
                shutil.rmtree(path)


if __name__ == "__main__":
    main()
