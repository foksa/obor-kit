// md is a spec's or report's markdown as HTML: headings, lists, code blocks, inline code and bold, task ids linked.
// Everything is escaped first; the only tags are the ones built here.

export const esc = (s: unknown) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

const SECTION = /^(Summary|Self-check|Skipped|Open questions|Concerns|Blocked):/;

export function md(text: string, taskHref: (id: string) => string): string {
  const inline = (s: string) => esc(s)
    .replace(/\b(T-\d+)\b/g, (t) => `<a href="${taskHref(t)}">${t}</a>`)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(SECTION, "<strong>$1:</strong>");
  const out: string[] = [];
  let para: string[] = [];
  let list: string[] | null = null;
  let code: string[] | null = null;
  const flush = () => {
    if (para.length) { out.push("<p>" + para.map(inline).join(" ") + "</p>"); para = []; }
    if (list) { out.push("<ul>" + list.map((i) => "<li>" + inline(i) + "</li>").join("") + "</ul>"); list = null; }
  };
  for (const raw of String(text || "").split("\n")) {
    if (code !== null) {
      if (/^\s*```/.test(raw)) { out.push("<pre><code>" + esc(code.join("\n")) + "</code></pre>"); code = null; }
      else code.push(raw);
      continue;
    }
    if (/^\s*```/.test(raw)) { flush(); code = []; continue; }
    const h = raw.match(/^(#{1,4})\s+(.*)$/);
    if (h) { flush(); out.push(`<h${h[1].length}>${inline(h[2])}</h${h[1].length}>`); continue; }
    const li = raw.match(/^\s*(?:[-*]|\d+\.)\s+(.*)$/);
    if (li) {
      if (para.length) { const l: string[] | null = list; list = null; flush(); list = l; }
      (list = list || []).push(li[1]);
      continue;
    }
    if (!raw.trim()) { flush(); continue; }
    if (list && /^\s+\S/.test(raw)) { list[list.length - 1] += " " + raw.trim(); continue; }
    if (list || SECTION.test(raw)) flush(); // a report section starts a paragraph
    para.push(raw.trim());
  }
  if (code !== null) out.push("<pre><code>" + esc(code.join("\n")) + "</code></pre>");
  flush();
  return out.join("");
}
