// The API: obor web's JSON routes, typed.
import type {
  AgentDetail, EventsPage, Prompt, ProjectsEntry, ProjectView, Setup, TaskDetail,
} from "./types";

const enc = encodeURIComponent;

async function get<T>(path: string, signal?: AbortSignal): Promise<T> {
  const r = await fetch(path, { cache: "no-store", signal });
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
  let b: unknown = null;
  try { b = await r.json(); } catch { b = null; }
  if (!r.ok) throw new Error((b as { error?: string } | null)?.error || `${r.status} ${r.statusText}`);
  return b as T;
}

export const api = {
  projects: (s?: AbortSignal) => get<ProjectsEntry[]>("/api/projects", s),
  view: (p: string, s?: AbortSignal) => get<ProjectView>(`/api/view?project=${enc(p)}`, s),
  setup: (p: string, s?: AbortSignal) => get<Setup>(`/api/setup?project=${enc(p)}`, s),
  task: (p: string, t: string, s?: AbortSignal) => get<TaskDetail>(`/api/task?project=${enc(p)}&task=${enc(t)}`, s),
  agent: (p: string, a: string, s?: AbortSignal) => get<AgentDetail>(`/api/agent?project=${enc(p)}&agent=${enc(a)}`, s),
  job: (p: string, j: string, s?: AbortSignal) => get<Prompt>(`/api/job?project=${enc(p)}&job=${enc(j)}`, s),
  events: (p: string, since: number, s?: AbortSignal) =>
    get<EventsPage>(`/api/events?project=${enc(p)}&since=${since}`, s),
};
