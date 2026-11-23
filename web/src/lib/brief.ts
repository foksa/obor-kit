// A briefed role agent's card on the Project page (beside the open review's): which role agents get one, the brief's
// stage and tone, and the brief's first line as the card shows it.
import { roleName, type Tone } from "./format";
import type { BriefStatus, Role } from "./types";

// NO_CARD is the roles with no brief card: the reviewer has the review card, the integrator's work is the merge loop,
// the lead briefs.
const NO_CARD = ["reviewer", "integrator", "lead"];

// briefCards is the role agents with an open brief that get a card, in the roles' order.
export const briefCards = (roles: Role[]) => roles.filter((r) => r.brief && !NO_CARD.includes(r.role));

// The brief's stages as serve sends them (wire.Brief*): its line still queued (not delivered), typed and not done
// (working), its agent's pane blocked at a prompt (waiting: the owner's input), or worked and its pane ready or its
// agent gone (done: serve's "<agent> finished" closes the brief).
const TONE: Record<string, Tone> = { "not delivered": "wait", working: "ok", waiting: "bad", done: "done" };

// briefStage is the role's brief's stage as serve sends it ("" for no brief).
export const briefStage = (r: Role): string => r.brief?.stage || "";

// briefTone is the stage's pill colour: at work the active colour, as the review card's reviewing; waiting the
// needs-you colour (an unknown stage grey).
export const briefTone = (s: string): Tone => TONE[s] || "idle";

// briefActive: the agent works on its brief now (the card's active border).
export const briefActive = (r: Role) => briefStage(r) === "working";

// briefBusy: the brief is open on the agent's pane, at work or waiting on the owner (the fleet tile's badges).
export const briefBusy = (r: Role) => briefStage(r) === "working" || briefStage(r) === "waiting";

// briefText is the brief's text without its "[obor <sender>] " prefix ("" for none).
export const briefText = (text: string | undefined) => (text || "").replace(/^\[obor [^\]]*\]\s*/, "").trim();

// briefFull is the brief's text as the card shows it (briefText), ending in "…" where serve clipped it (the agent's
// detail page shows it whole).
export function briefFull(b: BriefStatus | undefined): string {
  const t = briefText(b?.text);
  return t && b?.text_clipped ? t + "…" : t;
}

// BRIEF_LINE is how many characters of the brief's first line the card shows.
export const BRIEF_LINE = 140;

// briefFirstLine is the brief's first non-empty line, cut to max characters with an ellipsis ("" for none).
export function briefFirstLine(text: string | undefined, max = BRIEF_LINE): string {
  const line = briefText(text).split("\n").map((l) => l.trim()).find((l) => l) || "";
  const chars = Array.from(line);
  return chars.length > max ? chars.slice(0, max - 1).join("").trimEnd() + "…" : line;
}

// briefBadge is the fleet page's badge of a role agent on its brief: "<role> <stage>", as the review's.
export const briefBadge = (r: Role) => `${roleName(r.role)} ${briefStage(r)}`;
