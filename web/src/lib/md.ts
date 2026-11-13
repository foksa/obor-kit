// md is a spec's, report's or doc's markdown as HTML: headings (each with an id), lists, tables, rules, code blocks,
// inline code, bold and links, task ids linked. Everything is escaped first; the only tags are the ones built here, and
// a link's href is either one the caller's Links gave or an http(s) URL.

export const esc = (s: unknown) =>
  String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]!);

const SECTION = /^(Summary|Self-check|Skipped|Open questions|Concerns|Blocked):/;

// Links is how md links what it finds: a task id always; a ref (RV-n, DD-…, a repo path ending .md, or a code span's
// text) and a markdown link's target only when the caller resolves them (undefined: the text stays plain). section,
// when the caller has one, is the address of a heading's id: every heading then gets a small anchor to it.
export interface Links {
  task: (id: string) => string;
  ref?: (token: string) => string | undefined;
  url?: (target: string) => string | undefined;
  section?: (id: string) => string;
}

// REF is what the text links: a repo path ending .md, a task id, a review id, a deep dive's id.
const REF = /(?<![\w./-])((?:[\w.-]+\/)*[\w-][\w.-]*\.md)(?![\w/])|\b(T-\d+)\b|\b(RV-\d+)\b|\b(DD-[A-Za-z0-9](?:[\w.-]*[A-Za-z0-9])?)/g;

// SPAN is the inline markup md takes apart before escaping: a code span, a markdown link.
const SPAN = /`([^`]+)`|\[([^\]]+)\]\(([^)\s]+)\)/g;

// BOLD is **…**: words bolds a link's body, inline bolds a whole line once its spans are held out.
const BOLD = /\*\*([^*]+)\*\*/g;

// PLACE is how inline holds a line's code spans and links while it pairs bold across them.
const PLACE = /\u0000(\d+)\u0000/g;

const external = (t: string) => (/^https?:\/\//i.test(t) ? t : undefined);

// slugger is a heading's id, unique in one document: "h-" and its words, a repeat numbered.
export function slugger() {
  const seen = new Map<string, number>();
  return (text: string) => {
    const base = "h-" + (text.toLowerCase().replace(/[`*_[\]()]/g, "").replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "section");
    const n = seen.get(base) || 0;
    seen.set(base, n + 1);
    return n ? `${base}-${n}` : base;
  };
}

export interface Heading {
  level: number;
  text: string;
  id: string;
}

const FENCE = /^\s*```/;
const HEADING = /^(#{1,4})\s+(.*)$/;

// headings is the document's headings as md renders them (fenced code skipped), each with the id md gives it.
export function headings(text: string): Heading[] {
  const slug = slugger();
  const out: Heading[] = [];
  let code = false;
  for (const raw of String(text || "").split("\n")) {
    if (FENCE.test(raw)) { code = !code; continue; }
    if (code) continue;
    const h = raw.match(HEADING);
    if (h) out.push({ level: h[1].length, text: h[2], id: slug(h[2]) });
  }
  return out;
}

// cells is a table row's cells: split on the pipes that are not escaped, the outer ones dropped.
function cells(row: string): string[] {
  const s = row.trim().replace(/^\|/, "").replace(/(?<!\\)\|$/, "");
  return s.split(/(?<!\\)\|/).map((c) => c.trim().replace(/\\\|/g, "|"));
}

const TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const RULE = /^\s*(-{3,}|\*{3,}|_{3,})\s*$/;

export function md(text: string, links: ((id: string) => string) | Links): string {
  const L: Links = typeof links === "function" ? { task: links } : links;
  const a = (href: string, body: string, ext = false) =>
    `<a href="${esc(href)}"${ext ? ' target="_blank" rel="noopener noreferrer"' : ""}>${body}</a>`;
  // plain is text escaped, its refs linked (a link's own body passes link=false); words bolds what plain gives back
  const plain = (s: string, link = true) => {
    const e = esc(s);
    if (!link) return e;
    return e.replace(REF, (m, _path, task) => {
      const h = task ? L.task(task) : L.ref?.(m);
      return h ? a(h, m) : m;
    });
  };
  const words = (s: string, link = true) => plain(s, link).replace(BOLD, "<strong>$1</strong>");
  // inline is a line: a code span or a link is one atom, held out of the pairing, so a code span's contents are never
  // parsed for emphasis and `**a \`b\` c**` still finds its two delimiters.
  const inline = (s: string) => {
    const atoms: string[] = [];
    const hold = (html: string) => {
      atoms.push(html);
      return "\u0000" + (atoms.length - 1) + "\u0000";
    };
    let out = "";
    let last = 0;
    for (const m of s.matchAll(SPAN)) {
      out += plain(s.slice(last, m.index));
      if (m[1] !== undefined) {
        const code = "<code>" + esc(m[1]) + "</code>";
        const t = m[1].trim().replace(/:\d+(-\d+)?$/, ""); // a path's :line
        const h = /^T-\d+$/.test(t) ? L.task(t) : L.ref?.(t);
        out += hold(h ? a(h, code) : code);
      } else {
        const ext = external(m[3]);
        const h = L.url?.(m[3]) ?? ext;
        out += hold(h ? a(h, words(m[2], false), h === ext) : words(m[2]));
      }
      last = m.index! + m[0].length;
    }
    return (out + plain(s.slice(last))).replace(BOLD, "<strong>$1</strong>")
      .replace(PLACE, (_, i) => atoms[+i]).replace(SECTION, "<strong>$1:</strong>");
  };
  const slug = slugger();
  // at is a heading's section link: the caller's address for that heading, or nothing when it has none.
  const at = (id: string) =>
    L.section
      ? `<a class="hlink" href="${esc(L.section(id))}" aria-label="Copy link to this section" title="Copy link">#</a>`
      : "";
  const out: string[] = [];
  let para: string[] = [];
  let list: string[] | null = null;
  let code: string[] | null = null;
  const flush = () => {
    if (para.length) { out.push("<p>" + para.map(inline).join(" ") + "</p>"); para = []; }
    if (list) { out.push("<ul>" + list.map((i) => "<li>" + inline(i) + "</li>").join("") + "</ul>"); list = null; }
  };
  const lines = String(text || "").split("\n");
  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    if (code !== null) {
      if (FENCE.test(raw)) { out.push("<pre><code>" + esc(code.join("\n")) + "</code></pre>"); code = null; }
      else code.push(raw);
      continue;
    }
    if (FENCE.test(raw)) { flush(); code = []; continue; }
    const h = raw.match(HEADING);
    if (h) {
      flush();
      const id = slug(h[2]);
      out.push(`<h${h[1].length} id="${id}">${inline(h[2])}${at(id)}</h${h[1].length}>`);
      continue;
    }
    if (raw.includes("|") && i + 1 < lines.length && TABLE_SEP.test(lines[i + 1]) && lines[i + 1].includes("-")) {
      flush();
      const head = cells(raw);
      const rows: string[][] = [];
      for (i += 2; i < lines.length && lines[i].includes("|") && lines[i].trim(); i++) rows.push(cells(lines[i]));
      i--;
      out.push('<div class="table"><table><thead><tr>' + head.map((c) => "<th>" + inline(c) + "</th>").join("") +
        "</tr></thead><tbody>" + rows.map((r) => "<tr>" + head.map((_, k) => "<td>" + inline(r[k] || "") + "</td>").join("") + "</tr>").join("") +
        "</tbody></table></div>");
      continue;
    }
    if (RULE.test(raw) && !list) { flush(); out.push("<hr>"); continue; }
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
