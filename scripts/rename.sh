#!/bin/bash
# rename.sh — the mechanical half of the herd → obor rename (T-573, P7-0a; docs/plan/phase-7-advice.md §5). Run it
# from the repo root on the rename's parent commit; the reviewer re-runs it there and diffs the result against the
# merge, so only the hand-written fallbacks need reading. A second run changes nothing. bash 3.2, BSD or GNU sed.
#
#   word `herd` → `obor` (covers the command, `cmd/herd`, `bin/herd`, `dist/herd`, the Go module and its imports,
#   `.herd/`, `~/.herd`, `herd.db`, `herd.events`, `herd-kit`, skills `/herd:*`, the MCP server, tags `[herd …]`),
#   `HERD_` → `OBOR_`, `mcp__herd__` → `mcp__obor__`, a `\n` or `\t` escape before `herd` counts as a word edge;
#   the article in front of a renamed word: `a herd` → `an obor`.
# Kept: `herdr`/`HERDR_*` (word edges), the agent label `herd-lead` (labels do not change), the owner's machine paths
# `~/Work/herd-kit*` and `../herd-kit-wt` (P7-1 moves the install), the history docs, the guard's herd-era captures.
set -eu
cd "$(git rev-parse --show-toplevel)"

excluded() {
	case "$1" in
	scripts/rename.sh | docs/decisions.md | docs/reviews/* | docs/research/* | docs/UPGRADE-*.md) return 0 ;;
	docs/plan/phase-5* | docs/plan/phase-7*) return 0 ;;
	internal/guard/testdata/*) return 0 ;;
	esac
	return 1
}

W='[^A-Za-z0-9_]'
list=$(mktemp)
trap 'rm -f "$list"' EXIT
git ls-files --cached --others --exclude-standard >"$list"
while IFS= read -r f; do
	[ -f "$f" ] && [ ! -L "$f" ] || continue
	excluded "$f" && continue
	grep -Iq . "$f" || continue # binary or empty
	grep -qE 'herd|HERD_' "$f" || continue
	sed -E \
		-e 's#herd-lead#@@OBOR_LABEL@@#g' \
		-e 's#Work/herd-kit#@@OBOR_WORK@@#g' \
		-e 's#herd-kit-wt#@@OBOR_WT@@#g' \
		-e 's#mcp__herd__#mcp__obor__#g' \
		-e ':a' \
		-e "s#(^|$W|\\\\[nt])herd($W|\$)#\\1obor\\2#" \
		-e 'ta' \
		-e "s#(^|$W)HERD_#\\1OBOR_#g" \
		-e "s#(^|$W)([Aa]) (\`?)obor($W|\$)#\\1\\2n \\3obor\\4#g" \
		-e 's#@@OBOR_LABEL@@#herd-lead#g' \
		-e 's#@@OBOR_WORK@@#Work/herd-kit#g' \
		-e 's#@@OBOR_WT@@#herd-kit-wt#g' \
		"$f" >"$f.rename-tmp"
	if cmp -s "$f" "$f.rename-tmp"; then
		rm -f "$f.rename-tmp"
	else
		cat "$f.rename-tmp" >"$f" # keeps the file's mode
		rm -f "$f.rename-tmp"
	fi
done <"$list"

# Paths last (the loop above reads the old ones on the first run). A target that exists is left alone: after the
# rename `bin/herd` is the one-release shim next to `bin/obor`. Only the tracked files of .herd/ move: the gitignored
# state moves with `obor migrate` (P7-0b).
move() {
	if [ -e "$1" ] && [ ! -e "$2" ]; then mv "$1" "$2"; fi
}
for f in $(git ls-files .herd); do
	mkdir -p .obor
	move "$f" ".obor/${f#.herd/}"
done
move cmd/herd cmd/obor
move bin/herd bin/obor
move opencode/herd.js opencode/obor.js
move opencode/herd.test.js opencode/obor.test.js

# A renamed import can sort differently (`obor/internal/store` after `net`): gofmt puts it back in order.
gofmt -l cmd internal tests | while IFS= read -r f; do gofmt -w "$f"; done
