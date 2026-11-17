"""Source-only CI boundary checks; these never create a namespace or cgroup."""
import os
from pathlib import Path
import re
import runpy
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
CONDUCTOR = runpy.run_path(str(ROOT / 'scripts/check-ci.py'), run_name='conductor_policy_test')
IDENTITY = runpy.run_path(str(ROOT / 'scripts/check-identity-ci.py'), run_name='identity_policy_test')


class CIPolicy(unittest.TestCase):
    def test_fixture_budget_and_whole_workflow_deadline(self):
        quota, period = map(int, IDENTITY['LIMITS']['cpu.max'].split())
        self.assertEqual(quota, 2 * period)
        self.assertEqual(IDENTITY['LIMITS']['memory.max'], str(2 << 30))
        self.assertEqual(IDENTITY['LIMITS']['memory.swap.max'], '0')
        workflow = (ROOT / '.forgejo/workflows/ci.yml').read_text()
        timeout = re.search(r'^    timeout-minutes: (\d+)\s*$', workflow, re.M)
        self.assertIsNotNone(timeout)
        self.assertGreaterEqual(int(timeout.group(1)), 45)

    def test_actual_root_fixture_exec_preserves_cpu_budget_and_drops_ambient_env(self):
        def namespace(path):
            if path.endswith('/pid'):
                return 'pid:[1]'
            return 'original-' + path.rsplit('/', 1)[1]

        def identity(path):
            return SimpleNamespace(st_dev=1, st_ino=1 if '/proc/1/' in path else 2)

        with patch.dict(os.environ, {
            'SUPERBENCH_IDENTITY_ORIGINAL_MNT': 'original-mnt',
            'SUPERBENCH_IDENTITY_ORIGINAL_NET': 'original-net',
            'UNRELATED_AMBIENT_CREDENTIAL': 'must-not-forward',
        }, clear=True), patch('os.getuid', return_value=0), patch('os.geteuid', return_value=0), \
                patch('os.readlink', side_effect=namespace), patch('os.stat', side_effect=identity), \
                patch('os.execve') as execute:
            IDENTITY['inside']('/owned/sealed/fixture', 'ExactFixture')
        binary, arguments, environment = execute.call_args.args
        self.assertEqual(binary, '/owned/sealed/fixture')
        self.assertIn('-test.run=^ExactFixture$', arguments)
        self.assertEqual(environment.get('GOMAXPROCS'), '2')
        self.assertNotIn('UNRELATED_AMBIENT_CREDENTIAL', environment)
        self.assertEqual(environment['SUPERBENCH_IDENTITY_FIXTURE'], '1')

    def test_preparation_exec_fixture_is_selected_and_requires_its_proof(self):
        selected = [(package, markers) for package, selector, markers in IDENTITY['TESTS']
                    if selector == 'TestInstalledReadOnlyRegistrationInspectionFixture']
        self.assertEqual(len(selected), 1)
        self.assertEqual(selected[0][0], 'payloadexec')
        self.assertIn('PREPARATION_ACTUAL_ROOT_INPUTS_AND_REFUSALS', selected[0][1])
        phases = [package + '-' + selector for package, selector, _ in IDENTITY['TESTS']]
        self.assertEqual(len(phases), len(set(phases)))

    def test_finite_input_fixture_is_selected_and_requires_its_proof(self):
        selected = [(package, markers) for package, selector, markers in IDENTITY['TESTS']
                    if selector == 'TestFiniteInputSealedAfterCredentialDropFixture']
        self.assertEqual(len(selected), 1)
        self.assertEqual(selected[0][0], 'payloadexec')
        self.assertEqual(set(selected[0][1]), {
            'FINITE_INPUT_SEALED_DELIVERED_AFTER_CREDENTIAL_DROP',
            'FINITE_INPUT_IMMUTABLE_READ_ONLY_REFUSALS',
            'FINITE_INPUT_EXACT_BYTES_EOF_NO_DESCRIPTOR_LEAK',
            'FINITE_INPUT_EMPTY_KEEPS_INHERITED_STDIN'})

    def test_readonly_cgroup_refuses_before_privileged_fixture_execution(self):
        refusal = subprocess.CalledProcessError(1, ['identity', '--preflight'])
        with patch.object(sys, 'argv', ['check-ci.py', '--privileged-only']), patch('os.geteuid', return_value=0), \
                patch('subprocess.run', side_effect=refusal) as execute, \
                patch('tempfile.mkdtemp', side_effect=AssertionError('privileged fixture started before runner admission')) as temporary, \
                patch('shutil.disk_usage', return_value=SimpleNamespace(free=4 << 30)), \
                patch('shutil.which', return_value='/usr/bin/go'), patch('os.path.lexists', return_value=False), \
                patch('pwd.getpwnam') as account:
            with self.assertRaises(subprocess.CalledProcessError) as caught:
                CONDUCTOR['main']()
        self.assertIs(caught.exception, refusal)
        self.assertEqual(execute.call_count, 1)
        self.assertEqual(execute.call_args.args[0][-1], '--preflight')
        temporary.assert_not_called()
        account.assert_not_called()

    def test_cgroup_probe_removes_only_its_unchanged_empty_child_on_failure(self):
        probe = IDENTITY.get('probe_cgroup')
        self.assertIsNotNone(probe)
        from unittest.mock import MagicMock
        group = MagicMock()
        leaf = MagicMock()
        group.__truediv__.return_value = leaf
        group.stat.return_value = SimpleNamespace(st_dev=1, st_ino=2)
        leaf.read_text.return_value = 'populated 0'
        leaf.write_text.side_effect = OSError('fixture budget write refused')
        parent = MagicMock()
        parent.__truediv__.return_value = group
        with patch.dict(probe.__globals__, {'Path': lambda unused: parent}):
            with self.assertRaisesRegex(OSError, 'fixture budget write refused'):
                probe()
        group.mkdir.assert_called_once_with()
        group.rmdir.assert_called_once_with()

    def test_readonly_cgroup_creation_does_not_attempt_cleanup_or_budget_writes(self):
        import errno
        from unittest.mock import MagicMock
        probe = IDENTITY['probe_cgroup']
        parent, group = MagicMock(), MagicMock()
        parent.__truediv__.return_value = group
        refusal = OSError(errno.EROFS, 'read-only cgroup')
        group.mkdir.side_effect = refusal
        with patch.dict(probe.__globals__, {'Path': lambda unused: parent}):
            with self.assertRaises(OSError) as caught:
                probe()
        self.assertIs(caught.exception, refusal)
        group.rmdir.assert_not_called()
        group.__truediv__.assert_not_called()


if __name__ == '__main__':
    unittest.main()
