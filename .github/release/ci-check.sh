#!/bin/sh
# Release hook (ci-check): the gate's fallback when forge CI's result for the tag cannot be read.
# The shared release workflow runs this file, at a fixed path, when it exists.
# It runs what the forge's ordinary "check" job runs, unprivileged. The privileged-identity job
# needs a dedicated root runner and is not part of this fallback.
set -eu
# The ordinary checks, as CI runs them for an unprivileged user (the runner is not root).
# scripts/check-ci.py is the forge CI conductor and needs a Linux host set up to switch users.
#
# The conductor starts scripts/check.sh through Python's subprocess, which closes every
# descriptor above 2. A hosted runner can hand this job descriptors without close-on-exec, and
# Go keeps those open across exec, so they reach the clean worker that internal/cleanworker's
# tests launch, which rightly refuses anything but 0, 1 and 2. Start the checks the same way the
# conductor does, so they see the same descriptors; the worker's check stays as strict as it is.
exec python3 -c 'import subprocess, sys; sys.exit(subprocess.run(["sh", "scripts/check.sh"]).returncode)'
