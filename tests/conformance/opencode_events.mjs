// node tests/conformance/opencode_events.mjs <tool> <args-json> <namespaced>: the Claude Code PreToolUse events the
// OpenCode plugin's toolEvents maps one tool call to, as a JSON array ([] when the call has none). <namespaced> says
// whether the tool id is one ctx.tool.list() gives a namespace (an MCP tool's id). Prints the events and exits 0; a
// plugin that does not map the call at all exits 1 with the error on stderr. The conformance program's shared
// transport row runs it once per call (tests/conformance/guard_transport.go).
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const [, , tool, argsJSON, namespaced] = process.argv;
if (!tool) {
  console.error("usage: opencode_events.mjs <tool> <args-json> <namespaced>");
  process.exit(2);
}
const root = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url)))); // the repo root
try {
  const mod = await import(pathToFileURL(path.join(root, "opencode", "obor.js")).href);
  const events = await mod.toolEvents(tool, JSON.parse(argsJSON ?? "{}"), root, "conf", () => namespaced === "true");
  console.log(JSON.stringify(events));
} catch (e) {
  console.error(String(e?.message ?? e));
  process.exit(1);
}
