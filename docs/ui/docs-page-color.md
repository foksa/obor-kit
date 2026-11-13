# Docs page color

Design: https://claude.ai/artifact/5cvixeRCNYKUPpcSW36btt, board **7 · Color** (row "Color", under the six layout
boards). The board's theme toggle switches between Dark, Light with today's tokens, and Light with the fixed tones from
the separate item below. Clicking a heading in its "On this page" moves the current heading.

## Why

The Docs page as built (looked at live at `b4fa4e0`, obor-kit, 139 docs) is grey. Its only colors are accent links, the
open doc's 2 px accent edge, the accent search mark and the blue `staged` pill. The other pages read by color first:

| Page | What carries color | Where in code |
| --- | --- | --- |
| Fleet | state `Pill`s (serve up, needs you, paused), lane chips tinted by tone, the active project card's green-tinted border | `pages/Fleet.svelte` (`.lane-dot.t-*`, `.proj-card.active`) |
| Project | the status banner, pipeline stages (tone border 40% + tone background 9%), busy lane cards (tone border 40%), event badges | `pages/Project.svelte` (`.stage`, `.lane.busy`), `ui/EventList.svelte` (`ICON`) |
| Tasks | the state column's `Pill`, task ids as accent links | `pages/Tasks.svelte` |
| Task | the accent id chip, the `Stepper` (green done, tone current), the accent tab underline, the gate `Pill` | `pages/Task.svelte`, `ui/Stepper.svelte` |
| Agent | the status `Pill`, accent task links in its lines | `pages/Agent.svelte` |
| Setup | agent kinds as categorical pills (claude accent, codex info, opencode done), `Dot`s on roles, the accent heat map, accent inset edges on changed rows, amber on paused | `pages/Setup.svelte` (`.kind.k-*`, `heat`, `.lvl.changed`, `tr.mpaused`) |
| Shell | the green live dot, the amber stale mark, the red needs-you badge | `App.svelte` (`.live`, `.badge.bad`), `ui/Stale.svelte` |

The vocabulary is small. Seven tone tokens (`--ok --wait --bad --info --done --idle --accent`), always used one of four
ways:

1. a **Pill**: tone text on `color-mix(tone var(--tint))`;
2. a **Dot**: an 8 px tone circle beside fg text;
3. a **tinted surface**: a tone border 35–40% into `--line` and a tone background 7–9%;
4. an **inset edge**: `inset 2–3px 0 0` in a tone.

Setup's agent kinds also show that one fixed tone per category is part of the language, not only state.

What the design changes: the Docs page uses the same four forms, and only those.

- **Doc kind:** a tone per kind of doc, read from the path. Shown as a dot on rows, a dot and label in the doc header,
  and a tinted card for Pinned.
- **Freshness:** a doc changed in the last hour gets the shell's green live dot. Ages under a day are fg-2, older ones
  muted.
- **Where you are:** the current doc and the current heading get the accent tint as well as the accent edge. Headings
  already read get an accent rail, like the narrow bar's progress line.
- **Search:** the Enter target gets the accent tint and ring, the same as Setup's selected step and every focus ring.
  "Searching" gets an info dot.

The doc body itself stays as rendered today.

## Rules the design keeps

- **Tokens only.** Every color is a token or `color-mix()` of a token, like `Pill` and Project's `.stage`.
- **Color never carries text contrast.** Names, labels, ages and paths stay `--fg`, `--fg-2` or `--muted`. A tone
  shows only as a dot, an edge, a border or a background tint. In light mode the tone tokens are below 4.5:1 as text
  (see the separate item), so text on this page stays readable whether or not that fix lands. The only tone-colored
  text on the page already exists: accent links, the `staged` pill and red error lines.
- **Red is never a kind.** On every page it means needs-you or failed. It stays for the error lines (`.failed-line`,
  "Could not search the text").
- **No color inside the doc body** (`.prose` keeps its rendering). Reading comfort comes first, and kind and state are
  facts about the file, not its text.

Contrast, measured for the pairs this design adds (WCAG, dark / light):

| Pair | Dark | Light |
| --- | --- | --- |
| `--fg` on the current-doc tint (accent 12% over `--surface`) | 12.1 | 15.3 |
| `--fg` on the drawer's current-doc tint (accent 12% over `--side`) | 13.1 | 13.4 |
| `--fg-2` on a pinned card's kind tint (any kind, 7%) | ≥ 10.0 | ≥ 10.7 |
| `--muted` on a pinned card's kind tint (7%) | ≥ 5.06 | ≥ 5.44 |
| `--muted` on the Enter target's tint (accent 10%) | 4.95 | 5.25 |
| `--fg` on the search mark (accent 22%), unchanged | 10.0 | 13.3 |
| `--idle` on `--surface-3` (the read-only pill) | 5.09 | 5.14 |
| Kind and live dots against `--surface` (non-text, 3:1) | ≥ 7.2 | ≥ 4.35 |

## What gets which color, and why

### Doc kind (new, derived on the client from the path; works on any project)

| Kind | Token | Paths | Why this tone (the page that already uses it so) |
| --- | --- | --- | --- |
| open | `--wait` | file `BACKLOG.md`, `QUESTIONS.md` anywhere | amber is waiting or pending: Project's queue `brief` pill, Setup's paused dispatch. These are the files that list what is still open. |
| record | `--ok` | file `decisions.md`, `accepted.md`; anything under a `reviews/` folder | green is accepted: the events' `AC` badge (Fleet, Project), full check `GREEN`. Decisions and accepted findings are settled records. |
| agents | `--accent` | file `AGENTS.md`, `CLAUDE.md`; anything under an `agents/` or `skills/` folder (`docs/agents/`, `templates/agents/`, `skills/lead/`) | accent marks agents: agent links on Fleet, Project and Agent, Setup's `claude` kind. These are the texts agents run on. |
| plan | `--info` | anything under a `plan/`, `research/` or `ui/` folder | blue is in the loop or under review: Tasks' submitted and review states, the events' `GT` and `SB` badges. These are proposals, not contracts. |
| release | `--done` | anything under a `releases/` folder; file `CHANGELOG*.md`, `UPGRADE*.md`, `RELEASING.md` | violet is merged or published: Tasks' `merged` pill, the events' `MG` badge, `tone("published")`. |
| — | none | everything else (`docs/design.md`, `README.md`, knight-tour-web's `art/…`) | no dot. A project's ordinary docs stay quiet, so the dots mean something. |

File-name rules come first, then the deepest folder segment that matches. Examples: `docs/UPGRADE-v0.39.md` is a
release, and `templates/agents/worker.md` is agents.

Where a kind shows:

- **Pinned cards** (list page): a tinted surface, the Project `.stage` form at a lighter strength. The background is
  `color-mix(in srgb, var(--k) 7%, var(--surface))` and the border `color-mix(in srgb, var(--k) 35%, var(--line))`.
  The foot leads with a `Dot` (7 px) and the kind word in `--fg-2`, then the folder and the age. A pinned doc with no
  kind (`README.md`) keeps today's plain `.card`. obor-kit's five cards: Backlog amber, Decisions and Accepted findings
  green, AGENTS.md accent, obor plain.
- **Folder rows** (tree, list page and index): an 8 px `Dot` before the folder name when the folder has a kind
  (`agents/` accent, `plan/` `research/` `ui/` blue, `releases/` violet, `skills/` accent). `top level`, `docs/` and
  `templates/` have no dot.
- **Doc rows in the tree:** a 6 px dot only when the doc's kind differs from its folder's. In `top level`, Releasing
  obor is violet, Questions for the lead amber and obor-kit (CLAUDE.md) accent. In `docs/`, the UPGRADE files are
  violet. Inside `agents/`, no row repeats its folder's dot.
- **Flat rows** (Recently changed, matches, text-hit group headers, the index's Pinned): a 6 px dot before the name,
  since the folder isn't beside them. Rows without a kind keep a 6 px gap so names align.
- **Doc header:** a `Dot` and the kind word first in the meta line: `● agents · changed 2h ago · 34 lines · …`.
- **Newer / older cards** and the **narrow doc bar:** the neighbour's and the open doc's dot before the name and path.

### Freshness (ages)

- Changed in the last hour: a 6 px `--ok` dot before the age, which is `--fg-2`. This is the top bar's live dot
  (`App.svelte` `.live.on .ld`): the doc is being worked on now.
- Under a day: the age in `--fg-2`. Older: `--muted`, as today.
- The same rule covers doc rows, folder rows' `newest …`, pinned card feet, Recently changed and the doc header's
  `changed …`.
- `staged` stays the `--info` pill it is today.

### Current doc and current heading

- **Current doc** (index tree, index Pinned, matches, the drawer): `background: color-mix(in srgb, var(--accent) 12%,
  transparent)`, `box-shadow: inset 2px 0 0 var(--accent)`, `--fg`, weight 600. Today it is `--surface-3` plus the
  edge, which reads as hover. The tint is TaskLink's chip and Task's id chip (accent 13%), and the edge is Setup's
  `.lvl.changed`.
- **The open doc's folder chain:** the folder rows above the open doc are `--fg` instead of `--fg-2`, so the path to
  it reads.
- **Current heading** ("On this page"): the accent rail stays, and the row adds `color-mix(accent 10%)` with a `0 6px
  6px 0` radius. Headings above it are `--fg-2` with the rail in `color-mix(in srgb, var(--accent) 40%, var(--line))`:
  read so far. Headings below are `--muted` on `--line`. This is the same progress idea as the narrow bar's accent
  line.
- **Sections sheet / popover** (compact outline): the current row moves from `--surface-3` to the accent 12% tint and
  keeps its edge, the same as the drawer's current doc.

### Search hits and highlights

- **Mark:** `color-mix(accent 22%)` with `--fg` text, unchanged. Setup's heat map is the same accent ramp.
- **Enter target** (`.match.sel`, `.hit.sel`): accent 10% background and `inset 0 0 0 1px color-mix(in srgb,
  var(--accent) 50%, transparent)`. Today it is `--surface-3` plus a `--line-strong` ring. The accent is Setup's
  selected step (`.sel.st`) and `:focus-visible`: "this is what Enter opens".
- **Count line:** the numbers in `--fg`, the words `--muted`: `10 of 139 by name · 6 lines in 3 docs`.
- **Hit group header:** the doc's kind dot and title, and the line count as a small `--surface-3` / `--fg-2` badge (the
  sidebar's `.badge` shape, neutral).
- **Searching:** a 7 px `--info` dot before `searching…`, where today it is plain text. This is the Stale mark's
  dot-and-words shape, in info because a search is in progress.
- **Search error:** `--bad`, as today.
- Line numbers stay `--muted`. The filter's focus border stays `--accent`. The `Names` / `Search the text` toggle and
  the order toggle stay neutral, as `Segmented` is on every page.

### Page head

- `read-only` becomes `<Pill state="idle" text="read-only" dot={false} />`, Agent's and Setup's idle pill, beside
  `139 markdown files git tracks · at b4fa4e0`. The `Stale` mark stays.

### Narrow bar

- The open doc's kind dot sits between the `Docs` button and the path.
- The progress line stays `--accent` and gets a `--line-soft` 2 px track under it, so at 0% the bar still shows a line
  to fill.
- In the drawer, the current doc gets the accent tint. In the sheet, the current heading gets the accent tint.

### Unchanged on purpose

The doc body (`.prose`), the path line's links (muted, like the top bar's crumbs), the skeletons, the empty state, and
the `Copy path` and `↑ Top` buttons.

## Change list (one task)

### `web/src/lib/docs.ts` (+ `docs.test.ts`)

- Add `type DocKind = "open" | "record" | "agents" | "plan" | "release"` and `docKind(path: string): DocKind | ""`, with
  the rules above (file names first, then the deepest folder segment). A folder's kind is `docKind(dir + "/")`.
- Add `KIND_TONE: Record<DocKind, Tone>` (open `wait`, record `ok`, agents `accent`, plan `info`, release `done`). Use
  `Tone` from `lib/format.ts`.
- Add `freshness(updated: number | undefined, now: number): "hour" | "day" | "old" | "staged"`.
- Tests: `docKind` over obor-kit's paths and one other project's (`art/terrain/x.md` → `""`, `templates/agents/worker.md`
  → agents, `docs/UPGRADE-v0.39.md` → release); `freshness` at the boundaries. Unit, no fetch.

### `web/src/ui/Dot.svelte`

- Add an optional `size` prop (default 8). The docs rows use 6, the pinned foot 7, the searching dot 7. No other change;
  other pages keep 8.

### `web/src/ui/DocAge.svelte` (new)

- Props: `updated?: number`. Renders the `staged` info pill, or the age (`Rel mode="for"`) with the live `Dot`
  (`tone="ok"`, size 6) when `freshness` is `hour`. The age is `--fg-2` for `hour` and `day`, `--muted` for `old`.
  Freshness re-reads `tick.now` (`lib/clock.svelte`), as `Rel` does.
- It replaces the `age` snippets in both `DocIndex.svelte` and `Docs.svelte`, and their duplicated `.age` / `.staged`
  styles.

### `web/src/ui/DocIndex.svelte`

- Kind dots: index Pinned rows (6 px, a gap when no kind); folder rows (8 px, before the name); doc rows only when the
  kind differs from the folder's; match rows and hit group headers (6 px).
- `DocAge` in doc rows and match rows. The folder row's `newest …` uses the same freshness rule, live dot included.
- `.doc.on` (tree and index Pinned) and `.match.on`: accent 12% background, `inset 2px 0 0 var(--accent)`, `--fg`, 600. The
  folder rows on the open doc's path are `--fg` (a `chain` flag from `folderOf(path)` and its parents).
- `.match.sel`, `.hit.sel`: accent 10% background, accent 50% inset ring.
- The count line's numbers in `--fg`. "searching…" (in `countLine` and the `.note`) gets an info `Dot` (size 7).
- The hit group's count becomes a `--surface-3` / `--fg-2` badge.
- Stays: the mark, the filter's focus border, `DocModes`, the keys, `tall`.

### `web/src/ui/DocOutline.svelte`

- Derive the current heading's index. Buttons before it: `--fg-2`, `border-left-color: color-mix(in srgb,
  var(--accent) 40%, var(--line))`. The current one: add `background: color-mix(in srgb, var(--accent) 10%,
  transparent)` and `border-radius: 0 6px 6px 0`. After it: today's `--muted`.
- To draw the rail per row, the `.toc` `border-left` moves onto each button (it already has `border-left: 2px`).
- `compact` `.on`: accent 12% background instead of `--surface-3`, edge kept.

### `web/src/pages/Docs.svelte`

- Head: `read-only` as `<Pill state="idle" text="read-only" dot={false} />` before the count line (import `Pill`).
- Pinned cards: `style="--k: var(--{tone})"` and a `.pin.kinded` class when the doc has a kind. Tinted background 7% and
  border 35%; hover border `color-mix(var(--k) 55%, var(--line))`. The foot gets `Dot` (7) and the kind word in
  `--fg-2`, then the folder, then `DocAge`.
- Recently changed rows: a 6 px kind dot (or gap) before the title; `DocAge`.
- Doc header meta: `Dot` and the kind word first (none when no kind); `changed` + `DocAge` freshness.
- Newer / older cards: the neighbour's kind dot before its name; `a.nb:hover` border `color-mix(in srgb, var(--accent)
  40%, var(--line))`.
- Doc bar: the open doc's kind `Dot` before `.bar-path`; `.bar-line` gets a full-width `--line-soft` track under it.
- Drop the page's own `.age` and `.staged` rules (`DocAge` owns them).
- Stays: layout, breakpoints, `.prose`, the path line, skeletons, the empty and failed states.

### `web/src/app.css`

- No change for this page. The tones, `--tint` and `color-mix` cover everything.

### Tests

- `docs.test.ts`: `docKind`, `freshness` (above).
- Component: the current doc row carries `aria-current="page"` (exists). One test that a `BACKLOG.md` pinned card gets
  the `wait` kind (class or `--k`), and one that a doc changed 10 minutes ago shows the live dot and one changed 3 days
  ago does not. Existing tests unchanged otherwise.

## Separate item: light-theme tone tokens are under 4.5:1 as text (all pages)

Not part of the Docs task. Found while measuring: in light mode, tone text fails 4.5:1 almost everywhere. Accent
links on white are 4.49. `--ok` is 4.35 and `--wait` 4.40 on `--surface`. Every `Pill` is about 3.8 against its own
12% tint (`--info` 4.18, `--bad` 4.24). On `--bg` and `--surface-3`, accent links are 4.19 and 3.86. Dark mode passes
everywhere (≥ 5.1).

No token is missing. The fix is the light values of five existing tokens, the same hue darkened until each reaches
≥ 4.6 on its pill, on `--surface-3` and on `--bg`. Change them in both light blocks of `web/src/app.css`:

| Token | Today | Proposed | On its pill | On `--surface-3` | On `--bg` | On `--surface` |
| --- | --- | --- | --- | --- | --- | --- |
| `--accent` | `#c4552a` | `#ad4b25` | 4.64 | 4.74 | 5.14 | 5.51 |
| `--ok` | `#1d8a57` | `#19774b` | 4.70 | 4.78 | 5.18 | 5.55 |
| `--wait` | `#a86b00` | `#945e00` | 4.62 | 4.68 | 5.07 | 5.44 |
| `--bad` | `#c93a3a` | `#bf3434` | 4.67 | 4.81 | 5.22 | 5.59 |
| `--info` | `#2c6fd1` | `#2968c4` | 4.60 | 4.67 | 5.06 | 5.42 |
| `--done` | `#6e4fd6` | unchanged | 4.74 | 4.81 | 5.22 | 5.60 |

Board 7's "Light (fixed tones)" shows them. Also `--faint` (3.18 light, 3.70 dark on `--surface`) stays for
non-text only, as `docs/ui/docs-page.md` already says.

## Open questions for the owner

1. **The kinds and their tones:** five kinds (open, record, agents, plan, release), with the rest uncolored. Right
   set? The closest calls are `ui/` as plan, and decisions sharing green with accepted.
2. **Live dot window:** the green dot for "changed in the last hour". Or the last 3 hours, which on a busy day would
   mark most of Recently changed?
3. **Read-so-far rail** in "On this page": keep it, or only the current heading?
4. **The light tone fix** (separate item): take it as its own task now? It changes every page's light mode slightly
   (each tone a little darker).
