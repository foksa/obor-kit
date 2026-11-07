// The promotion of staging to main (T-974, T-977) as the project page's pipeline shows it: a Staging column after the
// merge columns (the tasks merged since the last promotion, their weight against the threshold, the promotion under
// way or the RED one) and a Main marker (the promoted sha and its age). Serve ships every fact (wire.PromoteStatus);
// nothing here reads wording. No promote object (branches.promote unset): no column, no marker, the page as before.
import type { Tone } from "./format";
import type { PromoteStatus } from "./types";

// PROMOTE_STEPS are PromoteRun.step's values, wire.PromoteSuite and wire.PromoteReview (internal/web's types_test).
export const PROMOTE_STEPS = ["suite", "review"] as const;

export interface Staging {
  tasks: string[]; // merged into staging since the last promotion
  weight: number;
  threshold: number;
  fill: number; // the weight bar's fill, 0..1
  tone: Tone; // the column's: bad RED, info promoting, wait waiting for review, ok otherwise
  state: string; // "" | "promoting: full suite" | "waiting for review RV-n" | "RED <sha7>"
  red?: { sha: string; tests: string; merges: string[] }; // the RED card
}

export const sha7 = (sha: string | undefined) => String(sha || "").slice(0, 7);

// stagingOf is the Staging column of p (null: no promotion, the page as before).
export function stagingOf(p: PromoteStatus | null | undefined): Staging | null {
  if (!p) return null;
  const threshold = p.threshold || 0;
  const weight = p.weight || 0;
  const s: Staging = {
    tasks: p.tasks || [], weight, threshold, fill: threshold > 0 ? Math.min(1, weight / threshold) : 0,
    tone: "ok", state: "",
  };
  if (p.red) {
    s.tone = "bad";
    s.state = "RED " + sha7(p.red.sha);
    s.red = { sha: sha7(p.red.sha), tests: p.red.tests, merges: p.red.merges || [] };
  } else if (p.run?.step === PROMOTE_STEPS[0]) {
    s.tone = "info";
    s.state = "promoting: full suite";
  } else if (p.run?.step === PROMOTE_STEPS[1]) {
    s.tone = "wait";
    s.state = "waiting for review" + (p.run.review ? " " + p.run.review : "");
  }
  return s;
}

// mainOf is the Main marker of p: the promoted branch, its sha7 and when it was promoted (at 0: no promotion recorded
// it); null without a promotion.
export function mainOf(p: PromoteStatus | null | undefined): { branch: string; sha: string; at: number } | null {
  if (!p) return null;
  return { branch: p.branch, sha: sha7(p.sha), at: p.at || 0 };
}

// promoteWarning is the fleet's warning for p: "promotion RED <sha7>" while a RED promotion's needs-you is open, else "".
export function promoteWarning(p: PromoteStatus | null | undefined): string {
  return p?.red ? "promotion RED " + sha7(p.red.sha) : "";
}
