// Routes: a hash as a page and the hash of every page a link points to. Help ("h") is the one page of no project;
// "tasks" is a project's list of every task, "docs" its tracked markdown (the id: a doc's path, "" the list).

export type Kind = "" | "p" | "s" | "tasks" | "docs" | "t" | "a" | "j" | "h";
export interface Route {
  kind: Kind;
  project: string;
  id: string;
}

const KINDS = new Set(["", "p", "s", "tasks", "docs", "t", "a", "j"]); // the kinds parseRoute splits ("h", help, is matched whole)

const dec = (s: string | undefined) => {
  try { return decodeURIComponent(s || ""); } catch { return s || ""; }
};

// parseRoute is a hash as its route: an unknown kind, or a project page with no project, is the fleet; help has none.
export function parseRoute(hash: string): Route {
  if (/^#?\/?h\/?$/.test(String(hash || ""))) return { kind: "h", project: "", id: "" };
  const parts = String(hash || "").replace(/^#\/?/, "").split("/");
  const kind = (parts[0] || "") as Kind;
  const project = dec(parts[1]);
  // a job's id keeps its slashes, encoded or not
  const id = parts.slice(2).map(dec).join("/");
  const needsId = kind === "t" || kind === "a" || kind === "j";
  if (!KINDS.has(kind) || (kind !== "" && !project) || (needsId && !id)) {
    return { kind: "", project: "", id: "" };
  }
  return { kind, project, id };
}

const enc = encodeURIComponent;
export const href = {
  fleet: () => "#/",
  help: () => "#/h",
  project: (p: string) => `#/p/${enc(p)}`,
  setup: (p: string) => `#/s/${enc(p)}`,
  tasks: (p: string) => `#/tasks/${enc(p)}`,
  // docs is the docs page, at one doc when path is given (its slashes kept)
  docs: (p: string, path = "") => `#/docs/${enc(p)}` + (path ? "/" + path.split("/").map(enc).join("/") : ""),
  task: (p: string, t: string) => `#/t/${enc(p)}/${enc(t)}`,
  agent: (p: string, a: string) => `#/a/${enc(p)}/${enc(a)}`,
  job: (p: string, j: string) => `#/j/${enc(p)}/${enc(j)}`,
};

// pageKey is what remounts the routed page when it changes: the route and every navigation (n) for most pages, so a
// link to the shown page reads it again; the docs page keys on its project only, so moving from doc to doc keeps it
// mounted (its index's filter, folds and scroll stay).
export function pageKey(r: Route, n: number): string {
  if (r.kind === "docs") return `docs/${r.project}`;
  return `${r.kind}/${r.project}/${r.id}/${n}`;
}
