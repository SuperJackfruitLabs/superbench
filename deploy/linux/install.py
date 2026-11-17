#!/usr/bin/env python3
"""Install a reviewed Linux payload without starting the service or replacing config.

Use --root /absolute/staging to inspect a package image first. For a fresh host,
--root / --prepare creates its accounts/directories and reloads systemd. On the
live root, --identity then writes the payload policy and service identity derived
from those accounts, --activation writes the all-false activation file, and
--storage writes the indexed storage policy and initializes its empty ledger once.
Starting or enabling the service is a separate operator action after configuration.
"""
import argparse
import grp
import hashlib
import json
import os
from pathlib import Path
import platform
import pwd
import re
import stat
import struct
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


def static_helper(data):
    """Reject a loader or shared-library dependency in the root payload helper."""
    if len(data) < 64:
        raise ValueError("bench-payload requires a complete static ELF64 header")
    offset = struct.unpack_from("<Q", data, 32)[0]
    size, count = struct.unpack_from("<HH", data, 54)
    if size != 56 or count == 0 or offset < 64 or offset + size * count > len(data):
        raise ValueError("bench-payload has invalid ELF program headers")
    for index in range(count):
        kind, _, start, _, _, length, _, _ = struct.unpack_from("<IIQQQQQQ", data, offset + index * size)
        if start + length > len(data):
            raise ValueError("bench-payload has an invalid ELF segment")
        if kind == 3:
            raise ValueError("bench-payload must not use a dynamic interpreter")
        if kind == 2:
            if length % 16:
                raise ValueError("bench-payload has an invalid dynamic section")
            for position in range(start, start + length, 16):
                tag, _ = struct.unpack_from("<qQ", data, position)
                if tag == 1:
                    raise ValueError("bench-payload must not depend on shared libraries")
                if tag == 0:
                    break


DAEMON_ACCOUNT = "superbench"
PAYLOAD_ACCOUNT = "superbench-payload"
DATA_ROOT = "/srv/superbench-data"
STATE_ROOT = "/var/lib/superbench/state"
# superbench-start passes the unit's delegated cgroup; this is its standard path.
CGROUP_ROOT = "/sys/fs/cgroup/system.slice/superbench.service"
NOLOGIN = ("/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false")
POLICY_FILE = "etc/superbench/payload-policy.json"
REGISTRY_FILE = "etc/superbench/service-identity.json"
ACTIVATION_FILE = "etc/superbench/activation.json"
ACTIVATION_SWITCHES = ("fresh_payload", "finite_io", "receipt_lookup", "cancellation",
                       "registration_observation")
STORAGE_FILE = "etc/superbench/storage.json"
UNIT_FILE = "etc/systemd/system/superbench.service"
QUOTA_HELPER = "/usr/local/libexec/bench-quota"
# The indexed journal needs a private root-owned ledger parent; tmpfiles creates
# /var/lib/superbench-quota 0700 root, and initialize-indexed creates the ledger.
QUOTA_PARENT = "/var/lib/superbench-quota"
LEDGER = QUOTA_PARENT + "/ledger"
BYTE_BUDGETS = ("BenchBytes", "StoreBytes", "MirrorBytes", "ReserveBytes")
INODE_BUDGETS = ("BenchInodes", "StoreInodes", "MirrorInodes", "ReserveInodes")
LEGACY_STORAGE = ("in-place conversion of a legacy storage policy or ledger to the indexed "
                  "operation journal is not supported; see docs/pilot-installation.md")
# Conservative path syntax: Go and Python encode these bytes identically in JSON.
POLICY_PATH = re.compile(r"/[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*")


def canonical(value):
    return json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()


def identity_documents(daemon, payload_ids, cgroup_root=CGROUP_ROOT):
    """Return the private payload policy and public service identity entries.

    The bytes are the canonical forms internal/payloadpolicy and
    internal/serviceidentity accept: payloadpolicy.Policy fields in declaration
    order, and a registry whose PolicySHA256 hashes those exact policy bytes.
    """
    ids = [*daemon, *payload_ids]
    if len(ids) != 4 or any(type(value) is not int or not 0 < value < 2**32 - 1 for value in ids):
        raise ValueError("payload policy needs four nonzero numeric account IDs")
    if daemon[0] == payload_ids[0] or daemon[1] == payload_ids[1]:
        raise ValueError("the payload account must not share the daemon's user or group")
    for path in (DATA_ROOT, STATE_ROOT, cgroup_root):
        if (not isinstance(path, str) or len(path) > 4096 or not POLICY_PATH.fullmatch(path)
                or os.path.normpath(path) != path):
            raise ValueError("payload policy roots must be normalized absolute paths: " + repr(path))
    policy = {"DaemonUID": daemon[0], "DaemonGID": daemon[1], "PayloadUID": payload_ids[0],
              "PayloadGID": payload_ids[1], "DataRoot": DATA_ROOT, "StateRoot": STATE_ROOT,
              "CgroupRoot": cgroup_root}
    private = canonical(policy)
    registry = {"Version": "superbench.service-identity.v1",
                "LayoutVersion": "superbench.payload-layout.v1", "Policy": policy,
                "PolicySHA256": hashlib.sha256(private).hexdigest()}
    return {POLICY_FILE: (private, 0o600), REGISTRY_FILE: (canonical(registry), 0o644)}


def activation_document():
    """The default-off activation file benchd reads (internal/activation)."""
    lines = ['  "version": "superbench.activation.v1"']
    lines += ['  "%s": false' % name for name in ACTIVATION_SWITCHES]
    return {ACTIVATION_FILE: (("{\n" + ",\n".join(lines) + "\n}\n").encode(), 0o644)}


def read_budgets(path):
    """The operator's reviewed byte and inode budgets, checked as hoststorage.Policy.Validate does."""
    raw = Path(path).read_bytes()
    if len(raw) > 64 << 10:
        raise ValueError("storage budgets exceed 64 KiB")
    document = json.loads(raw, object_pairs_hook=lambda pairs: ("object", pairs))
    if not isinstance(document, tuple) or len(dict(document[1])) != len(document[1]):
        raise ValueError("storage budgets must be one JSON object without duplicate keys")
    budgets = dict(document[1])
    if sorted(budgets) != sorted(BYTE_BUDGETS + INODE_BUDGETS):
        raise ValueError("storage budgets need exactly: " + ", ".join(BYTE_BUDGETS + INODE_BUDGETS))
    for name, value in budgets.items():
        if type(value) is not int:
            raise ValueError("storage budget must be an integer: " + name)
        if name in BYTE_BUDGETS and not (1024 <= value <= 1 << 50 and value % 1024 == 0):
            raise ValueError("byte budgets must be positive whole KiB, at most 1 PiB: " + name)
        if name in INODE_BUDGETS and not 0 < value <= 1 << 40:
            raise ValueError("inode budgets must be explicit positive counts, at most 2^40: " + name)
    return budgets


def storage_document(daemon, budgets):
    """The root-owned /etc/superbench/storage.json selecting the indexed journal.

    Field names are hoststorage.Policy's; its strict decoder refuses any other.
    """
    policy = {"OperationJournal": "indexed-v2", "Root": DATA_ROOT, "Ledger": LEDGER,
              "OwnerUID": daemon[0], "OwnerGID": daemon[1]}
    policy.update((name, budgets[name]) for name in BYTE_BUDGETS + INODE_BUDGETS)
    return {STORAGE_FILE: ((json.dumps(policy, indent=2) + "\n").encode(), 0o644)}


def storage_state(root):
    """Return "fresh" when the indexed ledger still has to be initialized, or
    "initialized" when an earlier run finished it. A legacy policy or ledger, or
    a partial initialization, refuses: the code cannot convert or adopt either.
    """
    policy = checked_path(root, STORAGE_FILE)
    parent = checked_path(root, QUOTA_PARENT[1:])
    names = set(os.listdir(parent)) if parent.is_dir() else set()
    if os.path.lexists(policy):
        try:
            indexed = json.loads(policy.read_bytes()).get("OperationJournal") == "indexed-v2"
        except (ValueError, AttributeError):
            indexed = False
        if not indexed:
            raise ValueError("existing " + str(policy) + " does not select the indexed journal; " + LEGACY_STORAGE)
    if names - {"ledger", "ledger.format-v2"}:
        raise ValueError("existing quota ledger in " + str(parent) + "; " + LEGACY_STORAGE)
    present = names & {"ledger", "ledger.format-v2"}
    if not present:
        return "fresh"
    if len(present) == 2 and os.path.lexists(policy):
        return "initialized"
    raise ValueError("partial indexed ledger initialization retained in " + str(parent) +
                     "; it is never adopted or retried. See docs/pilot-installation.md")


def masked_unit(root):
    """The unit path, and whether it is the /dev/null mask initialization uses."""
    path = checked_path(root, os.path.dirname(UNIT_FILE)) / os.path.basename(UNIT_FILE)
    return path, path.is_symlink() and os.readlink(path) == "/dev/null"


def check_data_mount(path=DATA_ROOT, mountinfo="/proc/self/mountinfo", stat=os.stat):
    """Refuse unless path is the root of a mounted XFS filesystem enforcing project quotas.

    Without the mount, state and the ledger would describe the root filesystem's
    directory, and the volume mounted over it later would not match them.
    """
    hint = "; mount the data volume (persistently, see docs/pilot-installation.md) first"
    try:
        separate = stat(path).st_dev != stat(os.path.dirname(path)).st_dev
    except FileNotFoundError:
        separate = False
    if not separate:
        raise ValueError(path + " is not a mounted filesystem" + hint)
    found = None
    for line in Path(mountinfo).read_text().splitlines():
        mount, _, source = line.partition(" - ")
        fields, tail = mount.split(), source.split()
        if len(fields) >= 5 and fields[4] == path and len(tail) >= 3:
            found = fields[3], tail[0], tail[2].split(",")  # the last mount is the visible one
    if found is None or found[0] != "/" or found[1] != "xfs":
        raise ValueError(path + " must be the root of a mounted XFS filesystem" + hint)
    if not {"prjquota", "pquota"} & set(found[2]) or "pqnoenforce" in found[2]:
        raise ValueError(path + " must be mounted with enforced project quotas (prjquota)" + hint)


UNIT_INSTALLED = ("the service unit is already installed; the indexed ledger can only be "
                  "initialized during a first install. See docs/pilot-installation.md")


def initialize_storage(root, run):
    """Run the direct-root, first-install-only bench-quota initialize-indexed.

    Its installation gate requires the service to be masked and stopped, so the
    unit file is installed only after this returns. On failure the service stays
    masked, without its unit file, and nothing is retried or removed.
    """
    mask, masked = masked_unit(root)
    if not masked:
        if os.path.lexists(mask):
            raise ValueError(UNIT_INSTALLED)
        make_parents(mask.parent)
        os.symlink("/dev/null", mask)
    run(["systemctl", "daemon-reload"], check=True)
    try:
        run([QUOTA_HELPER, "initialize-indexed"], check=True)
    except subprocess.CalledProcessError:
        raise ValueError("bench-quota initialize-indexed refused or failed; the service stays masked "
                         "without its unit file. See docs/pilot-installation.md") from None


class MissingAccount(ValueError):
    """The account or its group does not exist yet; --prepare creates it."""


def account_ids(name, getpwnam=pwd.getpwnam, getgrnam=grp.getgrnam, getpwall=pwd.getpwall):
    """Numeric IDs of a product system account that sysusers created."""
    try:
        user, group = getpwnam(name), getgrnam(name)
    except KeyError:
        raise MissingAccount("account or group is missing: " + name) from None
    if user.pw_gid != group.gr_gid or user.pw_uid == 0 or user.pw_shell not in NOLOGIN:
        raise ValueError("refusing a foreign account (expected a nologin system account "
                         "with its own group): " + name)
    # Socket clients join the daemon's group; nobody may share the payload's,
    # as a supplementary member or through another account's primary group.
    if name == PAYLOAD_ACCOUNT:
        if group.gr_mem:
            raise ValueError("the payload group must have no members: " + name)
        others = sorted(u.pw_name for u in getpwall() if u.pw_gid == group.gr_gid and u.pw_name != name)
        if others:
            raise ValueError("the payload group must be no other account's primary group: " + ", ".join(others))
    return user.pw_uid, user.pw_gid


def installed_identity(cgroup_root, lookup=account_ids):
    return identity_documents(lookup(DAEMON_ACCOUNT), lookup(PAYLOAD_ACCOUNT), cgroup_root)


def preflight_identity(root, prepare, lookup=account_ids):
    """Return True when the identity must wait for --prepare to create accounts.

    A foreign existing account refuses here, before any effect, even with
    --prepare: sysusers would keep it. Identity documents without both accounts
    belong to someone else; refuse them rather than overwrite or adopt them.
    """
    missing = []
    for name in (DAEMON_ACCOUNT, PAYLOAD_ACCOUNT):
        try:
            lookup(name)
        except MissingAccount as error:
            missing.append(error)
    if not missing:
        return False
    if not prepare:
        raise missing[0]
    for relative in (POLICY_FILE, REGISTRY_FILE):
        if os.path.lexists(checked_path(root, relative)):
            raise ValueError("identity document exists without its accounts: " + relative)
    return True


def payload(bin_dir, managed):
    here = Path(__file__).resolve().parent
    result = {}
    architecture = None
    executables = [
        ("bench", "usr/local/bin/bench"),
        ("benchd", "usr/local/bin/benchd"),
        ("bench-quota", "usr/local/libexec/bench-quota"),
    ]
    if managed:
        executables.append(("bench-payload", "usr/local/libexec/bench-payload"))
    for name, target in executables:
        source = bin_dir / name
        data = source.read_bytes()
        if data[:4] != b"\x7fELF" or len(data) < 20 or data[4:6] != b"\x02\x01":
            raise ValueError("expected Linux 64-bit little-endian ELF: " + name)
        machine = int.from_bytes(data[18:20], "little")
        if machine not in (62, 183) or (architecture is not None and machine != architecture):
            raise ValueError("payload must use one supported architecture (amd64 or arm64)")
        architecture = machine
        if name == "bench-payload":
            static_helper(data)
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
        result["usr/lib/sysusers.d/superbench-payload.conf"] = (
            (here / "superbench-payload.sysusers").read_bytes(), 0o644)
    return result


def make_parents(directory):
    """Create missing directories 0755 whatever the caller's umask.

    benchd runs as its own account: it must search /etc/superbench and refuses a
    group- or world-writable one that holds the activation file.
    """
    missing = []
    while not os.path.lexists(directory):
        missing.append(directory)
        directory = directory.parent
    for path in reversed(missing):
        try:
            os.mkdir(path, 0o755)
        except FileExistsError:
            continue
        os.chmod(path, 0o755)


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
        make_parents(path.parent)
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


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--managed", action="store_true", help="keep data root owned; install fixed helper sudo rule")
    parser.add_argument("--prepare", action="store_true", help="live root only: prepare accounts/directories; never start service")
    parser.add_argument("--identity", action="store_true",
                        help="live managed root only: write payload-policy.json and service-identity.json from the accounts")
    parser.add_argument("--cgroup-root", default=CGROUP_ROOT,
                        help="the unit's delegated cgroup, as superbench-start passes it to benchd")
    parser.add_argument("--activation", action="store_true",
                        help="write /etc/superbench/activation.json with every switch false")
    parser.add_argument("--storage", type=Path, metavar="BUDGETS_JSON",
                        help="live managed root with --identity only: write the indexed storage.json from "
                             "these reviewed budgets and initialize its empty ledger once")
    args = parser.parse_args(argv)
    if not args.root.is_absolute() or str(args.root) != os.path.normpath(str(args.root)) or args.root != args.root.resolve():
        parser.error("installation root must be absolute, normalized and use physical paths")
    if args.prepare and args.root != Path("/"):
        parser.error("--prepare applies only to a live / root")
    if args.identity and (args.root != Path("/") or not args.managed):
        # The documents carry numeric IDs from this host's account database.
        parser.error("--identity applies only to a live / root with --managed")
    if args.storage is not None and not args.identity:
        parser.error("--storage applies only with --identity on a live / root with --managed")
    if args.storage is not None and "SUDO_UID" in os.environ:
        # bench-quota initialize-indexed refuses sudo callers; never launder that.
        parser.error("--storage needs a direct root login, not sudo: bench-quota initialize-indexed "
                     "refuses a caller with SUDO_UID set")
    if args.root == Path("/"):
        if platform.system() != "Linux" or os.geteuid() != 0:
            parser.error("live installation requires Linux root")
        active = subprocess.run(["systemctl", "is-active", "--quiet", "superbench.service"])
        if active.returncode == 0:
            parser.error("stop the existing service before changing installed files")
        if active.returncode not in (3, 4):
            parser.error("cannot establish the existing systemd service state")
    apply(args)
    print("Installed payload; service has not been started or enabled.")


def apply(args, run=subprocess.run, lookup=account_ids, mounted=check_data_mount):
    """Install what the checked arguments ask for, preparing the host if asked."""
    entries = payload(args.bin_dir, args.managed)
    if args.activation:
        entries.update(activation_document())
    storage = getattr(args, "storage", None)
    unit = None
    if storage is not None:
        budgets = read_budgets(storage)
        state = storage_state(args.root)
        data = checked_path(args.root, DATA_ROOT[1:])
        mounted(str(data))
        if state == "fresh" and data.is_dir() and os.listdir(data):
            raise ValueError(str(data) + " must be empty for a first install of the indexed ledger")
        mask, masked = masked_unit(args.root)
        if state == "fresh" and os.path.lexists(mask) and not masked:
            raise ValueError(UNIT_INSTALLED)
        # A masked unit is an earlier initialization this run must finish.
        if state == "fresh" or masked:
            unit = entries.pop(UNIT_FILE)
    deferred = False
    if args.identity:
        identity_documents((1, 2), (3, 4), args.cgroup_root)  # refuse a bad cgroup root first
        deferred = preflight_identity(args.root, args.prepare, lookup)
        if not deferred:
            entries.update(installed_identity(args.cgroup_root, lookup))
    # Validate the exact sudo policy before installing any live/staged files.
    if args.managed and platform.system() == "Linux":
        source = Path(__file__).resolve().parent / "superbench.sudoers"
        run(["visudo", "-cf", str(source)], check=True)
    install(args.root, entries)
    if args.prepare:
        sysusers = ["/usr/lib/sysusers.d/superbench.conf"]
        if args.managed:
            sysusers.append("/usr/lib/sysusers.d/superbench-payload.conf")
        run(["systemd-sysusers", *sysusers], check=True)
        run(["systemd-tmpfiles", "--create", "/usr/lib/tmpfiles.d/superbench.conf"], check=True)
        run(["systemctl", "daemon-reload"], check=True)
    if deferred:
        install(args.root, installed_identity(args.cgroup_root, lookup))
    if storage is not None:
        install(args.root, storage_document(lookup(DAEMON_ACCOUNT), budgets))
        if state == "fresh":
            initialize_storage(args.root, run)
        if unit is not None:
            mask, masked = masked_unit(args.root)
            if masked:
                os.unlink(mask)
            install(args.root, {UNIT_FILE: unit})
        run(["systemctl", "daemon-reload"], check=True)


if __name__ == "__main__":
    main()
