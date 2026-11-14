#!/usr/bin/env python3
"""Pin committed canonical treaty data without consuming TypeScript at runtime."""
import hashlib
import json
from pathlib import Path
import subprocess
import sys

root = Path(__file__).resolve().parents[1]
treaty = Path(sys.argv[1]).resolve() if len(sys.argv) > 1 else root.parent / "treaty"
files = {
    "schemas/superbench/operation.v1.json": "operation.v1.json",
    "fixtures/superbench-api/operations.json": "operations.json",
}
revision = subprocess.check_output(
    ["git", "-C", str(treaty), "rev-parse", "HEAD"], text=True
).strip()
manifest = {
    "repository": "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
    "revision": revision,
    "status": "proposal; no suite consumer deployment implied",
    "files": {},
}
for source, target in files.items():
    # Read the named revision itself, never an uncommitted worktree carrying it.
    data = subprocess.check_output(
        ["git", "-C", str(treaty), "show", revision + ":" + source]
    )
    (root / "internal/protocol/testdata" / target).write_bytes(data)
    manifest["files"][source] = {
        "local": target,
        "sha256": hashlib.sha256(data).hexdigest(),
    }
(root / "internal/protocol/testdata/source.json").write_text(
    json.dumps(manifest, indent=2) + "\n"
)
