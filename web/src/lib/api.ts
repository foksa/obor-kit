// The API: obor web's JSON routes, typed.
import type {
  AgentDetail, DocHits, DocList, DocText, EventsPage, Help, Prompt, ProjectsEntry, ProjectView, Setup, TaskDetail, TaskRow,
} from "./types";

const enc = encodeURIComponent;

// buildHeader is the header every /api/ response carries: the id of the page obor web serves (a hash of its index.html).
const buildHeader = "X-Obor-Build";

// loadedBuild is the build this tab loaded with: the first response's header. A later response naming another build
// comes from a newer obor web, whose page this tab does not run, so the tab reloads itself (the hash route survives
// the reload) instead of drawing with stale scripts. Nothing names a build, the tab keeps its own.
let loadedBuild: string | null = null;

// followBuild is every response's check: the first one tells the tab which build it loaded, and one from another build
// reloads the page once. A response without the header is an obor web from before it: no reload.
function followBuild(r: Response): void {
  const id = r.headers.get(buildHeader);
  if (id === null) return;
  if (loadedBuild === null) loadedBuild = id;
  else if (id !== loadedBuild) location.reload();
}

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, { cache: "no-store", signal });
  followBuild(r);
  let body: unknown = null;
  try { body = await r.json(); } catch { body = null; }
  if (!r.ok) throw new Error((body as { error?: string } | null)?.error || `${r.status} ${r.statusText}`);
  return body as T;
}

// post sends body to a write route; its error is serve's refusal (or why obor web refused it).
export async function post<T>(path: string, body: unknown): Promise<T> {
  const r = await fetch(path, {
    method: "POST", cache: "no-store", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
  });
  followBuild(r);
  let b: unknown = null;
  try { b = await r.json(); } catch { b = null; }
  if (!r.ok) throw new Error((b as { error?: string } | null)?.error || `${r.status} ${r.statusText}`);
  return b as T;
}

export const api = {
  projects: (s?: AbortSignal) => get<ProjectsEntry[]>("/api/projects", s),
  view: (p: string, s?: AbortSignal) => get<ProjectView>(`/api/view?project=${enc(p)}`, s),
  setup: (p: string, s?: AbortSignal) => get<Setup>(`/api/setup?project=${enc(p)}`, s),
  tasks: (p: string, s?: AbortSignal) => get<TaskRow[]>(`/api/tasks?project=${enc(p)}`, s),
  docs: (p: string, s?: AbortSignal) => get<DocList>(`/api/docs?project=${enc(p)}`, s),
  doc: (p: string, path: string, s?: AbortSignal) => get<DocText>(`/api/doc?project=${enc(p)}&path=${enc(path)}`, s),
  docsearch: (p: string, q: string, s?: AbortSignal) =>
    get<DocHits>(`/api/docsearch?project=${enc(p)}&q=${enc(q)}`, s),
  task: (p: string, t: string, s?: AbortSignal) => get<TaskDetail>(`/api/task?project=${enc(p)}&task=${enc(t)}`, s),
  agent: (p: string, a: string, s?: AbortSignal) => get<AgentDetail>(`/api/agent?project=${enc(p)}&agent=${enc(a)}`, s),
  job: (p: string, j: string, s?: AbortSignal) => get<Prompt>(`/api/job?project=${enc(p)}&job=${enc(j)}`, s),
  help: (s?: AbortSignal) => get<Help>("/api/help", s),
  events: (p: string, since: number, s?: AbortSignal) =>
    get<EventsPage>(`/api/events?project=${enc(p)}&since=${since}`, s),
};
