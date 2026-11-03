// Routes: a hash as a page and the hash of every page a link points to.

export type Kind = "" | "p" | "s" | "t" | "a" | "j";
export interface Route {
  kind: Kind;
  project: string;
  id: string;
}

const KINDS = new Set(["", "p", "s", "t", "a", "j"]);

const dec = (s: string | undefined) => {
  try { return decodeURIComponent(s || ""); } catch { return s || ""; }
};

// parseRoute is a hash as its route: an unknown kind, or a project page with no project, is the fleet.
export function parseRoute(hash: string): Route {
  const parts = String(hash || "").replace(/^#\/?/, "").split("/");
  const kind = (parts[0] || "") as Kind;
  const project = dec(parts[1]);
  // a job's id keeps its slashes, encoded or not
  const id = parts.slice(2).map(dec).join("/");
  if (!KINDS.has(kind) || (kind !== "" && !project) || ("taj".includes(kind) && kind !== "" && !id)) {
    return { kind: "", project: "", id: "" };
  }
  return { kind, project, id };
}

const enc = encodeURIComponent;
export const href = {
  fleet: () => "#/",
  project: (p: string) => `#/p/${enc(p)}`,
  setup: (p: string) => `#/s/${enc(p)}`,
  task: (p: string, t: string) => `#/t/${enc(p)}/${enc(t)}`,
  agent: (p: string, a: string) => `#/a/${enc(p)}/${enc(a)}`,
  job: (p: string, j: string) => `#/j/${enc(p)}/${enc(j)}`,
};
