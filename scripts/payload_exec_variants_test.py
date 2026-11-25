"""Characterize exact causal transforms independently of privileged execution."""
import importlib.util
from pathlib import Path
import unittest
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location('exec_variants', ROOT / 'scripts/payload-exec-variants.py')
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class CausalTransforms(unittest.TestCase):
    def setUp(self):
        self.credentials = (ROOT / 'internal/payloadexec/credentials_linux.go').read_text()
        self.execution = (ROOT / 'internal/payloadexec/exec_linux.go').read_text()

    def test_exact_source_boundaries_and_unchanged_plain(self):
        variants = MODULE.variants(self.credentials, self.execution)
        self.assertEqual(set(variants), set(MODULE.NAMES))
        self.assertEqual(variants['plain'], (self.credentials, self.execution))
        self.assertNotIn('syscall.Setgroups(', variants['no-groups'][0])
        self.assertIn('return verifyDropped(p)', variants['no-groups'][0])
        self.assertIn('p.PayloadUID++', variants['verify-fault'][0])
        self.assertIn('return verifyDropped(p)', variants['verify-fault'][0])
        self.assertIn('p.PayloadUID++', variants['verify-mutant'][0])
        self.assertNotIn('return verifyDropped(p)', variants['verify-mutant'][0])
        self.assertNotIn('verifyDropped(p)', variants['verify-mutant'][1])
        self.assertEqual(variants['verify-fault'][1], self.execution)
        self.assertEqual(self.execution.count('if err := verifyDropped(p); err != nil {'), 1)
        self.assertEqual(variants['late-fd'][1].count('if err := descriptorsCloseOnExec();'), 3)
        self.assertEqual(variants['late-fd-mutant'][1].count('if err := descriptorsCloseOnExec();'), 2)
        self.assertNotIn('O_CLOEXEC', variants['late-fd'][1].split('// PRIVATE QA:')[1].split('phase = "credentials"')[0])
        self.assertEqual(variants['late-fd'][1].count('/run/superbench-exec-fixture-canary'), 1)

    def test_interposed_input_and_git_statements_survive_final_sweep_mutation(self):
        boundary = '\tphase = "executable"\n\tpath := "/usr/bin/git"\n'
        # Interpose between enterPayload's final sweep and exec.
        for statement in ('\t*phase = "input"\n\tif input != nil { _ = bindPayloadInput(input) }\n',
                          '\t*phase = "git-policy"\n\tif err := verifyManagedGit(); err != nil { return err }\n'):
            execution = MODULE.replace_once(self.execution, '\t*phase = "exec"\n', statement + '\t*phase = "exec"\n')
            variants = MODULE.variants(self.credentials, execution)
            guarded, mutant = variants['late-fd'][1], variants['late-fd-mutant'][1]
            self.assertEqual(guarded.split(boundary)[0], mutant.split(boundary)[0])
            self.assertEqual(mutant.split(boundary)[0].count('if err := descriptorsCloseOnExec();'), 2)
            self.assertNotIn('descriptorsCloseOnExec()', mutant.split(boundary)[1])
            self.assertEqual(guarded.count(statement), 1)
            self.assertEqual(mutant.count(statement), 1)
            self.assertEqual(guarded.split(statement)[1], mutant.split(statement)[1])

    def test_missing_duplicate_final_sweep_and_executable_boundary_refuse(self):
        boundary = '\tphase = "executable"\n\tpath := "/usr/bin/git"\n'
        sweep = '\t*phase = "descriptors"\n\tif err := descriptorsCloseOnExec(); err != nil {\n\t\treturn err\n\t}\n'
        before, suffix = self.execution.split(boundary)
        for altered in (suffix.replace(sweep, ''), suffix.replace(sweep, sweep + sweep)):
            with self.subTest():
                with self.assertRaisesRegex(RuntimeError, 'exactly one source boundary'):
                    MODULE.variants(self.credentials, before + boundary + altered)
        with self.assertRaisesRegex(RuntimeError, 'exactly one source boundary'):
            MODULE.variants(self.credentials, self.execution + boundary)

    def test_source_drift_or_duplicate_transforms_refuse(self):
        for credentials, execution in ((self.credentials.replace('return verifyDropped(p)', 'return nil'), self.execution),
                                       (self.credentials, self.execution.replace('phase = "credentials"', 'phase = "different"')),
                                       (self.credentials, self.execution + '\n\tphase = "credentials"\n')):
            with self.subTest():
                with self.assertRaisesRegex(RuntimeError, 'exactly one source boundary'):
                    MODULE.variants(credentials, execution)


class Bytecode(unittest.TestCase):
    def test_control_import_leaves_immutable_copy_without_bytecode(self):
        with tempfile.TemporaryDirectory(prefix='payload-exec-control-import-') as directory:
            folder = Path(directory)
            for name in ('check-payload-exec-ci.py', 'check-identity-ci.py'):
                shutil.copyfile(ROOT / 'scripts' / name, folder / name)
            subprocess.run([sys.executable, '-c',
                            "import runpy,sys; runpy.run_path(sys.argv[1],run_name='control_import_smoke')",
                            str(folder / 'check-payload-exec-ci.py')], check=True, timeout=10)
            self.assertEqual(sorted(p.name for p in folder.iterdir()),
                             ['check-identity-ci.py', 'check-payload-exec-ci.py'])


class Budget(unittest.TestCase):
    def test_ci_default_and_explicit_local_eight_core_budget(self):
        spec = importlib.util.spec_from_file_location('exec_ci', ROOT / 'scripts/check-payload-exec-ci.py')
        ci = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(ci)
        self.assertEqual(ci.LIMITS['cpu.max'], '200000 100000')
        self.assertEqual(ci.cpu_budget(2), {'cpu.max': '200000 100000', 'GOMAXPROCS': '2', 'GOFLAGS': '-p=2'})
        self.assertEqual(ci.cpu_budget(8), {'cpu.max': '800000 100000', 'GOMAXPROCS': '8', 'GOFLAGS': '-p=8'})
        for value in (0, 9):
            with self.assertRaises(ValueError):
                ci.cpu_budget(value)


if __name__ == '__main__':
    unittest.main()
