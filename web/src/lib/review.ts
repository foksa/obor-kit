// The open review's stages (wire.Review*, in the order a cycle passes them) as obor status words them, and their tone:
// the reviewer at work (reviewing, verifying) is the active colour, the lead's turn (written, approved) waits, fixes
// are in the merge loop.
import type { Tone } from "./format";
import type { ReviewOpenStatus } from "./types";

export const REVIEW_STAGES = ["reviewing", "written", "fixes", "verifying", "approved"] as const;
export type ReviewStage = (typeof REVIEW_STAGES)[number];

const TEXT: Record<ReviewStage, string> = {
  reviewing: "reviewing",
  written: "written: the lead's turn",
  fixes: "fixes",
  verifying: "verifying",
  approved: "approved: close it",
};

const TONE: Record<ReviewStage, Tone> = {
  reviewing: "ok",
  written: "wait",
  fixes: "info",
  verifying: "ok",
  approved: "wait",
};

const known = (s: string): s is ReviewStage => (REVIEW_STAGES as readonly string[]).includes(s);

// reviewStageText is the stage as obor status' review line words it (an unknown stage as sent).
export const reviewStageText = (stage: string) => (known(stage) ? TEXT[stage] : stage);

// reviewTone is the stage's pill colour.
export const reviewTone = (stage: string): Tone => (known(stage) ? TONE[stage] : "idle");

// reviewActive: the reviewer works on the review now (its brief or its verify's is open).
export const reviewActive = (stage: string) => stage === "reviewing" || stage === "verifying";

// reviewBadge is the fleet page's badge: "RV-n <stage>".
export const reviewBadge = (r: ReviewOpenStatus) => `${r.id} ${r.stage}`;

// fileName is the review file's base name ("RV-n.md").
export const fileName = (path: string) => path.split("/").pop() || path;
