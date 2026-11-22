// node tests/conformance/opencode_guard.mjs <mode>: the OpenCode plugin's guard call (opencode/obor.js) against a stub
// kit whose bin/obor answers `hook pre-tool-use` in <mode> (allow, deny, crash, killed, slow, or no bin/obor for
// missing). Prints {"denied": bool, "reason": "…"} — what the plugin tells opencode about the call — on stdout, and
// exits 0; a guard registration that fails exits 1 with the error on stderr. The conformance program's OpenCode
// hook-transport row runs it once per mode (tests/conformance/hooks.go).
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const mode = process.argv[2];
if (!mode) {
  console.error("usage: opencode_guard.mjs <mode>");
  process.exit(2);
}
const root = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url)))); // the repo root
const base = fs.mkdtempSync(path.join(os.tmpdir(), "obor-conf-guard-"));
try {
  // the kit the plugin's kitRoot() finds: a copy of the shipped plugin, and the stub guard beside it
  const kit = path.join(base, "kit");
  fs.mkdirSync(path.join(kit, "opencode"), { recursive: true });
  fs.copyFileSync(path.join(root, "opencode", "obor.js"), path.join(kit, "opencode", "obor.js"));
  if (mode !== "missing") {
    fs.mkdirSync(path.join(kit, "bin"), { recursive: true });
    fs.writeFileSync(
      path.join(kit, "bin", "obor"),
      `#!/bin/bash
case "${mode}" in
  deny) echo "obor: denied by the conformance stub" >&2; exit 2 ;;
  crash) echo "obor: the stub crashed" >&2; exit 1 ;;
  killed) kill -9 $$ ;;
  slow) exec /bin/sleep 30 ;;
esac
exit 0
`,
      { mode: 0o755 },
    );
  }
  // the project: a lane with .obor/config.json above it
  const lane = path.join(base, "lane");
  fs.mkdirSync(path.join(lane, ".obor"), { recursive: true });
  fs.writeFileSync(path.join(lane, ".obor", "config.json"), "{}\n");
  const mod = await import(pathToFileURL(path.join(kit, "opencode", "obor.js")).href + "?" + mode);
  let guard;
  await mod.default.setup({
    location: { directory: lane },
    tool: {
      hook: async (name, fn) => {
        if (name === "execute.before") guard = fn;
      },
    },
    session: { hook: async () => {} },
  });
  if (!guard) throw new Error("the plugin registered no tool hook");
  const call = { tool: "write", sessionID: "conf", input: { path: "x.go", content: "" } };
  try {
    await guard(call);
    console.log(JSON.stringify({ denied: false, reason: "" }));
  } catch (e) {
    console.log(JSON.stringify({ denied: true, reason: String(e?.message ?? e) }));
  }
} finally {
  fs.rmSync(base, { recursive: true, force: true });
}
