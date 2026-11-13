# Docs page redesign

Design: https://claude.ai/artifact/5cvixeRCNYKUPpcSW36btt (a private Design canvas with 6 boards: the list, reading a long
doc, three narrow-width screens, the states. Boards 1 and 2 are clickable: on board 1 the filter, the Newest/A–Z toggle
and the folder folds work on the real obor-kit list).

## Why

What is hard today (looked at live on obor-kit at `e39151c`, 138 docs, and at the other three projects):

- **The list is a wall.** Every doc is a bare file name in three columns, in ten directory groups. `docs/research/` (54)
  and `docs/releases/` (30) take most of the page; there is no way to narrow the list except reading it, and no title,
  age or size tells one doc from another.
- **The order looks random.** A folder is sorted newest commit first, but no time is shown, so `v0.18.0.md` sits
  between `v0.35.0.md` and `v0.19.0.md`. Nesting is flat: `docs/agents/` is a sibling of `docs/`, and knight-tour-web
  shows 14 groups, eight of them holding one file (`art/terrain/natural-v1/`, …).
- **Moving from doc to doc loses your place.** The page is keyed by the route (`App.svelte`'s `{#key}` holds `route.id`
  and `router.n`), so every doc click remounts it: the sidebar index scrolls back to the top. Picking something in
  `research/` means scrolling the index down again each time.
- **A long doc has no map.** `docs/design.md` is 826 lines and 26 sections; only BACKLOG gets an outline (a row of
  chips above the text). Nothing says which section you are in or how far through you are. A jump to a heading also
  lands under the sticky top bar (`scroll-margin-top: 12px` against a ~53 px bar), and the sticky index is offset 12 px
  from the top too, so its first rows sit behind the bar.
- **Narrow width:** under 1000 px the index is hidden; the only way to another doc is "All docs", back to the full
  138-name wall, and the outline is gone except on BACKLOG.

What the design changes:

1. **A filter over the list**, the first thing on the page (`/` focuses it): every word must be in the path, like the
   accepted.md filter. Matches show flat with their folder, the match highlighted, `n of 138`; arrow keys and Enter open
   one.
2. **The list in three parts:** Pinned as five cards, Recently changed (the 10 newest commits across all folders), and
   Folders as a tree: folders nest by path, each row shows its count and newest age, a folder over 12 docs starts
   folded. Every doc row shows its age, so the newest-first order reads. A Newest / A–Z toggle (A–Z in natural order:
   `v0.9` before `v0.10`).
3. **Reading in three columns:** the index (same filter and tree, the open doc's folder unfolded), the doc, and an "On
   this page" outline of every `##`/`###` heading that follows the scroll. The doc header gets the doc's own `#` title,
   its path (folder part links back to the list), age, line count and section count. Under the doc: newer / older in
   the same folder.
4. **The page stays mounted from doc to doc**, so the index keeps its filter, folds and scroll.
5. **Narrow width:** a sticky doc bar (Docs · path · Sections, a thin line for how far through) opens the index as a
   drawer and the outline as a bottom sheet.

Everything is built from what `/api/docs` and `/api/doc` already serve. Four ideas that need new data are listed at the
end, separately.

## Structure

### Routes (unchanged)

`#/docs/<project>` is the list; `#/docs/<project>/<path>` reads one doc (`lib/route.ts`). Both stay one page component.

### The list (`#/docs/<project>`, board 1)

Regions, top to bottom inside the usual `.page`:

1. **Head:** `Docs`, `138 markdown files git tracks · at e39151c · read-only`, the `Stale` mark.
2. **Filter row:** a 40 px search input (placeholder `Filter 138 docs by name or path`, a `/` hint) and the Newest / A–Z
   `Segmented`.
3. **With an empty filter:**
   - **Pinned:** a grid of cards (`repeat(auto-fill, minmax(200px, 1fr))`): file name, a short caption (see open
     questions), folder, age.
   - Then two columns (`.cols`): **Folders** (wide) and **Recently changed** (narrow).
     - Folders: one card holding the tree. A folder row is a button (`aria-expanded`): chevron, name (`agents/` indented
       under `docs/`; a parent with no docs of its own, `skills/`, still gets a row), count, `newest 2h`. A doc row is a
       link: name, age on the right. "Open all" / "Fold all" in the section header.
     - Recently changed: 10 rows, name and age on one line, folder under it.
4. **With a filter:** Pinned, Recent and Folders give way to one **Matches** card: flat rows `folder/` + name (match
   highlighted) + age, the first row marked as the Enter target; `n of 138`; the no-match line with "Clear the filter".

### Reading (`#/docs/<project>/<path>`, board 2)

A three-column layout ≥ 1280 px: index 248 px · doc (fills) · On this page 220 px.

- **Index** (sticky under the top bar, scrolls on its own): a 32 px filter input, "All docs · 138" (to the list),
  Pinned (full paths), Folders (the tree, folded except the open doc's folder and its parents; the open doc marked with
  `aria-current` and an accent edge). With a filter typed the tree becomes the flat match list, as on the list page.
- **Doc header:** path line (`docs/` links to the list with that folder unfolded; `Copy path` button), the doc's first
  `#` heading as the title (the path when it has none), a meta line: `changed 2h ago · 826 lines · 26 sections · at
  e39151c` (or `staged`). accepted.md's filter sits in the header, under the title.
- **Doc body:** the existing `.card` + `.prose`, unchanged rendering.
- **Under the doc:** "← newer in docs/" and "older in docs/ →" (the neighbours in the folder's current order); a dashed
  empty box at either end.
- **On this page** (sticky): every heading of level 2 and 3 (`###` indented), the one in view highlighted with
  `aria-current="location"`; clicking scrolls to it. `↑ Top` under the list. Hidden when the doc has fewer than two
  headings (release notes): the doc takes the width.

### Narrow width

- **1000–1279 px:** index + doc; the outline turns into a `Sections` button in the doc header, opening the same list in
  a popover.
- **Under 1000 px** (boards 3–5): the index column goes. A **doc bar** sticks under the top bar: `Docs` button (opens
  the index as a left drawer, 340 px, the filter focused), the path (truncated from the left so the file name stays),
  a `Sections` button (opens a bottom sheet of headings, the current one marked; tapping one closes it and scrolls).
  A 2 px accent line along the bar's bottom shows how far through the doc you are. Targets are 44 px.
- **The list under 860 px:** the pinned grid wraps to two columns then one; Recently changed comes before Folders and
  shows 5 rows; the filter row wraps the toggle under the input.
- Wide code blocks and tables scroll in their own box (as today); the page never scrolls sideways.

### States (board 6)

| State | What shows |
| --- | --- |
| List loading | Index skeleton rows and `loading…` (the `.loading` class) |
| List failed, no data | `Could not load the docs: <error>` (`.failed`), as today |
| Refresh failed, data shown | `Stale` mark in the head; list and doc stay as last read |
| Empty (no tracked `.md`) | One centered card: `git tracks no markdown file here.`; no filter, no folders |
| Filter, no match | `No doc's path has every word of "<q>".` and Clear the filter; `0 of 138` |
| Not a tracked doc | `<path> is not a markdown file git tracks here.`, plus any listed doc with the same file name, and All docs |
| Doc loading | Header from the list (path, age) at once; title and body as skeleton |
| Doc failed | `Could not load <path>: <error>` in the body, header kept |
| Staged doc (no `updated`) | `staged` pill instead of an age, in rows and in the header; sorts first under Newest |
| Long doc | On this page + progress line; heading jumps clear the sticky bars |
| No headings | No outline column / no Sections button |
| accepted.md | The line filter in the doc header, `4 of 47`, same `filterLines` |
| BACKLOG.md | The chip row goes; its `##` sections are in On this page like any doc's |

## Components and data

Data the page reads, unchanged:

- `GET /api/docs?project=` → `DocList` (`lib/types.ts`): `head`, `pinned: Doc[]`, `groups: {dir, docs: Doc[]}[]`,
  `Doc = {path, updated?}` (`internal/view/docs.go`: `BuildDocs`, pinned order from `PinnedDocs`, folders by name, docs
  newest first).
- `GET /api/doc?project=&path=` → `DocText {path, text}`.
- Both through `Live` (`lib/live.svelte.ts`): read again every few seconds.

| Component | Where | Shows | Reads |
| --- | --- | --- | --- |
| Docs page | `web/src/pages/Docs.svelte` | head, list or reading layout, doc bar, drawer/sheet | `api.docs`, `api.doc` |
| Doc index | new `web/src/ui/DocIndex.svelte` | filter, All docs, Pinned, folder tree or matches; used in the reading sidebar and the narrow drawer | `DocList`, the open path, filter/fold state passed in |
| Folder tree rows | inside `DocIndex` (and the list page's Folders card) | folder rows (count, newest), doc rows (age) | `tree()` from `lib/docs.ts` |
| Outline | new `web/src/ui/DocOutline.svelte` | On this page / the Sections list, current heading | `headings()` from `lib/md.ts` on the doc text; the rendered headings' ids |
| Age | `web/src/ui/Rel.svelte` | `2h ago`, live | `Doc.updated` |
| Order toggle | `web/src/ui/Segmented.svelte` | Newest / A–Z | page state |
| Section header | `web/src/ui/Section.svelte` | Pinned, Folders (count, Open all / Fold all), Recently changed, Matches | — |
| Stale mark | `web/src/ui/Stale.svelte` | refresh failed | `Live.error` |
| Markdown | `web/src/lib/md.ts` (`md`, `headings`, `slugger`) | the body, heading ids | doc text |
| Doc links | `web/src/lib/docs.ts` (`docLinks`, `resolve`) | task, review and path links, as today | `DocList` paths |

Derived on the client (no new data): the tree and its counts, folder newest age, the 10 recent docs, matches, natural
A–Z order, newer/older neighbours, same-name suggestions, and from the open doc's text its title (first `# ` heading),
line count and headings.

## Change list

### `web/src/App.svelte`

- The `{#key}` around the routed page: for `route.kind === "docs"` key on the kind and project only (not `route.id`,
  not `router.n`), so moving between docs keeps the Docs page mounted. Other pages keep today's key.
- Stays: crumbs (`Docs` / the path), the docs sidebar item, the ⌘K jump.

### `web/src/lib/docs.ts` (+ `docs.test.ts`)

- Add `tree(list, order)`: folders nested by path from `groups` (a parent with no docs of its own gets a row), each with
  its docs, count (own and below) and newest `updated`; the root group labelled `top level`.
- Add `matchDocs(list, q)`: docs whose path contains every word of `q` (case aside), pinned first, then list order;
  with the first match position in the file name for highlighting.
- Add `recent(list, n)`: the n newest docs across pinned and groups (staged first).
- Add `sortDocs(docs, order)`: `new` keeps the served order; `az` is `localeCompare(…, {numeric: true})` on the name.
- Add `neighbours(list, path, order)`: the newer and older doc in the same folder.
- Add `sameName(list, path)`: listed docs with the path's file name (for the not-tracked state).
- Add `docTitle(text)`: the first level-1 heading's text, `""` when none.
- Add `defaultFolded(count)`: over 12 docs.
- Change `outline(text)` to every level 2–3 heading (it serves every doc, not only BACKLOG); `isBacklog` goes with the
  chip row if nothing else uses it.
- Stays: `paths`, `normalize`, `resolve`, `docLinks`, `isAccepted`, `filterLines`, `groupName` (or folded into
  `tree`).
- Tests: one per new function (unit, no fetch); the BACKLOG outline test changes to the all-docs outline.

### `web/src/ui/DocIndex.svelte` (new)

- Props: `project`, `list: DocList`, `path` (open doc), bindable `q`, `order`, `open` (fold map). Renders the filter,
  All docs, Pinned, and `tree()` or `matchDocs()` rows. Keyboard: ↑/↓ over matches, Enter opens, Esc clears.
- One component for the reading sidebar and the narrow drawer.

### `web/src/ui/DocOutline.svelte` (new)

- Props: `headings: Heading[]`, `compact` (the Sections sheet style). Tracks the heading in view with one
  `IntersectionObserver` over the rendered heading ids; click scrolls with `scrollIntoView`. Renders nothing for fewer
  than two headings.

### `web/src/pages/Docs.svelte`

- List layout (no path): filter row, Pinned cards, Folders + Recently changed, Matches when filtering; the empty state
  card.
- Reading layout: `DocIndex` | article | `DocOutline`, the breakpoints above; doc header (path line + Copy path, title,
  meta); accepted.md's filter moves into the header; the BACKLOG chip row goes; newer/older nav under the doc;
  not-tracked state with `sameName`.
- Because the page now stays mounted: the doc `Live` is made per path inside the page (stop the old one, start a new
  one when `path` changes); filter `q`, `order` and the fold map live in the page so they survive doc changes; the
  open doc's folder and parents unfold on each change.
- Narrow: the sticky doc bar, the drawer (`DocIndex`) and the bottom sheet (`DocOutline compact`), each a native
  `<dialog>` (Esc and focus handled by the browser); the progress line from the scroll position.
- `/` focuses the filter when no input has focus (`svelte:window` keydown, beside App's ⌘K).
- Fix the sticky offsets: the index and outline stick below the top bar (≈ 64 px, not 12).
- Stays: read-only, the `Stale` mark, `Live` polling, `md` + `docLinks` rendering, loading/failed text and classes.

### `web/src/app.css`

- `.prose h1–h4 { scroll-margin-top }` to clear the sticky top bar (and the doc bar under 1000 px), ≈ 72 px / 120 px.
- Stays: tokens and every other base style; the page adds no colour outside the tokens (small grey text uses `--muted`,
  not `--faint`, which is under 4.5:1 on `--surface`).

### Found while looking (not part of the redesign; separate small items)

- `web/src/lib/md.ts`: bold that contains a code span renders its `**` literally (`design.md` § Constraints, first
  bullet: `**Runtime deps: … \`bin/obor\` …**`). `inline()` cuts the line at code spans before `words()` applies bold,
  so the two `**` land in different pieces.
- `docs/accepted.md` has a line with its prefix twice: `(RV-161, accepted) (RV-161, accepted)`.

### Needs new data (separate items, not in the change list above)

1. **Titles in the list:** `view.Doc` gains `title` (the first `#` heading), read when `DocCache` rebuilds the list. The
   list and the filter could then show and match "obor-kit design (v2)", not only `design.md`. Costs a read of each
   tracked doc whenever HEAD or the index moves.
2. **Size in the list:** `view.Doc` gains `lines`, to mark long reads in rows.
3. **Search inside the docs:** a server route (`git grep` at HEAD over the tracked docs) returning path, line and a
   snippet; the filter would gain a "search the text" mode.
4. **Linked from:** the docs that link to the open one; needs every doc's links, server side.

A **section link in the URL** (`#/docs/<project>/<path>` plus a heading id) needs no new data but changes
`lib/route.ts`'s parse, so it is also its own item; the design leaves heading `#` links out until then.

## Open questions for the owner

1. **Pinned captions:** the cards carry a short fixed caption per pinned file ("what is open, the handoff", "owner
   decisions on record", …), written in the client next to `PinnedDocs`. Keep them, or file name and age only?
2. **Default order in a folder:** Newest (today's server order, now with ages shown) or A–Z? Or A–Z only for folders
   whose files are versions (`releases/`, `UPGRADE-v*.md`)?
3. **Folding:** folders over 12 docs start folded (research 54, releases 30 here). Right threshold, or fold everything
   but Pinned?
4. **Remember per viewer:** keep the order toggle and the folds in `localStorage` across visits, or start fresh each
   time?
5. **Which new-data item first, if any:** titles in the list (1) is the one that most changes how the list reads.
6. **Recently changed:** 10 rows across all folders; useful, or does Pinned already cover what you open?
