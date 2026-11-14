import os
from pathlib import Path
import tempfile
import unittest

from install import checked_path, install, payload


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
        unmanaged = payload(binaries, False)
        managed = payload(binaries, True)
        self.assertNotIn("etc/sudoers.d/superbench", unmanaged)
        self.assertIn(b"/srv/superbench-data 0755 root root", managed["usr/lib/tmpfiles.d/superbench.conf"][0])
        rule = managed["etc/sudoers.d/superbench"][0]
        self.assertIn(b"NOPASSWD: /usr/local/libexec/bench-quota", rule)
        self.assertNotIn(b"/bin/sh", rule)
        (binaries / "benchd").write_bytes(b"\x7fELF\x02\x01" + bytes(12) + bytes([183, 0]))
        with self.assertRaises(ValueError):
            payload(binaries, True)
        (binaries / "benchd").write_bytes(b"not a Linux binary")
        with self.assertRaises(ValueError):
            payload(binaries, True)


if __name__ == "__main__":
    unittest.main()
