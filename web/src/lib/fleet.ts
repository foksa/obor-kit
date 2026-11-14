// fleet.ts: what the nav and the fleet page read of /api/projects: a project's state dot, and the agents at work as the
// fleet page lists them.
import { roleName } from "./format";
import type { ProjectsEntry, Role } from "./types";

// ProjectState is a project's dot in the nav: the worst of its facts first, then whether anything works in it.
export type ProjectState = "fail" | "down" | "needs-you" | "working" | "free";

// projectState is a project's state dot: fail (its view could not be built), serve down, needs you, working (a busy
// lane or an agent at work, the lead included: the overview's working count) and free otherwise. The busy/total beside
// the name stays a lane count.
export function projectState(p: ProjectsEntry): ProjectState {
  if (p.error) return "fail";
  if (!p.up) return "down";
  if (p.needs_you > 0) return "needs-you";
  if (p.busy > 0 || p.working > 0) return "working";
  return "free";
}

// FleetRole is one row of the fleet page's agents at work: its project, the agent's role and the role as the row words
// it ("lead", "deep dive").
export interface FleetRole {
  project: string;
  role: Role;
  label: string;
}

// fleetRoles is every project's agents at work as the fleet page's rows, /api/projects' list as it is (a lead row
// included, and only while it works).
export function fleetRoles(ps: ProjectsEntry[]): FleetRole[] {
  return ps.flatMap((p) => (p.roles || []).map((role) => ({ project: p.name, role, label: roleName(role.role) })));
}
