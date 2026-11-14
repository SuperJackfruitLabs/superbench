#!/usr/bin/env python3
"""Install a reviewed Linux payload without starting the service or replacing config.

Use --root /absolute/staging to inspect a package image first. For a fresh host,
--root / --prepare creates its account/directories and reloads systemd. Starting
or enabling the service is a separate operator action after configuration.
"""
import argparse
import os
from pathlib import Path
import platform
import stat
import subprocess
import tempfile


def checked_path(root, relative):
    path = root / relative
    for current in [path, *path.parents]:
        if current.is_symlink():
            raise ValueError("refusing symlink in installation path: " + str(current))
        if not current.exists():
            continue
        st = current.stat()
        if current != path and not stat.S_ISDIR(st.st_mode):
            raise ValueError("non-directory installation parent: " + str(current))
        if current != path:
            # A sticky root-owned /tmp is safe for a private staging root. Other
            # writable ancestors would permit swapping a checked destination.
            if st.st_mode & 0o022 and not (st.st_uid == 0 and st.st_mode & stat.S_ISVTX):
                raise ValueError("writable installation parent: " + str(current))
            if st.st_uid not in (0, os.geteuid()):
                raise ValueError("untrusted installation parent owner: " + str(current))
    return path


def payload(bin_dir, managed):
    here = Path(__file__).resolve().parent
    result = {}
    architecture = None
    for name, target in [
        ("bench", "usr/local/bin/bench"),
        ("benchd", "usr/local/bin/benchd"),
        ("bench-quota", "usr/local/libexec/bench-quota"),
    ]:
        source = bin_dir / name
        data = source.read_bytes()
        if data[:4] != b"\x7fELF" or len(data) < 20 or data[4:6] != b"\x02\x01":
            raise ValueError("expected Linux 64-bit little-endian ELF: " + name)
        machine = int.from_bytes(data[18:20], "little")
        if machine not in (62, 183) or (architecture is not None and machine != architecture):
            raise ValueError("payload must use one supported architecture (amd64 or arm64)")
        architecture = machine
        result[target] = (data, 0o755)
    for source, target, mode in [
        ("superbench.service", "etc/systemd/system/superbench.service", 0o644),
        ("superbench-start", "usr/local/libexec/superbench-start", 0o755),
        ("benchd.env", "etc/superbench/benchd.env", 0o644),
        ("superbench.sysusers", "usr/lib/sysusers.d/superbench.conf", 0o644),
        ("superbench.tmpfiles", "usr/lib/tmpfiles.d/superbench.conf", 0o644),
    ]:
        result[target] = ((here / source).read_bytes(), mode)
    if managed:
        text = result["usr/lib/tmpfiles.d/superbench.conf"][0].replace(
            b"/srv/superbench-data 0700 superbench superbench",
            b"/srv/superbench-data 0755 root root",
        )
        result["usr/lib/tmpfiles.d/superbench.conf"] = (text, 0o644)
        result["etc/sudoers.d/superbench"] = ((here / "superbench.sudoers").read_bytes(), 0o440)
    return result


def install(root, entries):
    # Preflight every destination before writing any: conflicting config must
    # not leave a half-upgraded binary set. Upgrades require a reviewed procedure.
    for relative, (data, mode) in entries.items():
        path = checked_path(root, relative)
        if path.exists():
            st = path.stat()
            if not stat.S_ISREG(st.st_mode) or path.read_bytes() != data:
                raise ValueError("existing file differs; refusing replacement: " + str(path))
            if stat.S_IMODE(st.st_mode) != mode:
                raise ValueError("existing file has unexpected permissions: " + str(path))
            if os.geteuid() == 0 and st.st_uid != 0:
                raise ValueError("existing file is not root owned: " + str(path))
    for relative, (data, mode) in entries.items():
        path = checked_path(root, relative)
        if path.exists():
            continue
        path.parent.mkdir(parents=True, exist_ok=True)
        # Publish complete files without replacing anything created concurrently.
        fd, stage = tempfile.mkstemp(prefix=".superbench-install-", dir=path.parent)
        try:
            with os.fdopen(fd, "wb") as stream:
                stream.write(data)
                stream.flush()
                os.fchmod(stream.fileno(), mode)
                os.fsync(stream.fileno())
            os.link(stage, path)
            directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
        finally:
            os.unlink(stage)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--managed", action="store_true", help="keep data root owned; install fixed helper sudo rule")
    parser.add_argument("--prepare", action="store_true", help="live root only: prepare account/directories; never start service")
    args = parser.parse_args()
    if not args.root.is_absolute() or str(args.root) != os.path.normpath(str(args.root)) or args.root != args.root.resolve():
        parser.error("installation root must be absolute, normalized and use physical paths")
    if args.prepare and args.root != Path("/"):
        parser.error("--prepare applies only to a live / root")
    if args.root == Path("/"):
        if platform.system() != "Linux" or os.geteuid() != 0:
            parser.error("live installation requires Linux root")
        active = subprocess.run(["systemctl", "is-active", "--quiet", "superbench.service"])
        if active.returncode == 0:
            parser.error("stop the existing service before changing installed files")
        if active.returncode not in (3, 4):
            parser.error("cannot establish the existing systemd service state")
    entries = payload(args.bin_dir, args.managed)
    # Validate the exact sudo policy before installing any live/staged files.
    if args.managed and platform.system() == "Linux":
        source = Path(__file__).resolve().parent / "superbench.sudoers"
        subprocess.run(["visudo", "-cf", str(source)], check=True)
    install(args.root, entries)
    if args.prepare:
        subprocess.run(["systemd-sysusers", "/usr/lib/sysusers.d/superbench.conf"], check=True)
        subprocess.run(["systemd-tmpfiles", "--create", "/usr/lib/tmpfiles.d/superbench.conf"], check=True)
        subprocess.run(["systemctl", "daemon-reload"], check=True)
    print("Installed payload; service has not been started or enabled.")


if __name__ == "__main__":
    main()
