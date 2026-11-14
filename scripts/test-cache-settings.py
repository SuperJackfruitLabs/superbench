#!/usr/bin/env python3
"""Verify repository cache policy through real Linux setup and execution."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import tempfile
import uuid

spec = importlib.util.spec_from_file_location("setup_support", Path(__file__).with_name("test-repository-setup.py"))
support = importlib.util.module_from_spec(spec)
spec.loader.exec_module(support)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bench", required=True)
    parser.add_argument("--benchd", required=True)
    parser.add_argument("--old-bench", required=True, help="published pre-cache-settings CLI")
    args = parser.parse_args()
    if os.geteuid() != 0:
        raise SystemExit("disposable Linux root required")
    root = Path(tempfile.mkdtemp(prefix="sb-cache-settings-", dir="/var/tmp"))
    state, data, sock = root / "state", root / "data", root / "api.sock"
    cg = Path("/sys/fs/cgroup") / ("sb-cache-settings-" + uuid.uuid4().hex)
    cg.mkdir()
    daemon = None
    log = (root / "daemon.log").open("wb")
    outside = root / "outside-cache"
    outside.mkdir()
    environment = dict(os.environ, NPM_CONFIG_CACHE=str(outside),
                       NPM_CONFIG_STORE_DIR=str(outside), npm_config_cache=str(outside),
                       npm_config_package_import_method="hardlink",
                       PLAYWRIGHT_BROWSERS_PATH=str(outside))

    def run(words):
        return subprocess.run(list(map(str, words)), capture_output=True, text=True,
                              timeout=45, env=environment)

    def good(words):
        r = run(words)
        assert r.returncode == 0, (words, r.stderr)
        return r.stdout

    def command(op, *words):
        return [args.bench, op, "--state", state, "--data", data, "--cgroup-root", cg, *words]

    def cli(op, *words):
        return json.loads(good(command(op, *words)))

    def config(shared):
        return "[setup]\nonce='python3 probe.py once'\neach='python3 probe.py each'\n[caches]\n" + "".join(name + "=" + str(shared).lower() + "\n" for name in ("pnpm", "npm", "bun", "playwright", "go"))

    def events(key):
        return [json.loads(line) for line in (data / "benches" / key / ".cache-events").read_text().splitlines()]

    def check(key, shared):
        base = data / "store" if shared else data / "benches" / key / ".bench/cache"
        expected = {"npm_config_store_dir": base / "pnpm", "npm_config_cache": base / "npm",
                    "BUN_INSTALL_CACHE_DIR": base / "bun", "PLAYWRIGHT_BROWSERS_PATH": base / "playwright",
                    "GOCACHE": base / "go-build", "GOMODCACHE": base / "go-mod"}
        latest = events(key)[-1]
        assert latest["paths"] == {name: str(path) for name, path in expected.items()}, latest
        assert latest["import_method"] == "clone"
        assert Path(latest["pnpm_store"]).is_relative_to(base / "pnpm"), latest
        return latest

    try:
        seed, remote = root / "seed", root / "remote.git"
        good(["git", "init", "--bare", remote])
        good(["git", "init", "-b", "main", seed])
        (seed / ".bench").mkdir()
        (seed / ".gitignore").write_text(".cache-events\n.bench/cache/\n")
        (seed / ".bench/config.toml").write_text(config(True))
        (seed / "probe.py").write_text('''import json,os,subprocess,sys
from pathlib import Path
names=['npm_config_store_dir','npm_config_cache','BUN_INSTALL_CACHE_DIR','PLAYWRIGHT_BROWSERS_PATH','GOCACHE','GOMODCACHE']
paths={name:os.environ[name] for name in names}
assert 'BENCH_LEASE_TOKEN' not in os.environ
store=subprocess.check_output(['pnpm','store','path'],text=True).strip()
method=subprocess.check_output(['pnpm','config','get','package-import-method'],text=True).strip()
marker=Path(paths['PLAYWRIGHT_BROWSERS_PATH'])/'cache-probe'
previous=marker.read_text() if marker.exists() else None
marker.write_text(str(Path.cwd()))
record={'phase':sys.argv[1],'paths':paths,'pnpm_store':store,'import_method':method,'previous':previous}
with Path('.cache-events').open('a') as out:out.write(json.dumps(record)+'\\n')
''')
        good(["git", "-C", seed, "add", "."])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "cache fixture"])
        good(["git", "-C", seed, "remote", "add", "origin", remote])
        good(["git", "-C", seed, "push", "origin", "main"])
        good(["git", "-C", remote, "symbolic-ref", "HEAD", "refs/heads/main"])

        def publish(shared, ignored=True):
            (seed / ".bench/config.toml").write_text(config(shared))
            (seed / ".gitignore").write_text(".cache-events\n" + (".bench/cache/\n" if ignored else ""))
            good(["git", "-C", seed, "add", "."])
            good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "cache policy"])
            good(["git", "-C", seed, "push", "origin", "main"])

        cli("create", "--repo", remote, "shared-one")
        check("shared-one", True)
        assert [event["phase"] for event in events("shared-one")] == ["once", "each"]
        cli("create", "--repo", remote, "shared-two")
        assert events("shared-two")[0]["previous"] == str(data / "benches/shared-one")
        check("shared-two", True)
        assert not list(outside.iterdir()), "inherited cache overrides escaped controlled roots"
        print("PASS: warm creation routes actual pnpm hooks and all selected cache paths to shared storage, reuses cache state across benches and overrides inherited aliases", flush=True)

        publish(False)
        cli("create", "--repo", remote, "private-one")
        cli("create", "--repo", remote, "private-two")
        check("private-one", False)
        check("private-two", False)
        assert events("private-one")[0]["previous"] is None and events("private-two")[0]["previous"] is None
        daemon = subprocess.Popen([args.benchd, "--state", str(state), "--data", str(data),
                                   "--socket", str(sock), "--cgroup-root", str(cg)],
                                  env=environment, stdout=log, stderr=log)

        def ready():
            assert daemon.poll() is None
            try:
                c = support.UnixHTTP(sock)
                c.request("GET", "/healthz")
                r = c.getresponse(); r.read(); c.close()
                return r.status == 200
            except OSError:
                return False
        support.wait_for(ready, "cache daemon ready")
        good([args.bench, "run", "--socket", sock, "--request-id", "cache-main", "--holder", "main", "private-one", "--", "python3", "probe.py", "main"])
        assert [event["phase"] for event in events("private-one")] == ["once", "each", "each", "main"]
        check("private-one", False)
        held = cli("lease", "--holder", "cooperative", "private-one")["Lease"]
        check("private-one", False)
        cli("checkpoint", "--token", held["Token"], "private-one")
        cli("release", "--token", held["Token"], "private-one")
        print("PASS: private caches stay inside each checkout and persist through daemon run/setup stages and cooperative acquisition without repeating once", flush=True)

        held = cli("lease-repair", "--holder", "configure", "private-one")["Lease"]
        path = data / "benches/private-one"
        (path / ".bench/config.toml").write_text(config(True))
        good(["git", "-C", path, "add", ".bench/config.toml"])
        good(["git", "-C", path, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "share caches"])
        cli("checkpoint", "--token", held["Token"], "private-one")
        cli("release", "--token", held["Token"], "private-one")
        cli("run", "--holder", "reconfigured", "private-one", "--", "python3", "probe.py", "main")
        check("private-one", True)
        assert sum(event["phase"] == "once" for event in events("private-one")) == 2
        assert (path / ".bench/cache/playwright/cache-probe").exists(), "old private cache was deleted"
        with sqlite3.connect(state / "bench.db") as db:
            assert db.execute("SELECT count(*) FROM setup_receipts WHERE bench='private-one'").fetchone()[0] == 1
        print("PASS: changing cache policy reruns once under the new configuration and preserves previous private cache files", flush=True)

        cli("create-cold", "--repo", remote, "linked")
        (data / "benches/linked/.bench/cache").symlink_to(outside, target_is_directory=True)
        failed = run(command("run", "--holder", "blocked", "linked", "--", "touch", root / "forbidden"))
        assert failed.returncode != 0 and not (root / "forbidden").exists()
        assert cli("history", "linked") == [] and not list(outside.iterdir())
        print("PASS: a linked private cache prefix refuses execution without following the target or starting the requested command", flush=True)

        tracked_cache = seed / ".bench/cache/pnpm/keep"
        tracked_cache.parent.mkdir(parents=True)
        tracked_cache.write_text("tracked source must survive")
        good(["git", "-C", seed, "add", "-f", ".bench/cache/pnpm/keep"])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "tracked namespace collision"])
        good(["git", "-C", seed, "push", "origin", "main"])
        cli("create-cold", "--repo", remote, "tracked-cache")
        refused = run(command("run", "--holder", "blocked", "tracked-cache", "--", "touch", root / "tracked-forbidden"))
        assert refused.returncode != 0 and not (root / "tracked-forbidden").exists()
        assert (data / "benches/tracked-cache/.bench/cache/pnpm/keep").read_text() == "tracked source must survive"
        assert cli("history", "tracked-cache") == []
        good(["git", "-C", seed, "rm", "--", ".bench/cache/pnpm/keep"])
        good(["git", "-C", seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "remove fixture collision"])
        good(["git", "-C", seed, "push", "origin", "main"])
        print("PASS: tracked files in the private cache namespace refuse execution and are preserved unchanged", flush=True)

        publish(False, ignored=False)
        failed = run(command("create", "--repo", remote, "not-ignored"))
        assert failed.returncode != 0
        held = json.loads(failed.stdout)["Lease"]
        assert held["Execution"]["Phase"] == "stopped"
        assert (data / "benches/not-ignored/.bench/cache/playwright/cache-probe").exists()
        assert (data / "benches/not-ignored/.gitignore").read_text() == ".cache-events\n"
        old = run([args.old_bench, "run", "--state", state, "--data", data, "--cgroup-root", cg,
                   "--holder", "old", "shared-one", "--", "touch", root / "old-forbidden"])
        assert old.returncode != 0 and not (root / "old-forbidden").exists()
        print("PASS: unignored private output retains a stopped lease and files; an older producer rejects unsupported cache configuration before command effects", flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            daemon.terminate()
            daemon.wait(timeout=15)
        (cg / "cgroup.kill").write_text("1")
        support.wait_for(lambda: "populated 0" in (cg / "cgroup.events").read_text(), "cache cgroup empty")
        for child in cg.iterdir():
            if child.is_dir(): child.rmdir()
        cg.rmdir()
        log.close()
        shutil.rmtree(root)


if __name__ == "__main__":
    main()
