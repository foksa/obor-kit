import { describe, expect, it } from "vitest";
import { headings, md } from "./md";

const h = (t: string) => "#/t/pj/" + t;

describe("md", () => {
  it("escapes first, then builds headings, lists, code and task links", () => {
    const out = md("# Goal <b>\n\nFix **it** in `a.go`, see T-1.\n\n- one\n- two\n\n```\nx < y\n```", h);
    expect(out).toContain('<h1 id="h-goal-b">Goal &lt;b&gt;</h1>');
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
  it("builds a table: a header row, the separator, its rows (pipes escaped kept, cells inline)", () => {
    const out = md("| Key | Value |\n|---|:--|\n| `a` | x \\| y |\n| **b** | T-2 |\n\nafter", h);
    expect(out).toBe('<div class="table"><table><thead><tr><th>Key</th><th>Value</th></tr></thead><tbody>' +
      "<tr><td><code>a</code></td><td>x | y</td></tr>" +
      '<tr><td><strong>b</strong></td><td><a href="#/t/pj/T-2">T-2</a></td></tr></tbody></table></div><p>after</p>');
    expect(md("a | b\nno separator", h)).toBe("<p>a | b no separator</p>");
  });
  it("links a markdown link only to what the caller resolves or an http(s) URL, escaped", () => {
    const L = { task: h, url: (t: string) => (t === "plan.md" ? "#/docs/pj/docs/plan.md" : undefined) };
    expect(md("[the plan](plan.md) and [gone](nope.md)", L)).toBe('<p><a href="#/docs/pj/docs/plan.md">the plan</a> and gone</p>');
    expect(md('[site](https://example.com/a?b=1&c="x")', h))
      .toBe('<p><a href="https://example.com/a?b=1&amp;c=&quot;x&quot;" target="_blank" rel="noopener noreferrer">site</a></p>');
    expect(md("[x](javascript:alert(1))", h)).not.toContain("href");
  });
  it("links refs only through the caller's ref: RV-n, DD-…, a .md path, a code span", () => {
    const seen: string[] = [];
    const L = { task: h, ref: (t: string) => { seen.push(t); return t === "RV-7" || t === "docs/a.md" ? "#/x/" + t : undefined; } };
    const out = md("See RV-7, DD-v0.38. and docs/a.md; `docs/a.md:12` and `nope.md`.", L);
    expect(out).toContain('<a href="#/x/RV-7">RV-7</a>');
    expect(out).toContain("DD-v0.38.");
    expect(out).not.toContain('href="#/x/DD');
    expect(out).toContain('<a href="#/x/docs/a.md">docs/a.md</a>');
    expect(out).toContain('<a href="#/x/docs/a.md"><code>docs/a.md:12</code></a>');
    expect(out).toContain("<code>nope.md</code>");
    expect(seen).toEqual(["RV-7", "DD-v0.38", "docs/a.md", "docs/a.md", "nope.md"]);
    expect(md("RV-7", h)).toBe("<p>RV-7</p>"); // no ref: plain
  });
  it("gives every heading a unique id, the one headings() names, and skips fenced headings", () => {
    const text = "## Next\n\n```\n## not one\n```\n## Next\n### Deep `x`";
    expect(headings(text)).toEqual([
      { level: 2, text: "Next", id: "h-next" }, { level: 2, text: "Next", id: "h-next-1" },
      { level: 3, text: "Deep `x`", id: "h-deep-x" }]);
    const out = md(text, h);
    expect(out).toContain('<h2 id="h-next">Next</h2>');
    expect(out).toContain('<h2 id="h-next-1">Next</h2>');
    expect(out).toContain('<h3 id="h-deep-x">Deep <code>x</code></h3>');
    expect(out).toContain("<pre><code>## not one</code></pre>");
  });
  it("keeps bold that holds a code span, and never parses a code span for emphasis", () => {
    expect(md("**Runtime deps: git, `bin/obor`, herdr.**", h))
      .toBe("<p><strong>Runtime deps: git, <code>bin/obor</code>, herdr.</strong></p>");
    expect(md("**a `b` c** and **plain**", h)).toBe("<p><strong>a <code>b</code> c</strong> and <strong>plain</strong></p>");
    expect(md("**a `T-1` and `**b**`**", h))
      .toBe('<p><strong>a <a href="#/t/pj/T-1"><code>T-1</code></a> and <code>**b**</code></strong></p>');
    expect(md("`a ** b`", h)).toBe("<p><code>a ** b</code></p>");
  });
  it("draws a rule", () => {
    expect(md("a\n\n---\n\nb", h)).toBe("<p>a</p><hr><p>b</p>");
  });
});
