"""Trigger policy only: no requests, compilation, job cancellation or privileges."""
import fnmatch
import json
from pathlib import Path
import unittest

WORKFLOW = Path(__file__).resolve().parent.parent / '.forgejo/workflows/ci.yml'


class CITriggers(unittest.TestCase):
    def setUp(self):
        self.text = WORKFLOW.read_text()
        declarations = [line[4:] for line in self.text.splitlines() if line.startswith('on: ')]
        self.assertEqual(len(declarations), 1)
        # JSON flow syntax is also valid YAML; stdlib parsing needs no dependency.
        self.triggers = json.loads(declarations[0])

    def selected(self, event, ref, action='', target='main'):
        rule = self.triggers.get(event)
        if rule is None:
            return False
        if event == 'pull_request':
            # No branch filter: all targets keep open/update/reopen checks.
            return action in rule['types'] and ('branches' not in rule or target in rule['branches'])
        if event != 'push':
            return False
        if ref.startswith('refs/heads/'):
            return any(fnmatch.fnmatchcase(ref[11:], pattern) for pattern in rule.get('branches', ['**']))
        if ref.startswith('refs/tags/'):
            return any(fnmatch.fnmatchcase(ref[10:], pattern) for pattern in rule.get('tags', ['**']))
        return False

    def test_exact_trigger_policy_preserves_tags_and_all_pr_targets(self):
        self.assertEqual(self.triggers, {
            'push': {'branches': ['main'], 'tags': ['**']},
            'pull_request': {'types': ['opened', 'synchronize', 'reopened']},
        })
        self.assertNotIn('pull_request_target', self.triggers)

    def test_actual_trigger_matrix_has_no_duplicate_topic_push(self):
        for event, ref, action, target, expected in (
            ('push', 'refs/heads/main', '', '', True),
            ('push', 'refs/heads/codex/topic', '', '', False),
            ('push', 'refs/heads/release', '', '', False),
            ('push', 'refs/tags/v0.0.1', '', '', True),
            ('push', 'refs/tags/snapshot', '', '', True),
            ('push', 'refs/tags/group/nested-tag', '', '', True),
            ('push', 'refs/pull/62/head', '', '', False),
            ('pull_request_target', 'refs/heads/main', 'opened', 'main', False),
            ('workflow_dispatch', 'refs/heads/main', '', '', False),
            ('pull_request', 'refs/pull/62/head', 'closed', 'main', False),
            ('pull_request', 'refs/pull/62/head', 'edited', 'main', False),
        ):
            with self.subTest(event=event, ref=ref, action=action, target=target):
                self.assertEqual(self.selected(event, ref, action, target), expected)
        for action in ('opened', 'synchronize', 'reopened'):
            for target in ('main', 'codex/stack-base', 'release'):
                with self.subTest(action=action, target=target):
                    self.assertTrue(self.selected('pull_request', 'refs/pull/62/head', action, target))


if __name__ == '__main__':
    unittest.main()
