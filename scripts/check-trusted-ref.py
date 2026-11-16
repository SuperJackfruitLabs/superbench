"""Read-only CI scheduling gate; runner admission remains independently required."""
import http.client
import json
import os
import re
import urllib.error
import urllib.request

API = 'https://forge.superjackfruit.com/api/v1'
REPOSITORY = 'SuperJackfruitLabs/superbench'
MAX_RESPONSE = 64 * 1024


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise urllib.error.HTTPError(req.full_url, code, 'redirect refused', headers, fp)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON field')
        result[key] = value
    return result


def trusted_ref(environment):
    # Refuse all PRs (including same-repository PRs), tags and unknown metadata
    # before HTTP or token access. Never derive an authority URL from payloads.
    if environment.get('CI_GATE_EVENT') != 'push' or environment.get('CI_GATE_REF') != 'refs/heads/main':
        return False, 'event/ref refused'
    if environment.get('CI_GATE_API_URL') != API or environment.get('CI_GATE_REPOSITORY') != REPOSITORY:
        return False, 'API/repository refused'
    sha = environment.get('CI_GATE_SHA', '')
    token = environment.get('CI_GATE_TOKEN', '')
    if not re.fullmatch('[0-9a-f]{40}', sha) or not token or any(c.isspace() for c in token):
        return False, 'identity/token missing'
    request = urllib.request.Request(API + '/repos/' + REPOSITORY + '/branches/main',
                                     headers={'Authorization': 'token ' + token,
                                              'Accept': 'application/json'})
    # Explicit TLS origin, no ambient proxy or redirect credential forwarding.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(request, timeout=10) as response:
            if response.status != 200 or response.geturl() != request.full_url:
                return False, 'HTTP response refused'
            raw = response.read(MAX_RESPONSE + 1)
        if len(raw) > MAX_RESPONSE:
            return False, 'response bound exceeded'
        branch = json.loads(raw.decode('utf-8'), object_pairs_hook=unique_object)
        if not isinstance(branch, dict) or branch.get('name') != 'main' or branch.get('protected') is not True:
            return False, 'branch protection refused'
        commit = branch.get('commit')
        if not isinstance(commit, dict) or commit.get('id') != sha:
            return False, 'current main commit mismatch'
    except (OSError, ValueError, RecursionError, http.client.HTTPException):
        return False, 'API read refused'
    return True, 'protected current main verified'


def main():
    accepted, reason = trusted_ref(os.environ)
    output = os.environ.get('FORGEJO_OUTPUT') or os.environ['GITHUB_OUTPUT']
    with open(output, 'a', encoding='utf-8') as stream:
        stream.write('trusted_ref=' + ('true' if accepted else 'false') + '\n')
    # Closed diagnostic only: no token, response body or exception text.
    print('Privileged scheduling gate: ' + reason + '; no privileged execution proof.')


if __name__ == '__main__':
    main()
