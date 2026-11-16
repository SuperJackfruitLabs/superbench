"""Conductor characterization: no compilation, privileges or fixture execution."""
import ast
import contextlib
import re
import importlib.util
import io
import os
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest import mock

SOURCE = Path(__file__).with_name('check-ci.py')


def load():
    spec = importlib.util.spec_from_file_location('ci_conductor', SOURCE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RunnerSeparation(unittest.TestCase):
    def test_nonroot_runs_ordinary_suite_without_identity_preflight_or_root_claim(self):
        module = load()
        with tempfile.TemporaryDirectory() as parent:
            fixture = Path(parent) / 'fixture'
            fixture.mkdir()
            account = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
            self.assertNotEqual(account.pw_uid, 0, 'characterization must run unprivileged')
            calls = []

            def command(args, **kwargs):
                calls.append((args, kwargs))
                if kwargs.get('preexec_fn'):
                    kwargs['preexec_fn']()
                return SimpleNamespace(returncode=0)

            with contextlib.ExitStack() as stack:
                stack.enter_context(mock.patch.object(module.sys, 'argv', ['check-ci.py']))
                stack.enter_context(mock.patch.object(module.tempfile, 'mkdtemp', return_value=str(fixture)))
                stack.enter_context(mock.patch.object(module.shutil, 'disk_usage', return_value=SimpleNamespace(free=8 << 30)))
                stack.enter_context(mock.patch.object(module.shutil, 'which', side_effect=lambda name: '/trusted/' + name + '/bin/' + name))
                stack.enter_context(mock.patch.object(module.os.path, 'lexists', return_value=False))
                stack.enter_context(mock.patch.object(module.pwd, 'getpwuid', return_value=account))
                chown = stack.enter_context(mock.patch.object(module.os, 'chown', side_effect=AssertionError('nonroot chown')))
                stack.enter_context(mock.patch.object(module.os, 'setgroups', side_effect=AssertionError('nonroot credential change')))
                stack.enter_context(mock.patch.object(module.subprocess, 'run', side_effect=command))
                output = stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
                module.main()
            ordinary = [kwargs for args, kwargs in calls if args == ['sh', 'scripts/check.sh']]
            self.assertEqual(len(ordinary), 1)
            self.assertEqual(ordinary[0]['env']['GOFLAGS'], '-p=2')
            self.assertEqual(ordinary[0]['env']['GOMAXPROCS'], '2')
            self.assertEqual(ordinary[0]['env']['PATH'], '/trusted/go/bin:/trusted/node/bin:/usr/local/bin:/usr/bin:/bin')
            self.assertFalse(any('check-identity-ci.py' in str(args) for args, _ in calls))
            self.assertFalse(any('test' in args for args, _ in calls))
            self.assertIn('require the separate privileged job', output.getvalue())
            self.assertNotIn('explicit effective-root refusal', output.getvalue())
            chown.assert_not_called()

    def test_unprivileged_privileged_job_refuses_before_any_command(self):
        module = load()
        with mock.patch.object(module.sys, 'argv', ['check-ci.py', '--privileged-only']), \
                mock.patch.object(module.os, 'geteuid', return_value=1001), \
                mock.patch.object(module.subprocess, 'run') as command:
            with self.assertRaisesRegex(SystemExit, 'privileged CI requires an actual root'):
                module.main()
            command.assert_not_called()

    def test_nonroot_with_root_group_refuses_before_any_command(self):
        module = load()
        with mock.patch.object(module.sys, 'argv', ['check-ci.py']), \
                mock.patch.object(module.os, 'getuid', return_value=1001), \
                mock.patch.object(module.os, 'geteuid', return_value=1001), \
                mock.patch.object(module.os, 'getgid', return_value=0), \
                mock.patch.object(module.os, 'getegid', return_value=0), \
                mock.patch.object(module.subprocess, 'run') as command:
            with self.assertRaisesRegex(SystemExit, 'actual unprivileged UID and groups'):
                module.main()
            command.assert_not_called()

    def test_privileged_preflight_failure_never_becomes_ordinary_or_fixture_pass(self):
        module = load()
        error = module.subprocess.CalledProcessError(1, ['preflight'])
        with mock.patch.object(module.sys, 'argv', ['check-ci.py', '--privileged-only']), \
                mock.patch.object(module.os, 'geteuid', return_value=0), \
                mock.patch.object(module.subprocess, 'run', side_effect=error) as command:
            with self.assertRaises(module.subprocess.CalledProcessError):
                module.main()
            self.assertEqual(command.call_count, 1)
            self.assertEqual(command.call_args.args[0][-1], '--preflight')

    def test_workflow_keeps_two_explicit_jobs_and_no_privilege_skip_as_pass(self):
        workflow = SOURCE.parent.parent / '.forgejo/workflows/ci.yml'
        text = workflow.read_text()
        self.assertIn('runs-on: ubuntu-latest', text)
        self.assertIn('runs-on: [self-hosted, superbench-privileged]', text)
        self.assertIn('python3 scripts/check-ci.py --privileged-only', text)
        self.assertNotIn('head.repo.fork == false', text)
        self.assertNotIn('continue-on-error', text)
        self.assertEqual(text.count('timeout-minutes: 60'), 2)


class PrivilegedTrustGate(unittest.TestCase):
    def test_actual_workflow_gate_rejects_untrusted_events_and_missing_metadata(self):
        text = (SOURCE.parent.parent / '.forgejo/workflows/ci.yml').read_text()
        job = text.split('  privileged-identity:', 1)[1]
        expression = re.search(r'^    if: (.+)$', job, re.MULTILINE).group(1)
        # Evaluate the actual closed expression, with unknown context failing closed.
        # No YAML dependency or shell evaluation is needed for this scalar condition.
        expression = expression.replace('&&', ' and ').replace('||', ' or ')

        def permitted(event, ref, protected=None):
            values = {'github.event_name': event, 'github.ref': ref,
                      'needs.check.outputs.trusted_ref': protected}
            resolved = re.sub(r'(?:github|needs)(?:\.[A-Za-z_]+)+',
                              lambda match: repr(values.get(match.group())), expression)
            tree = ast.parse(resolved, mode='eval')
            allowed = (ast.Expression, ast.BoolOp, ast.And, ast.Or, ast.Compare,
                       ast.Eq, ast.NotEq, ast.Constant)
            self.assertTrue(all(isinstance(node, allowed) for node in ast.walk(tree)))
            return eval(compile(tree, '<workflow trust gate>', 'eval'), {'__builtins__': {}})

        self.assertIn('needs: check', job)
        self.assertNotIn('ref_protected', expression)
        self.assertIn('trusted_ref: ${{ steps.trusted_ref.outputs.trusted_ref }}', text)
        self.assertTrue(permitted('push', 'refs/heads/main', 'true'))
        for event, ref, protected in (
            ('push', 'refs/heads/main', False),
            ('push', 'refs/heads/main', None),
            ('push', 'refs/heads/main', True),
            ('push', 'refs/heads/main', 'false'),
            ('push', 'refs/heads/topic', 'true'),
            ('push', 'refs/tags/v0.0.1', 'true'),
            ('pull_request', 'refs/heads/main', 'true'),
            ('pull_request', 'refs/pull/47/head', 'true'),
            ('pull_request_target', 'refs/heads/main', 'true'),
            ('workflow_dispatch', 'refs/heads/main', 'true'),
        ):
            with self.subTest(event=event, ref=ref, protected=protected):
                self.assertFalse(permitted(event, ref, protected))


if __name__ == '__main__':
    unittest.main()
