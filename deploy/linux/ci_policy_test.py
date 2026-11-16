"""Source-only CI boundary checks; these never create a namespace or cgroup."""
import os
from pathlib import Path
import re
import runpy
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
IDENTITY = runpy.run_path(str(ROOT / 'scripts/check-identity-ci.py'), run_name='identity_policy_test')


class CIPolicy(unittest.TestCase):
    def test_fixture_budget_and_whole_workflow_deadline(self):
        quota, period = map(int, IDENTITY['LIMITS']['cpu.max'].split())
        self.assertEqual(quota, 8 * period)
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
        self.assertEqual(environment.get('GOMAXPROCS'), '8')
        self.assertNotIn('UNRELATED_AMBIENT_CREDENTIAL', environment)
        self.assertEqual(environment['SUPERBENCH_IDENTITY_FIXTURE'], '1')


if __name__ == '__main__':
    unittest.main()
