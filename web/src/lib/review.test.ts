import { describe, expect, it } from "vitest";
import { REVIEW_STAGES, fileName, reviewActive, reviewBadge, reviewStageText, reviewTone } from "./review";

describe("the open review's stage (T-1256)", () => {
  it("words each stage as obor status does", () => {
    expect(REVIEW_STAGES.map(reviewStageText)).toEqual([
      "reviewing",
      "written: the lead's turn",
      "fixes",
      "verifying",
      "approved: close it",
    ]);
    expect(reviewStageText("later")).toBe("later");
  });
  it("colours the reviewer at work active and the lead's turn waiting", () => {
    expect(REVIEW_STAGES.filter(reviewActive)).toEqual(["reviewing", "verifying"]);
    expect(reviewTone("reviewing")).toBe("ok");
    expect(reviewTone("verifying")).toBe("ok");
    expect(reviewTone("written")).toBe("wait");
    expect(reviewTone("approved")).toBe("wait");
    expect(reviewTone("fixes")).toBe("info");
    expect(reviewTone("later")).toBe("idle");
  });
  it("badges the fleet row RV-n <stage> and names the file by its base name", () => {
    expect(reviewBadge({ id: "RV-190", stage: "reviewing", since: 0 })).toBe("RV-190 reviewing");
    expect(fileName("reviews/RV-190.md")).toBe("RV-190.md");
  });
});
