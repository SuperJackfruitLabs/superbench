// Developer-only mirror refresh. It neither contacts a service nor enables a viewer.
import { readFileSync, writeFileSync, mkdirSync } from "node:fs";
import { resolve, join } from "node:path";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { stripTypeScriptTypes } from "node:module";
const args = process.argv.slice(2);
if (args.length !== 2 || args[0] !== "--source")
  throw new Error(
    "usage: node scripts/sync-reader-contract.mjs --source /absolute/treaty",
  );
const source = resolve(args[1]),
  root = resolve(import.meta.dirname, ".."),
  dest = join(root, "web/contract");
if (
  execFileSync("git", ["status", "--porcelain"], {
    cwd: source,
    encoding: "utf8",
  }).trim()
)
  throw new Error("contract source must be committed and clean");
const revision = execFileSync("git", ["rev-parse", "HEAD"], {
  cwd: source,
  encoding: "utf8",
}).trim();
const hash = (value) => createHash("sha256").update(value).digest("hex");
const mirrored = {};
mkdirSync(dest, { recursive: true });
for (const path of [
  "schemas/superbench/observation-request.v1.json",
  "schemas/superbench/observation-response.v1.json",
  "fixtures/superbench-api/observations.json",
]) {
  const raw = readFileSync(join(source, path));
  const name = path.split("/").at(-1);
  writeFileSync(join(dest, name), raw);
  mirrored[path] = { file: name, sha256: hash(raw) };
}
const lexer = readFileSync(
  join(source, "scripts/superbench-workspace-response-decode.ts"),
  "utf8",
);
const validation = readFileSync(
  join(source, "scripts/superbench-observation-validation.ts"),
  "utf8",
);
writeFileSync(join(dest, "decode-source.txt"), lexer);
writeFileSync(join(dest, "validation-source.txt"), validation);
const generated = {};
function emit(name, value) {
  value = value
    .split("\n")
    .map((line) => line.trimEnd())
    .join("\n");
  writeFileSync(join(dest, name), value);
  generated[name] = hash(value);
}
emit("decode.mjs", stripTypeScriptTypes(lexer));
let portable = validation
  .replace('import { createHash } from "node:crypto";\n', "")
  .replace('"./superbench-workspace-response-decode"', '"./decode.mjs"')
  .replace(
    "export function observationErrors(",
    "export async function observationErrors(",
  )
  .replace(
    "): string[] {\n  const body = record(value)",
    "): Promise<string[]> {\n  const body = record(value)",
  )
  .replace('Buffer.from(raw, "base64")', "decodeBase64(raw)")
  .replace('bytes.toString("base64")', "encodeBase64(bytes)")
  .replace(
    'createHash("sha256").update(bytes).digest("hex")',
    "await digest(bytes)",
  );
if (portable.includes("createHash(") || portable.includes("Buffer."))
  throw new Error("unreviewed portable codec source");
portable +=
  '\nfunction decodeBase64(value: string): Uint8Array {try {return Uint8Array.from(atob(value), c=>c.charCodeAt(0));}catch {return new Uint8Array();}}\nfunction encodeBase64(value: Uint8Array): string {return btoa(String.fromCharCode(...value));}\nasync function digest(value: Uint8Array): Promise<string> {return Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256",value)),b=>b.toString(16).padStart(2,"0")).join("");}\n';
emit("validate.mjs", stripTypeScriptTypes(portable));
writeFileSync(
  join(dest, "source.json"),
  JSON.stringify(
    {
      status:
        "PROPOSED contract; fixture-only consumer; no runtime authority proof",
      repository: "https://forge.superjackfruit.com/SuperJackfruitLabs/treaty",
      revision,
      files: mirrored,
      codec_sources: {
        "decode-source.txt": hash(lexer),
        "validation-source.txt": hash(validation),
      },
      generated,
      adaptation:
        "Type erasure plus browser WebCrypto/base64 portability only. Shared corpus tests actual generated codec. No live adapter.",
    },
    null,
    2,
  ) + "\n",
);
