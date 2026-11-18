#!/usr/bin/env python3
"""Pin committed workspace response objects without changing request/v1 pins."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

if len(sys.argv) != 3:
    raise SystemExit("usage: sync-workspace-responses.py TREATY_CHECKOUT COMMITTED_REVISION")
root = Path(__file__).resolve().parents[1]
treaty = Path(sys.argv[1]).resolve()
revision = subprocess.check_output(
    ["git", "-C", str(treaty), "rev-parse", sys.argv[2] + "^{commit}"], text=True
).strip()
destination = root / "internal/protocolv2/testdata"
manifest = {
    "repository": "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
    "revision": revision,
    "status": "proposed workspace response contract; no suite deployment implied",
    "files": {},
}
objects = {}
for canonical, local in {
    "schemas/superbench/response.v2.json": "response.v2.json",
    "fixtures/superbench-api/workspace-responses.json": "workspace-responses.json",
    "fixtures/superbench-api/workspace-lifecycle.json": "workspace-lifecycle.json",
    "fixtures/superbench-api/reclaim.json": "reclaim.json",
    "fixtures/superbench-api/reclaim-lifecycle.json": "reclaim-lifecycle.json",
}.items():
    raw = subprocess.check_output(
        ["git", "-C", str(treaty), "show", revision + ":" + canonical]
    )
    objects[local] = raw
    manifest["files"][canonical] = {
        "local": local, "sha256": hashlib.sha256(raw).hexdigest()
    }
# Resolve all objects before updating any local copy.
for local, raw in objects.items():
    (destination / local).write_bytes(raw)
(destination / "response-source.json").write_text(json.dumps(manifest, indent=2) + "\n")
