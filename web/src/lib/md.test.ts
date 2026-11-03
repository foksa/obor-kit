import { describe, expect, it } from "vitest";
import { md } from "./md";

const h = (t: string) => "#/t/pj/" + t;

describe("md", () => {
  it("escapes first, then builds headings, lists, code and task links", () => {
    const out = md("# Goal <b>\n\nFix **it** in `a.go`, see T-1.\n\n- one\n- two\n\n```\nx < y\n```", h);
    expect(out).toContain("<h1>Goal &lt;b&gt;</h1>");
    expect(out).toContain("<strong>it</strong>");
    expect(out).toContain("<code>a.go</code>");
    expect(out).toContain('<a href="#/t/pj/T-1">T-1</a>');
    expect(out).toContain("<ul><li>one</li><li>two</li></ul>");
    expect(out).toContain("<pre><code>x &lt; y</code></pre>");
    expect(out).not.toContain("<b>");
  });
  it("starts a report's sections on their own paragraph", () => {
    expect(md("Summary: done\nConcerns: none", h)).toBe("<p><strong>Summary:</strong> done</p><p><strong>Concerns:</strong> none</p>");
  });
});
