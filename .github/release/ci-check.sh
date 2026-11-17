#!/bin/sh
# Release hook (ci-check): the gate's fallback when forge CI's result for the tag cannot be read.
# The shared release workflow runs this file, at a fixed path, when it exists.
# It runs what the forge's ordinary "check" job runs, unprivileged. The privileged-identity job
# needs a dedicated root runner and is not part of this fallback.
set -eu
# The ordinary checks, as CI runs them for an unprivileged user (the runner is not root).
# scripts/check-ci.py is the forge CI conductor and needs a Linux host set up to switch users.
sh scripts/check.sh
