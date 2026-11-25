// node tests/conformance/opencode_row.mjs <tool>: whether the OpenCode plugin's table has a row for one of OpenCode's
// own tools (opencode/obor.js hasRow: a PASS row, a DENY row, or one opencodeEvents maps), as JSON true or false. The
// live tier's OC-L7 asks it after toolEvents gives a single unguarded event, so a row that deliberately denies is a
// classified tool, not a drift WARN; a name with no row is the WARN.
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const [, , tool] = process.argv;
if (!tool) {
  console.error("usage: opencode_row.mjs <tool>");
  process.exit(2);
}
const root = path.dirname(path.dirname(path.dirname(fileURLToPath(import.meta.url)))); // the repo root
try {
  const mod = await import(pathToFileURL(path.join(root, "opencode", "obor.js")).href);
  console.log(JSON.stringify(mod.hasRow(tool)));
} catch (e) {
  console.error(String(e?.message ?? e));
  process.exit(1);
}
