// The promotion of staging to main (T-974, T-977) as the pages show it (T-986): one compact line with the project's
// pipeline (`staging 4/6 · main 3eaaa98 (2h ago)`, plus the promotion under way), a RED promotion as the project's
// warning, and the fleet's main marker. Serve ships every fact (wire.PromoteStatus); nothing here reads wording. No
// promote object (branches.promote unset): no line, no warning, no marker, the pages as before.
import { now, relText } from "./format";
import type { PromoteStatus } from "./types";

// PROMOTE_STEPS are PromoteRun.step's values, wire.PromoteSuite and wire.PromoteReview (internal/web's types_test).
export const PROMOTE_STEPS = ["suite", "review"] as const;

export const sha7 = (sha: string | undefined) => String(sha || "").slice(0, 7);

// promoteState is what a running promotion does now: "promoting: full suite", "waiting for review RV-n", or "" (none
// runs, or a RED one: the warning says that).
export function promoteState(p: PromoteStatus | null | undefined): string {
  if (!p || p.red) return "";
  if (p.run?.step === PROMOTE_STEPS[0]) return "promoting: full suite";
  if (p.run?.step === PROMOTE_STEPS[1]) return "waiting for review" + (p.run.review ? " " + p.run.review : "");
  return "";
}

// promoteLine is the pipeline's promotion line at time t: the merged weight against the threshold, main's promoted
// sha and its age, then the running promotion's state ("": promotion off).
export function promoteLine(p: PromoteStatus | null | undefined, t = now()): string {
  if (!p) return "";
  const main = (p.branch || "main") + " " + (sha7(p.sha) || "—") + (p.at ? " (" + relText(p.at, "ago", t) + ")" : "");
  return ["staging " + (p.weight || 0) + "/" + (p.threshold || 0), main, promoteState(p)].filter(Boolean).join(" · ");
}

// promoteWarning is the project's warning for p: "promotion RED <sha7>" while a RED promotion's needs-you is open,
// else "".
export function promoteWarning(p: PromoteStatus | null | undefined): string {
  return p?.red ? "promotion RED " + sha7(p.red.sha) : "";
}

// promoteWarningDetail is the RED warning's hover text: the failing tests and the merges since ("": none).
export function promoteWarningDetail(p: PromoteStatus | null | undefined): string {
  if (!p?.red) return "";
  const merges = p.red.merges || [];
  return [p.red.tests, merges.length ? "merges since: " + merges.join(", ") : ""].filter(Boolean).join(" · ");
}

// mainOf is the fleet's main marker of p: the promoted branch, its sha7 and when it was promoted (at 0: no promotion
// recorded it); null without a promotion.
export function mainOf(p: PromoteStatus | null | undefined): { branch: string; sha: string; at: number } | null {
  if (!p) return null;
  return { branch: p.branch, sha: sha7(p.sha), at: p.at || 0 };
}
