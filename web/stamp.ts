// The source stamp (T-760): a hash of web/'s sources that `vite build` writes next to the bundle as source.sha256.
// internal/web's TestDistFresh computes the same hash in Go and fails when they differ: web/ changed and `make web`
// was not run. It is the gate's only guard (the gate has no network, so it never builds the page itself).
//
// The hash, both sides alike: every regular file under web/, skipping node_modules and any name starting with "."; per
// file "<slash path relative to web/>\0<sha256 hex of its bytes>\n", sorted by path; the sha256 hex of all of it.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep } from "node:path";

export function sourceHash(root: string): string {
  const files: string[] = [];
  const walk = (dir: string) => {
    for (const e of readdirSync(dir, { withFileTypes: true })) {
      if (e.name.startsWith(".") || e.name === "node_modules") continue;
      const p = join(dir, e.name);
      if (e.isDirectory()) walk(p);
      else if (e.isFile()) files.push(p);
    }
  };
  walk(root);
  const rel = (p: string) => relative(root, p).split(sep).join("/");
  const lines = files
    .map((p) => [rel(p), createHash("sha256").update(readFileSync(p)).digest("hex")] as const)
    .sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0))
    .map(([p, h]) => `${p}\0${h}\n`);
  return createHash("sha256").update(lines.join("")).digest("hex");
}
