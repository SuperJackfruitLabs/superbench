#!/usr/bin/env python3
"""Pin the committed checkpoint manifest contract, independently of socket v1."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
treaty = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root.parent / "treaty"
revision = subprocess.check_output(
    ["git", "-C", str(treaty), "rev-parse", "HEAD"], text=True
).strip()
destination = root / "internal/checkpointset/testdata"
destination.mkdir(exist_ok=True)
manifest = {
    "repository": "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
    "revision": revision,
    "status": "proposed checkpoint extension; no suite consumer deployment implied",
    "files": {},
}
for source, local in {
    "schemas/superbench/checkpoint.v1.json": "checkpoint.v1.json",
    "fixtures/superbench-api/checkpoints.json": "checkpoints.json",
}.items():
    raw = subprocess.check_output(
        ["git", "-C", str(treaty), "show", revision + ":" + source]
    )
    (destination / local).write_bytes(raw)
    manifest["files"][source] = {"local": local, "sha256": hashlib.sha256(raw).hexdigest()}
(destination / "source.json").write_text(json.dumps(manifest, indent=2) + "\n")
