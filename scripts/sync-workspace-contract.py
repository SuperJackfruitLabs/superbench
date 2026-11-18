#!/usr/bin/env python3
"""Pin only committed workspace v2 contract objects, independently of socket v1."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_REVISION = "83e51d583a71f668eec8c2093394613e002c1678"
treaty = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else ROOT.parent / "treaty"
requested = sys.argv[2] if len(sys.argv) > 2 else DEFAULT_REVISION
revision = subprocess.check_output(
    ["git", "-C", str(treaty), "rev-parse", requested + "^{commit}"], text=True
).strip()
destination = ROOT / "internal/protocolv2/testdata"
destination.mkdir(parents=True, exist_ok=True)
source = {
    "repository": "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
    "revision": revision,
    "status": "proposed workspace request contract; no lifecycle deployment implied",
    "files": {},
}
for canonical, local in {
    "schemas/superbench/operation.v2.json": "operation.v2.json",
    "fixtures/superbench-api/workspace-operations.json": "workspace-operations.json",
}.items():
    raw = subprocess.check_output(["git", "-C", str(treaty), "show", revision + ":" + canonical])
    (destination / local).write_bytes(raw)
    source["files"][canonical] = {"local": local, "sha256": hashlib.sha256(raw).hexdigest()}
(destination / "source.json").write_text(json.dumps(source, indent=2) + "\n")
