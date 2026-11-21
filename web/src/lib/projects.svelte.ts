// The projects list: one /api/projects read for every page that shows it (the shell's sidebar and the fleet page), so
// the fleet page reads it once per refresh, not twice. The first hold starts it, the last release stops it.
import { Live } from "./live.svelte";
import { api } from "./api";
import type { ProjectsEntry } from "./types";

export const projects = new Live<ProjectsEntry[]>((s) => api.projects(s));

let holders = 0;

// holdProjects keeps the projects read running until the function it returns is called (a second call does nothing).
export function holdProjects(): () => void {
  if (holders++ === 0) projects.start();
  let held = true;
  return () => {
    if (!held) return;
    held = false;
    if (--holders === 0) projects.stop();
  };
}
