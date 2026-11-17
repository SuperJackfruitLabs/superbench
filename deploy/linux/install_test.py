import contextlib
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import struct
from types import SimpleNamespace
import unittest

import subprocess

from install import (ACTIVATION_FILE, ACTIVATION_SWITCHES, CGROUP_ROOT, POLICY_FILE, REGISTRY_FILE,
                     DATA_ROOT, QUOTA_HELPER, STORAGE_FILE, UNIT_FILE, MissingAccount, account_ids,
                     check_data_mount,
                     activation_document, apply, checked_path, identity_documents, install,
                     installed_identity, main, payload, preflight_identity, read_budgets, static_helper,
                     storage_document, storage_state)

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

    def every_user():
        return [SimpleNamespace(pw_name=n, **vars(u)) for n, u in users.items()]

    def lookup(name):
        return account_ids(name, getpwnam=lambda n: users[n], getgrnam=lambda n: groups[n],
                           getpwall=every_user)
    return lookup


def binaries(directory):
    directory.mkdir()
    for name in ["bench", "benchd", "bench-quota"]:
        (directory / name).write_bytes(b"\x7fELF\x02\x01" + bytes(12) + bytes([62, 0]))
    (directory / "bench-payload").write_bytes(helper_elf())
    return directory


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
                          # Another account whose primary group is the payload group.
                          {"user_node": SimpleNamespace(pw_uid=1001, pw_gid=976, pw_shell="/bin/bash")},
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

    def test_payload_group_may_be_another_accounts_primary_group_only_if_it_is_its_own(self):
        self.assertEqual(accounts(user_node=SimpleNamespace(pw_uid=1001, pw_gid=977, pw_shell="/bin/bash"))(
            "superbench-payload"), (976, 976))
        with self.assertRaisesRegex(ValueError, "no other account's primary group: node"):
            accounts(user_node=SimpleNamespace(pw_uid=1001, pw_gid=976, pw_shell="/bin/bash"))("superbench-payload")

    def test_preflight_refuses_a_foreign_account_before_any_effect_even_with_prepare(self):
        shell = SimpleNamespace(pw_uid=976, pw_gid=976, pw_shell="/bin/bash")
        for overrides in [{"user_superbench_payload": shell},
                          {"user_superbench": None, "user_superbench_payload": shell},
                          {"user_superbench_payload": None, "user_superbench": SimpleNamespace(
                              pw_uid=977, pw_gid=977, pw_shell="/bin/sh")}]:
            for prepare in (False, True):
                with self.assertRaisesRegex(ValueError, "foreign account", msg=str(overrides)):
                    preflight_identity(self.root, prepare=prepare, lookup=accounts(**overrides))
        with self.assertRaises(MissingAccount):
            preflight_identity(self.root, prepare=False, lookup=accounts(user_superbench=None))

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


class Main(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sb-main-", dir="/tmp")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()

    def test_identity_is_for_the_live_managed_root_only(self):
        bins = binaries(self.root / "bin")
        target = self.root / "staging"
        target.mkdir()
        for flags in (["--identity", "--managed"], ["--identity"]):
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as stop:
                main(["--bin-dir", str(bins), "--root", str(target), *flags])
            self.assertEqual(stop.exception.code, 2)
            self.assertIn("--identity applies only to a live / root with --managed", stderr.getvalue())
        self.assertEqual(list(target.iterdir()), [])

    def test_prepare_creates_both_accounts_then_writes_the_deferred_identity(self):
        bins = binaries(self.root / "bin")
        target = self.root / "host"
        target.mkdir()
        created, commands = [], []
        ready = accounts()

        def lookup(name):
            if not created:
                raise MissingAccount("account or group is missing: " + name)
            return ready(name)

        def run(command, check):
            self.assertTrue(check)
            commands.append(command)
            if command[0] == "systemd-sysusers":
                self.assertFalse((target / POLICY_FILE).exists(), "identity written before the accounts")
                created.append(True)

        apply(SimpleNamespace(bin_dir=bins, root=target, managed=True, prepare=True, identity=True,
                              activation=True, cgroup_root=CGROUP_ROOT), run=run, lookup=lookup)
        self.assertIn(["systemd-sysusers", "/usr/lib/sysusers.d/superbench.conf",
                       "/usr/lib/sysusers.d/superbench-payload.conf"], commands)
        self.assertEqual((target / POLICY_FILE).read_bytes(), POLICY)
        self.assertEqual((target / REGISTRY_FILE).read_bytes(),
                         installed_identity(CGROUP_ROOT, lookup=ready)[REGISTRY_FILE][0])
        self.assertTrue((target / "usr/lib/sysusers.d/superbench-payload.conf").exists())


BUDGETS = {"BenchBytes": 1 << 30, "StoreBytes": 2 << 30, "MirrorBytes": 1 << 30, "ReserveBytes": 1 << 30,
           "BenchInodes": 100000, "StoreInodes": 1000000, "MirrorInodes": 500000, "ReserveInodes": 100000}


class IndexedStorage(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sb-storage-", dir="/tmp")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.target = self.root / "host"
        self.target.mkdir()
        (self.target / "var/lib/superbench-quota").mkdir(parents=True, mode=0o700)
        self.budgets = self.root / "budgets.json"
        self.budgets.write_text(json.dumps(BUDGETS))
        self.bins = binaries(self.root / "bin")
        self.commands = []
        self.mounts = []
        self.fail_initialize = None

    def args(self, **overrides):
        values = dict(bin_dir=self.bins, root=self.target, managed=True, prepare=False, identity=True,
                      activation=True, cgroup_root=CGROUP_ROOT, storage=self.budgets)
        values.update(overrides)
        return SimpleNamespace(**values)

    def run_command(self, command, check, **options):
        self.assertTrue(check)
        self.commands.append(command)
        quota = self.target / "var/lib/superbench-quota"
        if command == [QUOTA_HELPER, "operation-info"]:
            # The real helper opens the journal only with its ready record.
            if not (quota / "ledger").is_dir() or len((quota / "ledger.format-v2").read_bytes().splitlines()) != 2:
                raise subprocess.CalledProcessError(1, command)
        if command == [QUOTA_HELPER, "initialize-indexed"]:
            unit = self.target / UNIT_FILE
            # The helper's installation gate needs the masked unit and the indexed policy.
            self.assertTrue(unit.is_symlink())
            self.assertEqual(os.readlink(unit), "/dev/null")
            self.assertIn(b'"OperationJournal": "indexed-v2"', (self.target / STORAGE_FILE).read_bytes())
            self.assertIn(["systemctl", "daemon-reload"], self.commands[:-1])
            if self.fail_initialize == "before":
                raise subprocess.CalledProcessError(1, command)
            # The real initializer's order (quotaoperationjournal): the format
            # intent, the ledger directory, its records, and the ready line last.
            (quota / "ledger.format-v2").write_text(
                json.dumps({"version": "v2", "ledger": "/var/lib/superbench-quota/ledger", "nonce": "0" * 64}) + "\n")
            (quota / "ledger").mkdir()
            (quota / "ledger" / "operations.lock").write_bytes(b"")
            if self.fail_initialize == "partway":
                raise subprocess.CalledProcessError(1, command)
            (quota / "ledger" / "assignments.json").write_bytes(b"{}")
            with (quota / "ledger.format-v2").open("a") as anchor:
                anchor.write(json.dumps({"device": 1, "inode": 2, "lock_device": 1, "lock_inode": 3,
                                         "database_device": 1, "database_inode": 4,
                                         "authority": "a" * 64}) + "\n")

    def mounted(self, path):
        self.mounts.append(path)

    def apply(self, **overrides):
        apply(self.args(**overrides), run=self.run_command, lookup=accounts(), mounted=self.mounted)

    def test_budgets_are_checked_as_the_go_policy_checks_them(self):
        self.assertEqual(read_budgets(self.budgets), BUDGETS)
        for bad in ['[["BenchBytes", 1024]]', '{"BenchBytes": 1024}',
                    json.dumps(dict(BUDGETS, BenchBytes=1000)), json.dumps(dict(BUDGETS, BenchInodes=0)),
                    json.dumps(dict(BUDGETS, StoreBytes=True)), json.dumps(dict(BUDGETS, Root="/srv")),
                    json.dumps(BUDGETS)[:-1] + ', "BenchBytes": 2048}']:
            self.budgets.write_text(bad)
            with self.assertRaises(ValueError, msg=bad):
                read_budgets(self.budgets)

    def test_fresh_install_writes_the_indexed_policy_and_initializes_once(self):
        self.apply()
        self.assertEqual(self.commands.count([QUOTA_HELPER, "initialize-indexed"]), 1)
        unit = self.target / UNIT_FILE
        self.assertFalse(unit.is_symlink(), "the service stayed masked")
        self.assertEqual(unit.read_bytes(), (Path(__file__).resolve().parent / "superbench.service").read_bytes())
        self.assertEqual(self.commands[-1], ["systemctl", "daemon-reload"])
        policy = json.loads((self.target / STORAGE_FILE).read_bytes())
        self.assertEqual(policy["OperationJournal"], "indexed-v2")
        self.assertEqual(policy["Ledger"], "/var/lib/superbench-quota/ledger")
        self.assertEqual((policy["OwnerUID"], policy["OwnerGID"]), (977, 977))
        self.assertEqual((self.target / STORAGE_FILE).stat().st_mode & 0o777, 0o644)
        self.assertEqual(storage_state(self.target), "initialized")
        # Rerunning the same command never initializes again.
        self.commands.clear()
        self.apply()
        self.assertNotIn([QUOTA_HELPER, "initialize-indexed"], self.commands)

    def test_failed_initialization_keeps_the_service_masked_and_can_be_retried(self):
        self.fail_initialize = "before"
        with self.assertRaisesRegex(ValueError, "stays masked"):
            self.apply()
        unit = self.target / UNIT_FILE
        self.assertTrue(unit.is_symlink())
        self.assertEqual(storage_state(self.target), "fresh")
        self.fail_initialize = None
        self.apply()
        self.assertFalse(unit.is_symlink())
        self.assertEqual(storage_state(self.target), "initialized")

    def test_initialization_that_stopped_partway_is_refused_and_stays_masked(self):
        # Both names exist, but without the ready line the journal never opens:
        # a rerun must not call this initialized and unmask the service.
        self.fail_initialize = "partway"
        with self.assertRaisesRegex(ValueError, "stays masked"):
            self.apply()
        unit = self.target / UNIT_FILE
        self.fail_initialize = None
        self.commands.clear()
        with self.assertRaisesRegex(ValueError, "partial indexed ledger"):
            self.apply()
        self.assertTrue(unit.is_symlink(), "a partial initialization unmasked the service")
        self.assertEqual(os.readlink(unit), "/dev/null")
        self.assertEqual(self.commands, [])

    def test_unit_stays_masked_when_the_journal_does_not_open(self):
        real = self.run_command

        def closed(command, check, **options):
            if command == [QUOTA_HELPER, "operation-info"]:
                self.commands.append(command)
                raise subprocess.CalledProcessError(1, command)
            return real(command, check, **options)
        with self.assertRaisesRegex(ValueError, "not unmasked"):
            apply(self.args(), run=closed, lookup=accounts(), mounted=self.mounted)
        self.assertTrue((self.target / UNIT_FILE).is_symlink())

    def test_a_different_storage_policy_refuses_before_any_effect(self):
        other = storage_document((977, 977), dict(BUDGETS, BenchBytes=2 << 30))
        install(self.target, other)
        with self.assertRaisesRegex(ValueError, "existing file differs"):
            self.apply()
        self.assertFalse((self.target / "usr/local/bin/benchd").exists(), "effects before refusal")
        self.assertEqual((self.target / STORAGE_FILE).read_bytes(), other[STORAGE_FILE][0])

    def test_legacy_or_partial_ledgers_refuse_before_any_effect(self):
        quota = self.target / "var/lib/superbench-quota"
        legacy = storage_document((977, 977), BUDGETS)[STORAGE_FILE][0].replace(
            b'  "OperationJournal": "indexed-v2",\n', b"")
        for name, setup, message in [
            ("legacy policy", lambda: install(self.target, {STORAGE_FILE: (legacy, 0o644)}), "does not select the indexed"),
            ("legacy ledger", lambda: (quota / "assignments.json").write_bytes(b"{}"), "existing quota ledger"),
            ("partial anchor", lambda: (quota / "ledger.format-v2").write_bytes(b"x"), "partial indexed ledger"),
            ("ledger without policy", lambda: [(quota / "ledger").mkdir(), (quota / "ledger.format-v2").write_bytes(b"x")],
             "partial indexed ledger"),
            ("installed unit", lambda: install(self.target, {UNIT_FILE: (b"unit", 0o644)}), "already installed"),
        ]:
            with self.subTest(name):
                setup()
                with self.assertRaisesRegex(ValueError, message):
                    self.apply()
                self.assertNotIn([QUOTA_HELPER, "initialize-indexed"], self.commands)
                self.assertFalse((self.target / "usr/local/bin/benchd").exists(), "effects before refusal")
                for path in [self.target / STORAGE_FILE, self.target / UNIT_FILE, *quota.iterdir()]:
                    if path.is_dir():
                        path.rmdir()
                    elif os.path.lexists(path):
                        path.unlink()

    def test_storage_is_written_only_onto_the_mounted_empty_data_volume(self):
        def unmounted(path):
            raise ValueError(path + " is not a mounted filesystem")
        with self.assertRaisesRegex(ValueError, "not a mounted filesystem"):
            apply(self.args(), run=self.run_command, lookup=accounts(), mounted=unmounted)
        self.assertFalse(os.path.lexists(self.target / STORAGE_FILE))
        self.assertFalse((self.target / "usr/local/bin/benchd").exists())
        stray = self.target / DATA_ROOT[1:] / "benches"
        stray.mkdir(parents=True)
        with self.assertRaisesRegex(ValueError, "must be empty"):
            self.apply()
        self.assertFalse(os.path.lexists(self.target / STORAGE_FILE))
        stray.rmdir()
        self.apply()
        self.assertEqual(self.mounts[-1], str(self.target / DATA_ROOT[1:]))

    def test_mount_check_reads_the_visible_mount(self):
        def devices(mapping):
            return lambda path: SimpleNamespace(st_dev=mapping[path])
        separate = devices({DATA_ROOT: 2, "/srv": 1})
        info = self.root / "mountinfo"
        good = "36 25 7:1 / " + DATA_ROOT + " rw,relatime - xfs /dev/loop1 rw,attr2,inode64,prjquota\n"
        info.write_text(good)
        check_data_mount(DATA_ROOT, info, separate)
        with self.assertRaisesRegex(ValueError, "not a mounted filesystem"):
            check_data_mount(DATA_ROOT, info, devices({DATA_ROOT: 1, "/srv": 1}))
        with self.assertRaisesRegex(ValueError, "not a mounted filesystem"):
            check_data_mount(str(self.root / "absent"), info)
        for bad, message in [
                ("", "root of a mounted XFS"),
                (good.replace(" xfs ", " ext4 "), "root of a mounted XFS"),
                (good.replace(" / " + DATA_ROOT, " /sub " + DATA_ROOT), "root of a mounted XFS"),
                (good.replace(",prjquota", ""), "enforced project quotas"),
                (good.replace("prjquota", "prjquota,pqnoenforce"), "enforced project quotas"),
                (good + "40 36 0:5 / " + DATA_ROOT + " rw - tmpfs tmpfs rw\n", "root of a mounted XFS")]:
            info.write_text(bad)
            with self.assertRaisesRegex(ValueError, message, msg=bad):
                check_data_mount(DATA_ROOT, info, separate)

    def test_storage_needs_identity_and_a_direct_root_login(self):
        for flags, environment, message in [
                (["--managed"], {}, "--storage applies only with --identity"),
                (["--managed", "--identity"], {"SUDO_UID": "1000"}, "direct root login")]:
            stderr = io.StringIO()
            previous = dict(os.environ)
            os.environ.update(environment)
            try:
                with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as stop:
                    main(["--bin-dir", str(self.bins), "--root", "/", "--storage", str(self.budgets), *flags])
            finally:
                os.environ.clear()
                os.environ.update(previous)
            self.assertEqual(stop.exception.code, 2)
            self.assertIn(message, stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
