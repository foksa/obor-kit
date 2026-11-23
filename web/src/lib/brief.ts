// A briefed role agent's card on the Project page (beside the open review's): which role agents get one, the brief's
// stage and tone, and the brief's first line as the card shows it.
import { roleName, type Tone } from "./format";
import type { Role } from "./types";

// NO_CARD is the roles with no brief card: the reviewer has the review card, the integrator's work is the merge loop,
// the lead briefs.
const NO_CARD = ["reviewer", "integrator", "lead"];

// briefCards is the role agents with an open brief that get a card, in the roles' order.
export const briefCards = (roles: Role[]) => roles.filter((r) => r.brief && !NO_CARD.includes(r.role));

export type BriefStage = "not delivered" | "delivered" | "working" | "done";

// briefStage is where the role's brief is: its line still queued (not delivered), typed and not started (delivered),
// the agent's pane at work (working), or its pane worked on it and stopped working (done: the agent's turn ended,
// serve's "<agent> finished" closes the brief).
export function briefStage(r: Role): BriefStage {
  if (!r.brief?.delivered) return "not delivered";
  if (r.status === "working") return "working";
  return r.brief.worked ? "done" : "delivered";
}

const TONE: Record<BriefStage, Tone> = { "not delivered": "wait", delivered: "info", working: "ok", done: "done" };

// briefTone is the stage's pill colour: at work the active colour, as the review card's reviewing.
export const briefTone = (s: BriefStage): Tone => TONE[s];

// briefActive: the agent works on its brief now (the card's active border).
export const briefActive = (r: Role) => briefStage(r) === "working";

// briefText is the brief's text without its "[obor <sender>] " prefix ("" for none).
export const briefText = (text: string | undefined) => (text || "").replace(/^\[obor [^\]]*\]\s*/, "").trim();

// BRIEF_LINE is how many characters of the brief's first line the card shows.
export const BRIEF_LINE = 140;

// briefFirstLine is the brief's first non-empty line, cut to max characters with an ellipsis ("" for none).
export function briefFirstLine(text: string | undefined, max = BRIEF_LINE): string {
  const line = briefText(text).split("\n").map((l) => l.trim()).find((l) => l) || "";
  const chars = Array.from(line);
  return chars.length > max ? chars.slice(0, max - 1).join("").trimEnd() + "…" : line;
}

// briefBadge is the fleet page's badge of a role agent at work on its brief: "<role> working", as the review's.
export const briefBadge = (r: Role) => `${roleName(r.role)} ${briefStage(r)}`;
