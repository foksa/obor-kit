import { describe, expect, it } from "vitest";
import { idleText, idleWhen } from "./idle";

describe("the idle line (T-803)", () => {
  it("words an unacknowledged state as obor status does", () => {
    const text = idleText({ since: 1762338300, last: "T-1 merged abc1234" });
    expect(text).toBe("idle, not acknowledged since " + idleWhen(1762338300));
    expect(text).toMatch(/^idle, not acknowledged since [A-Z][a-z]{2} \d{1,2} \d{2}:\d{2}$/);
  });
  it("words an acknowledged state as idle: <why>", () => {
    expect(idleText({ since: 1762338300, acked: true, why: "nothing to dispatch" })).toBe("idle: nothing to dispatch");
  });
  it("keeps an acknowledgement without a why worded", () => {
    expect(idleText({ since: 0, acked: true })).toBe("idle: acknowledged");
  });
});
