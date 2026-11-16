import os
from pathlib import Path
import tempfile
import struct
import unittest

from install import checked_path, install, payload, static_helper


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


if __name__ == "__main__":
    unittest.main()
