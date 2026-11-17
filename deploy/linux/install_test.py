import hashlib
import json
import os
from pathlib import Path
import tempfile
import struct
from types import SimpleNamespace
import unittest

from install import (ACTIVATION_FILE, ACTIVATION_SWITCHES, POLICY_FILE, REGISTRY_FILE, account_ids,
                     activation_document, checked_path, identity_documents, install, installed_identity,
                     payload, preflight_identity, static_helper)

# The exact canonical bytes internal/payloadpolicy and internal/serviceidentity
# accept for these IDs (cross-checked in Go by TestInstallerDocumentsAreCanonical).
POLICY = (b'{"DaemonUID":977,"DaemonGID":977,"PayloadUID":976,"PayloadGID":976,'
          b'"DataRoot":"/srv/superbench-data","StateRoot":"/var/lib/superbench/state",'
          b'"CgroupRoot":"/sys/fs/cgroup/system.slice/superbench.service"}')


def accounts(**overrides):
    users = {"superbench": SimpleNamespace(pw_uid=977, pw_gid=977, pw_shell="/usr/sbin/nologin"),
             "superbench-payload": SimpleNamespace(pw_uid=976, pw_gid=976, pw_shell="/usr/sbin/nologin")}
    groups = {"superbench": SimpleNamespace(gr_gid=977, gr_mem=["node"]),
              "superbench-payload": SimpleNamespace(gr_gid=976, gr_mem=[])}
    for name, value in overrides.items():
        kind, account = name.split("_", 1)
        table = users if kind == "user" else groups
        account = account.replace("_", "-")
        if value is None:
            del table[account]
        else:
            table[account] = value

    def lookup(name):
        return account_ids(name, getpwnam=lambda n: users[n], getgrnam=lambda n: groups[n])
    return lookup


def helper_elf(machine=62, kind=1, dynamic_tag=None):
    data = bytearray(136 if dynamic_tag is not None else 120)
    data[:6] = b"\x7fELF\x02\x01"
    struct.pack_into("<H", data, 18, machine)
    struct.pack_into("<Q", data, 32, 64)
    struct.pack_into("<HH", data, 54, 56, 1)
    struct.pack_into("<IIQQQQQQ", data, 64, kind, 0, 120, 0, 0,
                     16 if dynamic_tag is not None else 0, 0, 0)
    if dynamic_tag is not None:
        struct.pack_into("<qQ", data, 120, dynamic_tag, 0)
    return bytes(data)


class InstallationGuards(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sb-install-", dir="/tmp")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()

    def test_conflicting_config_prevents_any_binary_update(self):
        config = self.root / "etc/superbench/benchd.env"
        config.parent.mkdir(parents=True)
        config.write_bytes(b"reviewed operator configuration")
        with self.assertRaises(ValueError):
            install(self.root, {"usr/local/bin/benchd": (b"new", 0o755),
                                "etc/superbench/benchd.env": (b"other", 0o644)})
        self.assertFalse((self.root / "usr/local/bin/benchd").exists())
        self.assertEqual(config.read_bytes(), b"reviewed operator configuration")

    def test_created_directories_are_0755_under_any_umask(self):
        # 002 would make /etc/superbench group-writable; 077 would hide it from benchd.
        for umask in (0o002, 0o077):
            root = self.root / ("umask-%o" % umask)
            root.mkdir(mode=0o755)
            previous = os.umask(umask)
            try:
                install(root, {"etc/superbench/activation.json": (b"{}", 0o644)})
            finally:
                os.umask(previous)
            for directory in ["etc", "etc/superbench"]:
                self.assertEqual((root / directory).stat().st_mode & 0o7777, 0o755, directory)

    def test_symlink_and_writable_ancestor_cannot_redirect_installation(self):
        outside = self.root / "outside"
        outside.mkdir()
        (self.root / "usr").symlink_to(outside, target_is_directory=True)
        with self.assertRaises(ValueError):
            checked_path(self.root, "usr/local/bin/benchd")
        self.assertFalse(list(outside.iterdir()))
        (self.root / "usr").unlink()
        (self.root / "usr").mkdir(mode=0o777)
        (self.root / "usr").chmod(0o777)
        with self.assertRaises(ValueError):
            checked_path(self.root, "usr/local/bin/benchd")

    def test_complete_publication_is_idempotent_and_private_policy_stays_private(self):
        entries = {"etc/sudoers.d/superbench": (b"fixed helper only", 0o440)}
        install(self.root, entries)
        install(self.root, entries)
        policy = self.root / "etc/sudoers.d/superbench"
        self.assertEqual(policy.read_bytes(), b"fixed helper only")
        self.assertEqual(policy.stat().st_mode & 0o777, 0o440)
        policy.chmod(0o666)
        with self.assertRaises(ValueError):
            install(self.root, entries)

    def test_managed_payload_preserves_root_data_parent_and_fixed_helper(self):
        binaries = self.root / "bin"
        binaries.mkdir()
        for name in ["bench", "benchd", "bench-quota"]:
            (binaries / name).write_bytes(b"\x7fELF\x02\x01" + bytes(12) + bytes([62, 0]))
        helper = binaries / "bench-payload"
        helper.write_bytes(helper_elf())
        unmanaged = payload(binaries, False)
        managed = payload(binaries, True)
        self.assertNotIn("etc/sudoers.d/superbench", unmanaged)
        self.assertNotIn("usr/local/libexec/bench-payload", unmanaged)
        self.assertEqual(managed["usr/local/libexec/bench-payload"], (helper.read_bytes(), 0o755))
        self.assertIn(b"/srv/superbench-data 0755 root root", managed["usr/lib/tmpfiles.d/superbench.conf"][0])
        rule = managed["etc/sudoers.d/superbench"][0]
        self.assertIn(b"NOPASSWD: /usr/local/libexec/bench-quota", rule)
        self.assertNotIn(b"/bin/sh", rule)
        self.assertIn(b'/usr/local/libexec/bench-payload ""\n', rule)
        # Payload framing requires real pipes; disable PTYs for this command only.
        defaults = [line for line in rule.splitlines() if line.startswith(b"Defaults")]
        self.assertEqual(defaults, [b"Defaults!/usr/local/libexec/bench-payload !use_pty"])
        self.assertNotIn(b"SETENV", rule)
        self.assertNotIn(b"closefrom_override", rule)
        helper.unlink()
        with self.assertRaises(FileNotFoundError):
            payload(binaries, True)
        helper.write_bytes(helper_elf(machine=183))
        with self.assertRaisesRegex(ValueError, "one supported architecture"):
            payload(binaries, True)
        helper.write_bytes(helper_elf())
        (binaries / "benchd").write_bytes(b"\x7fELF\x02\x01" + bytes(12) + bytes([183, 0]))
        with self.assertRaises(ValueError):
            payload(binaries, True)
        (binaries / "benchd").write_bytes(b"not a Linux binary")
        with self.assertRaises(ValueError):
            payload(binaries, True)

    def test_payload_helper_refuses_dynamic_and_malformed_binaries(self):
        static_helper(helper_elf())
        static_helper(helper_elf(machine=183))
        with self.assertRaisesRegex(ValueError, "dynamic interpreter"):
            static_helper(helper_elf(kind=3))
        with self.assertRaisesRegex(ValueError, "shared libraries"):
            static_helper(helper_elf(kind=2, dynamic_tag=1))
        with self.assertRaisesRegex(ValueError, "complete static ELF64"):
            static_helper(b"\x7fELF")
        bad = bytearray(helper_elf())
        struct.pack_into("<Q", bad, 32, 1 << 63)
        with self.assertRaisesRegex(ValueError, "invalid ELF program headers"):
            static_helper(bad)


class PayloadIdentity(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sb-identity-", dir="/tmp")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()

    def test_documents_are_the_canonical_policy_and_its_registry(self):
        documents = installed_identity("/sys/fs/cgroup/system.slice/superbench.service", lookup=accounts())
        self.assertEqual(documents[POLICY_FILE], (POLICY, 0o600))
        registry, mode = documents[REGISTRY_FILE]
        self.assertEqual(mode, 0o644)
        self.assertEqual(registry, b'{"Version":"superbench.service-identity.v1",'
                         b'"LayoutVersion":"superbench.payload-layout.v1","Policy":' + POLICY +
                         b',"PolicySHA256":"' + hashlib.sha256(POLICY).hexdigest().encode() + b'"}')

    def test_documents_refuse_what_the_go_policy_refuses(self):
        for daemon, payload_ids in [((0, 977), (976, 976)), ((977, 977), (977, 976)),
                                    ((977, 977), (976, 977)), ((977, 977), (2**32 - 1, 976)),
                                    ((977, "977"), (976, 976)), ((977, True), (976, 976))]:
            with self.assertRaises(ValueError):
                identity_documents(daemon, payload_ids)
        for cgroup in ["", "/", "relative", "/sys/fs/cgroup/../x", "/sys/fs/cgroup/", "/a b", "/aé",
                       "/sys/fs/cgroup//x", "/" + "a" * 4096]:
            with self.assertRaises(ValueError):
                identity_documents((977, 977), (976, 976), cgroup)

    def test_accounts_must_be_our_nologin_system_accounts(self):
        self.assertEqual(accounts()("superbench"), (977, 977))
        self.assertEqual(accounts()("superbench-payload"), (976, 976))
        for overrides in [{"user_superbench_payload": None}, {"group_superbench_payload": None},
                          {"user_superbench_payload": SimpleNamespace(pw_uid=976, pw_gid=976, pw_shell="/bin/bash")},
                          {"user_superbench_payload": SimpleNamespace(pw_uid=976, pw_gid=100, pw_shell="/usr/sbin/nologin")},
                          {"user_superbench_payload": SimpleNamespace(pw_uid=0, pw_gid=976, pw_shell="/usr/sbin/nologin")},
                          {"group_superbench_payload": SimpleNamespace(gr_gid=976, gr_mem=["node"])},
                          {"user_superbench": SimpleNamespace(pw_uid=977, pw_gid=977, pw_shell="/bin/sh")}]:
            with self.assertRaises(ValueError, msg=str(overrides)):
                installed_identity("/sys/fs/cgroup/x", lookup=accounts(**overrides))

    def test_identity_is_idempotent_and_never_overwrites_a_changed_document(self):
        documents = installed_identity("/sys/fs/cgroup/system.slice/superbench.service", lookup=accounts())
        install(self.root, documents)
        install(self.root, documents)
        policy = self.root / POLICY_FILE
        self.assertEqual(policy.read_bytes(), POLICY)
        self.assertEqual(policy.stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.root / REGISTRY_FILE).stat().st_mode & 0o777, 0o644)
        # Different accounts (or an operator's edit) must refuse, not replace.
        other = installed_identity("/sys/fs/cgroup/x", lookup=accounts(
            user_superbench_payload=SimpleNamespace(pw_uid=975, pw_gid=975, pw_shell="/usr/sbin/nologin"),
            group_superbench_payload=SimpleNamespace(gr_gid=975, gr_mem=[])))
        with self.assertRaisesRegex(ValueError, "existing file differs"):
            install(self.root, other)
        self.assertEqual(policy.read_bytes(), POLICY)
        policy.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "unexpected permissions"):
            install(self.root, documents)

    def test_preflight_refuses_identity_documents_without_their_accounts(self):
        self.assertFalse(preflight_identity(self.root, prepare=False, lookup=accounts()))
        missing = accounts(user_superbench_payload=None)
        with self.assertRaises(ValueError):
            preflight_identity(self.root, prepare=False, lookup=missing)
        self.assertTrue(preflight_identity(self.root, prepare=True, lookup=missing))
        foreign = self.root / REGISTRY_FILE
        foreign.parent.mkdir(parents=True)
        foreign.write_bytes(b"someone else's registry")
        with self.assertRaisesRegex(ValueError, "without its accounts"):
            preflight_identity(self.root, prepare=True, lookup=missing)
        self.assertEqual(foreign.read_bytes(), b"someone else's registry")

    def test_activation_file_is_strict_and_all_false(self):
        raw, mode = activation_document()[ACTIVATION_FILE]
        self.assertEqual(mode, 0o644)
        pairs = json.loads(raw, object_pairs_hook=list)
        self.assertEqual(pairs, [("version", "superbench.activation.v1")] + [(n, False) for n in ACTIVATION_SWITCHES])
        install(self.root, activation_document())
        install(self.root, activation_document())
        edited = self.root / ACTIVATION_FILE
        edited.write_bytes(raw.replace(b'"finite_io": false', b'"finite_io": true'))
        with self.assertRaisesRegex(ValueError, "existing file differs"):
            install(self.root, activation_document())
        self.assertIn(b'"finite_io": true', edited.read_bytes())

    def test_managed_payload_creates_the_payload_account(self):
        binaries = self.root / "bin"
        binaries.mkdir()
        for name in ["bench", "benchd", "bench-quota"]:
            (binaries / name).write_bytes(b"\x7fELF\x02\x01" + bytes(12) + bytes([62, 0]))
        (binaries / "bench-payload").write_bytes(helper_elf())
        self.assertNotIn("usr/lib/sysusers.d/superbench-payload.conf", payload(binaries, False))
        rule, mode = payload(binaries, True)["usr/lib/sysusers.d/superbench-payload.conf"]
        self.assertEqual(mode, 0o644)
        self.assertEqual(rule, b'u superbench-payload - "SuperBench payload" - /usr/sbin/nologin\n')


if __name__ == "__main__":
    unittest.main()
