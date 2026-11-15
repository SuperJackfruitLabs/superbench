#!/usr/bin/env python3
"""Pin the separate PROPOSED cancellation extension; do not change v1/v2 pins."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

if len(sys.argv) != 3:
    raise SystemExit("usage: sync-cancellation.py TREATY_CHECKOUT COMMITTED_REVISION")
root = Path(__file__).resolve().parents[1]
treaty = Path(sys.argv[1]).resolve()
revision = subprocess.check_output(
    ["git", "-C", str(treaty), "rev-parse", sys.argv[2] + "^{commit}"], text=True
).strip()
paths = [
    "schemas/superbench/cancel-operation.v2.json",
    "schemas/superbench/cancel-response.v2.json",
    "fixtures/superbench-api/cancellation.json",
    "fixtures/superbench-api/source-digests.json",
    "fixtures/superbench-api/cancellation-lifecycle.json",
]
objects = {
    name: subprocess.check_output(["git", "-C", str(treaty), "show", revision + ":" + name])
    for name in paths
}
manifest = {
    "repository": "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
    "revision": revision,
    "status": "PROPOSED; private conformance only, no enabled route or fencing proof",
    "files": {},
}
destination = root / "internal/protocolv2/testdata"
for name, raw in objects.items():
    local = Path(name).name
    manifest["files"][name] = {"local": local, "sha256": hashlib.sha256(raw).hexdigest()}
    (destination / local).write_bytes(raw)
(destination / "cancellation-source.json").write_text(json.dumps(manifest, indent=2) + "\n")
