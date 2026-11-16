"""API scheduling-gate tests: no live requests, runner or privilege changes."""
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import urllib.error

SOURCE = Path(__file__).with_name('check-trusted-ref.py')


def load():
    spec = importlib.util.spec_from_file_location('trusted_ref', SOURCE)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TrustedRef(unittest.TestCase):
    def setUp(self):
        self.module = load()
        self.environment = {'CI_GATE_EVENT': 'push', 'CI_GATE_REF': 'refs/heads/main',
                            'CI_GATE_API_URL': self.module.API,
                            'CI_GATE_REPOSITORY': self.module.REPOSITORY,
                            'CI_GATE_SHA': 'a' * 40, 'CI_GATE_TOKEN': 'private-fixture-token'}
        self.url = self.module.API + '/repos/' + self.module.REPOSITORY + '/branches/main'

    def response(self, raw, status=200, url=None):
        response = mock.MagicMock()
        response.status = status
        response.geturl.return_value = url or self.url
        response.read.return_value = raw
        response.__enter__.return_value = response
        return response

    def test_exact_protected_current_main_read_only_request_and_bounded_read(self):
        response = self.response(json.dumps({'name': 'main', 'protected': True,
                                             'commit': {'id': 'a' * 40}}).encode())
        with mock.patch.object(self.module.urllib.request, 'build_opener') as build:
            build.return_value.open.return_value = response
            accepted, _ = self.module.trusted_ref(self.environment)
        self.assertTrue(accepted)
        request = build.return_value.open.call_args.args[0]
        self.assertEqual(request.get_method(), 'GET')
        self.assertEqual(request.full_url, self.url)
        self.assertEqual(request.headers['Authorization'], 'token private-fixture-token')
        self.assertEqual(build.return_value.open.call_args.kwargs, {'timeout': 10})
        response.read.assert_called_once_with(65537)
        self.assertEqual(build.call_args.args[0].proxies, {})
        self.assertIsInstance(build.call_args.args[1], self.module.NoRedirect)

    def test_untrusted_events_refs_and_metadata_never_open_http(self):
        for key, value in (
            ('CI_GATE_EVENT', 'pull_request'), ('CI_GATE_EVENT', 'pull_request_target'),
            ('CI_GATE_EVENT', 'workflow_dispatch'), ('CI_GATE_EVENT', ''),
            ('CI_GATE_REF', 'refs/heads/topic'), ('CI_GATE_REF', 'refs/tags/v1.0.0'),
            ('CI_GATE_REF', 'refs/pull/47/head'), ('CI_GATE_REF', ''),
            ('CI_GATE_REPOSITORY', 'attacker/superbench'), ('CI_GATE_REPOSITORY', ''),
            ('CI_GATE_API_URL', 'http://forge.superjackfruit.com/api/v1'),
            ('CI_GATE_API_URL', 'https://elsewhere.test/api/v1'),
            ('CI_GATE_API_URL', self.module.API + '?redirect=elsewhere'),
            ('CI_GATE_SHA', ''), ('CI_GATE_SHA', 'A' * 40),
            ('CI_GATE_TOKEN', ''), ('CI_GATE_TOKEN', 'invalid\nheader'),
        ):
            with self.subTest(key=key, value=value):
                environment = {**self.environment, key: value}
                with mock.patch.object(self.module.urllib.request, 'build_opener') as build:
                    self.assertFalse(self.module.trusted_ref(environment)[0])
                build.assert_not_called()

    def test_server_evidence_must_be_exact_bool_current_commit_and_strict_bounded_json(self):
        valid = {'name': 'main', 'protected': True, 'commit': {'id': 'a' * 40}}
        cases = [
            {**valid, 'protected': False}, {**valid, 'protected': 1},
            {**valid, 'protected': 'true'}, {k: v for k, v in valid.items() if k != 'protected'},
            {**valid, 'name': 'topic'}, {**valid, 'commit': {'id': 'b' * 40}},
            {**valid, 'commit': {}}, {**valid, 'commit': None},
            {k: v for k, v in valid.items() if k != 'commit'}, [], None,
        ]
        raws = [json.dumps(case).encode() for case in cases]
        raws += [b'{', b'\xff', b' ' * 65537,
                 b'{"name":"main","protected":false,"protected":true,"commit":{"id":"' + b'a' * 40 + b'"}}']
        for raw in raws:
            with self.subTest(raw=raw[:80]):
                with mock.patch.object(self.module.urllib.request, 'build_opener') as build:
                    build.return_value.open.return_value = self.response(raw)
                    self.assertFalse(self.module.trusted_ref(self.environment)[0])

    def test_http_status_redirect_network_and_unauthorized_refuse_without_credential_log(self):
        valid = json.dumps({'name': 'main', 'protected': True, 'commit': {'id': 'a' * 40}}).encode()
        for response in (self.response(valid, status=401), self.response(valid, status=302),
                         self.response(valid, url='https://elsewhere.test/')):
            with mock.patch.object(self.module.urllib.request, 'build_opener') as build:
                build.return_value.open.return_value = response
                self.assertFalse(self.module.trusted_ref(self.environment)[0])
        for error in (TimeoutError('private error'), urllib.error.URLError('private error'),
                      urllib.error.HTTPError(self.url, 403, 'private error', {}, None),
                      self.module.http.client.IncompleteRead(b'partial'), RecursionError('invalid nesting')):
            with mock.patch.object(self.module.urllib.request, 'build_opener') as build:
                build.return_value.open.side_effect = error
                self.assertEqual(self.module.trusted_ref(self.environment), (False, 'API read refused'))
        redirect = self.module.NoRedirect()
        request = self.module.urllib.request.Request(self.url, headers={'Authorization': 'token fixture'})
        with self.assertRaises(urllib.error.HTTPError):
            redirect.redirect_request(request, None, 302, 'redirect', {}, 'https://elsewhere.test/')

    def test_false_output_does_not_fail_ordinary_suite_or_print_token_and_body(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / 'output'
            environment = {**self.environment, 'FORGEJO_OUTPUT': str(output)}
            with mock.patch.dict(os.environ, environment, clear=True), \
                    mock.patch.object(self.module.urllib.request, 'build_opener') as build, \
                    contextlib.redirect_stdout(io.StringIO()) as diagnostic:
                build.return_value.open.side_effect = TimeoutError('private-fixture-token')
                self.module.main()
            self.assertEqual(output.read_text(), 'trusted_ref=false\n')
            self.assertNotIn('private-fixture-token', diagnostic.getvalue())
            self.assertIn('no privileged execution proof', diagnostic.getvalue())

    def test_workflow_token_is_step_scoped_and_output_is_actual_dependency(self):
        text = (SOURCE.parent.parent / '.forgejo/workflows/ci.yml').read_text()
        ordinary, privileged = text.split('  privileged-identity:', 1)
        preflight = ordinary.split('      - id: trusted_ref', 1)[1].split('      - uses:', 1)[0]
        self.assertIn('CI_GATE_TOKEN: ${{ github.token }}', preflight)
        self.assertEqual(text.count('${{ github.token }}'), 1)
        self.assertIn('run: python3 scripts/check-trusted-ref.py', preflight)
        self.assertIn('trusted_ref: ${{ steps.trusted_ref.outputs.trusted_ref }}', ordinary)
        self.assertIn('needs: check', privileged)
        self.assertIn("needs.check.outputs.trusted_ref == 'true'", privileged)


if __name__ == '__main__':
    unittest.main()
