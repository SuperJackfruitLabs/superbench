#!/usr/bin/env python3
"""Compile private causal overlays; original tracked Go source is never edited."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

NAMES = ('plain', 'no-groups', 'verify-fault', 'verify-mutant', 'late-fd', 'late-fd-mutant')


def replace_once(raw, old, new):
    if raw.count(old) != 1:
        raise RuntimeError('causal overlay no longer matches exactly one source boundary')
    return raw.replace(old, new)


def variants(credentials, execution):
    groups = '\tif err := syscall.Setgroups([]int{}); err != nil {\n\t\treturn errors.Join(errCredentials, err)\n\t}\n'
    wrong = replace_once(credentials, 'func verifyDropped(p Policy) error {\n',
                         'func verifyDropped(p Policy) error {\n\tp.PayloadUID++ // PRIVATE QA: mismatched policy readback, not a kernel drop fault.\n')
    bypass = replace_once(wrong, '\treturn verifyDropped(p)\n', '\treturn nil // PRIVATE QA mutation: remove final readback.\n')
    # enterPayload repeats the readback right before exec; the mutant removes
    # both, so only the readback can be what refuses the verify-fault variant.
    readback = '\t*phase = "credentials"\n\tif err := verifyDropped(p); err != nil {\n\t\treturn err\n\t}\n'
    unchecked = replace_once(execution, readback, '\t// PRIVATE QA mutation: remove the pre-exec readback.\n')
    late = replace_once(execution, '\tphase = "credentials"\n',
                        '\t// PRIVATE QA: root-only canary opened after both initial sweeps.\n'
                        '\tif _, err := syscall.Open("/run/superbench-exec-fixture-canary", syscall.O_RDONLY, 0); err != nil { return err }\n'
                        '\tphase = "credentials"\n')
    final = '\t*phase = "descriptors"\n\tif err := descriptorsCloseOnExec(); err != nil {\n\t\treturn err\n\t}\n'
    # The executable-selection suffix has exactly one final sweep, in the shared
    # enterPayload after its credential check. Its next statement may be finite
    # stdin binding (or an interposed statement), not exec.
    boundary = '\tphase = "executable"\n\tpath := "/usr/bin/git"\n'
    if late.count(boundary) != 1:
        raise RuntimeError('causal overlay no longer matches exactly one source boundary')
    before_executable, executable = late.split(boundary)
    late_mutant = before_executable + boundary + replace_once(executable, final, '')
    return {'plain': (credentials, execution),
            'no-groups': (replace_once(credentials, groups, ''), execution),
            'verify-fault': (wrong, execution), 'verify-mutant': (bypass, unchecked),
            'late-fd': (credentials, late), 'late-fd-mutant': (credentials, late_mutant)}


def main():
    source, work, go = Path(sys.argv[1]).resolve(strict=True), Path(sys.argv[2]), sys.argv[3]
    if os.getuid() == 0 or os.geteuid() == 0:
        raise RuntimeError('variant compiler must be ordinary user')
    work.mkdir(mode=0o700)
    files = [source / 'internal/payloadexec/credentials_linux.go', source / 'internal/payloadexec/exec_linux.go']
    original = [p.read_bytes() for p in files]
    result = {}
    environment = dict(os.environ, CGO_ENABLED='0', GOTOOLCHAIN='local')
    subprocess.run([sys.executable, '-B', '-m', 'unittest', 'discover', '-s', 'scripts', '-p', 'payload_exec_variants_test.py'],
                   cwd=source, env=environment, check=True, timeout=30)
    for name, contents in variants(*(raw.decode() for raw in original)).items():
        variant = work / name
        variant.mkdir(mode=0o700)
        mapping = {}
        for path, content in zip(files, contents):
            target = variant / path.name
            target.write_text(content)
            mapping[str(path)] = str(target)
        overlay = variant / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': mapping}))
        binary = work / (name + '.bin')
        command = [go, 'build']
        if name != 'plain':
            command.append('-overlay=' + str(overlay))
        command += ['-o', str(binary), './cmd/bench-payload']
        subprocess.run(command,
                       cwd=source, env=environment, check=True, timeout=180)
        result[name] = {'command': command, 'sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
                        'overlays': {str(Path(k).relative_to(source)): hashlib.sha256(Path(v).read_bytes()).hexdigest() for k, v in mapping.items()}}
    subprocess.run([go, 'build', '-o', str(work / 'probe.bin'), './scripts/fixtures/payload-exec-probe'],
                   cwd=source, env=environment, check=True, timeout=180)
    if [p.read_bytes() for p in files] != original:
        raise RuntimeError('original Go source changed')
    (work / 'variants.json').write_text(json.dumps(result, indent=2) + '\n')


if __name__ == '__main__':
    main()
