import contextlib
import hashlib
import io
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest

from install import EXECUTABLES, CGROUP_ROOT, Upgrade, apply, install, main, payload, read_previous, unit_stopped
from install_test import binaries


def sums(directory, names=("bench", "benchd", "bench-quota", "bench-payload")):
    lines = "".join(hashlib.sha256((directory / n).read_bytes()).hexdigest() + "  " + n + "\n" for n in names)
    (directory / "SHA256SUMS").write_text(lines)
    return directory / "SHA256SUMS"


def no_commands(command, check):
    """visudo and systemd are host effects; these tests stage files only."""
    assert command[0] == "visudo", command


class UpgradeMode(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sb-upgrade-", dir="/tmp")
        self.addCleanup(self.tmp.cleanup)
        self.top = Path(self.tmp.name).resolve()
        self.root = self.top / "host"
        self.root.mkdir()
        self.old = binaries(self.top / "old")
        self.previous = sums(self.old)
        self.new = binaries(self.top / "new")
        for name in ("bench", "benchd", "bench-quota"):
            (self.new / name).write_bytes((self.new / name).read_bytes() + b"new revision")
        apply(self.args(self.old, upgrade=False), run=no_commands)

    def args(self, bins, upgrade=True, backup="backup"):
        return SimpleNamespace(bin_dir=bins, root=self.root, managed=True, prepare=False, identity=False,
                               activation=False, cgroup_root=CGROUP_ROOT, upgrade=upgrade,
                               previous=self.previous if upgrade else None,
                               backup=self.top / backup if upgrade else None)

    def installed(self, relative):
        return (self.root / relative).read_bytes()

    def test_plain_rerun_still_refuses_a_newer_executable(self):
        with self.assertRaisesRegex(ValueError, "existing file differs"):
            apply(self.args(self.new, upgrade=False), run=no_commands)
        self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes())

    def test_upgrade_replaces_only_changed_executables_and_keeps_a_backup(self):
        config = {relative: self.installed(relative) for relative in payload(self.old, True) if relative not in EXECUTABLES}
        inode = (self.root / "usr/local/bin/bench").stat().st_ino
        apply(self.args(self.new), run=no_commands)
        for name, relative in [("bench", "usr/local/bin/bench"), ("benchd", "usr/local/bin/benchd"),
                               ("bench-quota", "usr/local/libexec/bench-quota"),
                               ("bench-payload", "usr/local/libexec/bench-payload")]:
            self.assertEqual(self.installed(relative), (self.new / name).read_bytes(), name)
            self.assertEqual((self.root / relative).stat().st_mode & 0o7777, 0o755, name)
        self.assertNotEqual((self.root / "usr/local/bin/bench").stat().st_ino, inode, "replaced in place")
        backup = self.top / "backup"
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        self.assertEqual(sorted(p.name for p in backup.iterdir()), ["SHA256SUMS", "bench", "bench-quota", "benchd"])
        self.assertEqual(read_previous(backup / "SHA256SUMS"), {k: v for k, v in read_previous(self.previous).items() if k != "bench-payload"})
        for name in ("bench", "benchd", "bench-quota"):
            self.assertEqual((backup / name).read_bytes(), (self.old / name).read_bytes())
            self.assertEqual((backup / name).stat().st_mode & 0o777, 0o600)
        for relative, data in config.items():
            self.assertEqual(self.installed(relative), data, relative)
        self.assertFalse([p for p in (self.root / "usr/local/bin").iterdir() if p.name.startswith(".superbench-install-")])
        apply(self.args(self.new, backup="second"), run=no_commands)  # an exact rerun replaces and backs up nothing
        self.assertFalse((self.top / "second").exists())

    def test_upgrade_refuses_a_foreign_executable_before_any_effect(self):
        (self.root / "usr/local/bin/benchd").chmod(0o755)
        (self.root / "usr/local/bin/benchd").write_bytes(b"\x7fELF\x02\x01locally patched")
        with self.assertRaisesRegex(ValueError, "not the previous reviewed build"):
            apply(self.args(self.new), run=no_commands)
        self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes())
        self.assertFalse((self.top / "backup").exists())

    def test_upgrade_refuses_unexpected_installed_files(self):
        bench = self.root / "usr/local/bin/bench"
        for name, damage, undo in [
                ("mode", lambda: bench.chmod(0o775), lambda: bench.chmod(0o755)),
                ("hard link", lambda: os.link(bench, self.top / "extra"), lambda: os.unlink(self.top / "extra"))]:
            damage()
            with self.assertRaises(ValueError, msg=name):
                apply(self.args(self.new), run=no_commands)
            undo()
            self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes(), name)
            self.assertFalse((self.top / "backup").exists(), name)
        bench.unlink()
        bench.symlink_to(self.old / "bench")
        with self.assertRaisesRegex(ValueError, "symlink"):
            apply(self.args(self.new), run=no_commands)

    def test_upgrade_never_replaces_configuration(self):
        env = self.root / "etc/superbench/benchd.env"
        env.chmod(0o644)
        env.write_bytes(env.read_bytes() + b"# operator change\n")
        with self.assertRaisesRegex(ValueError, "existing file differs"):
            apply(self.args(self.new), run=no_commands)
        self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes())

    def test_upgrade_refuses_an_executable_missing_from_previous_sums(self):
        self.previous = sums(self.old, names=("bench", "benchd"))
        with self.assertRaisesRegex(ValueError, "not in the previous SHA256SUMS"):
            apply(self.args(self.new), run=no_commands)
        self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes())

    def test_upgrade_needs_a_new_backup_directory(self):
        (self.top / "backup").mkdir()
        with self.assertRaisesRegex(ValueError, "new directory"):
            apply(self.args(self.new), run=no_commands)
        self.assertEqual(self.installed("usr/local/bin/bench"), (self.old / "bench").read_bytes())
        with self.assertRaisesRegex(ValueError, "absolute"):
            Upgrade({}, "relative/backup")

    def test_previous_sums_are_strict(self):
        bad = self.top / "bad"
        for text in ["", "deadbeef  bench\n", "%s  bench\n%s  bench\n" % ("a" * 64, "b" * 64), "%s  other\n" % ("a" * 64)]:
            bad.write_text(text)
            with self.assertRaises(ValueError, msg=repr(text)):
                read_previous(bad)

    def test_flags_go_together(self):
        for flags in (["--upgrade"], ["--previous", str(self.previous)], ["--upgrade", "--backup", str(self.top / "b")]):
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr), self.assertRaises(SystemExit) as stop:
                main(["--bin-dir", str(self.new), "--root", str(self.root), "--managed", *flags])
            self.assertEqual(stop.exception.code, 2)
            self.assertIn("--upgrade needs both --previous and --backup", stderr.getvalue())

    def test_unit_must_be_stopped_with_no_main_process(self):
        def shown(text):
            return lambda command, **kw: SimpleNamespace(stdout=text)
        self.assertTrue(unit_stopped(shown("ActiveState=inactive\nMainPID=0\n")))
        self.assertTrue(unit_stopped(shown("MainPID=0\nActiveState=failed\n")))
        for text in ["ActiveState=active\nMainPID=42\n", "ActiveState=activating\nMainPID=0\n",
                     "ActiveState=deactivating\nMainPID=7\n", "ActiveState=inactive\nMainPID=9\n", ""]:
            self.assertFalse(unit_stopped(shown(text)), text)


if __name__ == "__main__":
    unittest.main()
