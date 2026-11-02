# release-lib.sh: what scripts/release and scripts/ship both prove about a release tag, sourced by both (T-643).
# Needs $tag (v<version>). POSIX sh.

# version_lt <a> <b>: version a (x.y.z) is below version b, comparing the parts numerically (false for anything that is
# not x.y.z, both sides: a prerelease or malformed tag never becomes the changelog baseline, T-683).
version_lt() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    if (a !~ /^[0-9]+\.[0-9]+\.[0-9]+$/ || b !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1
    split(a, x, "."); split(b, y, ".")
    for (i = 1; i <= 3; i++) if (x[i] + 0 != y[i] + 0) exit (x[i] + 0 < y[i] + 0) ? 0 : 1
    exit 1
  }'
}

# tag_notes: the release notes at the tag (docs/releases/v<version>.md), empty when the tag has no such file.
tag_notes() {
  git show "$tag:docs/releases/v${tag#v}.md" 2> /dev/null || true
}

# tag_notes_problem <notes>: why <notes> (tag_notes at $tag) is not a release's notes (empty when it is): something but
# whitespace, so the body publish sends gh is not empty. scripts/publish and scripts/release refuse the same way (T-682,
# T-683).
tag_notes_problem() {
  [ -n "$(printf '%s' "$1" | tr -d '[:space:]')" ] ||
    echo "no docs/releases/v${tag#v}.md at $tag: write the release notes (a summary line, then - **Name:** bullets) and commit them on main before the tag"
}

# main_neutral <path>: a path main may change after a tag without making it stale: docs (docs/, any *.md) and the
# release's own bookkeeping on main (bin/checksums.txt, the copy-back commit).
main_neutral() {
  case $1 in
    docs/* | *.md | bin/checksums.txt) return 0 ;;
  esac
  return 1
}

# tag_neutral <path>: a path the tag's own commit may change on top of its source parent: the catalog. Its
# .claude-plugin/marketplace.json may change only in the top-level name (tag_market_problem, T-645).
tag_neutral() {
  case $1 in
    bin/checksums.txt) return 0 ;;
  esac
  return 1
}

# market_flip: stdin's marketplace.json with its one "name": "obor-kit-dev" set to "name": "obor-kit" (main's dev name
# to the name users install), nothing else touched; fails, printing nothing, unless that string is there exactly once.
# v0.17.2 replaced every "name" and so renamed the plugin entry too (T-645).
market_flip() {
  awk -v re='"name"[[:space:]]*:[[:space:]]*"obor-kit-dev"' '
    { lines[NR] = $0; s = $0; while (match(s, re)) { n++; s = substr(s, RSTART + RLENGTH) } }
    END {
      if (n != 1) exit 1
      for (i = 1; i <= NR; i++) { l = lines[i]; sub(re, "\"name\": \"obor-kit\"", l); print l }
    }'
}

# market_problem <market> <source market> <plugin name> <where>: why the marketplace.json text <market> is not a
# release's (empty when it is): it parses, its top-level name is obor-kit, its plugin entry's name is <plugin name>
# (plugin.json's), and it is <source market> (main's) with only the top-level name flipped (market_flip).
market_problem() {
  printf '%s\n' "$1" | jq -e 'type == "object"' > /dev/null 2>&1 || { echo "at $4 does not parse as a JSON object"; return 0; }
  mk_name=$(printf '%s\n' "$1" | jq -r '.name | strings' 2> /dev/null) || mk_name=
  [ "$mk_name" = obor-kit ] || { echo "name at $4 is '$mk_name', want obor-kit"; return 0; }
  mk_plug=$(printf '%s\n' "$1" | jq -r '[.plugins[]?.name | tostring] | join(",")' 2> /dev/null) || mk_plug=
  [ -n "$3" ] && [ "$mk_plug" = "$3" ] ||
    { echo "plugin name at $4 is '$mk_plug', want '$3' (.claude-plugin/plugin.json's name)"; return 0; }
  mk_flip=$(printf '%s\n' "$2" | market_flip) ||
    { echo "at $4: its source has no single \"name\": \"obor-kit-dev\" to compare with"; return 0; }
  [ "$mk_flip" = "$1" ] || echo "at $4 differs from its source's in more than the top-level name"
}

# tag_market_problem: market_problem for the tag's marketplace.json against its source parent's (tag_source), the
# plugin name from the tag's plugin.json (empty when the tag's marketplace is right).
tag_market_problem() {
  mk_base=$(tag_source)
  [ -n "$mk_base" ] || { echo "at $tag has no source parent to compare with"; return 0; }
  market_problem "$(git show "$tag:.claude-plugin/marketplace.json" 2> /dev/null)" \
    "$(git show "$mk_base:.claude-plugin/marketplace.json" 2> /dev/null)" \
    "$(git show "$tag:.claude-plugin/plugin.json" 2> /dev/null | jq -r '.name | strings' 2> /dev/null)" "$tag"
}

# require_tag_market: the tag's marketplace.json is right (tag_market_problem), or the run stops naming why.
require_tag_market() {
  mk_problem=$(tag_market_problem)
  [ -z "$mk_problem" ] || fail ".claude-plugin/marketplace.json $mk_problem"
}

# tag_source: the tag's source parent: its commit's first parent, the version commit it was built from (empty when none).
tag_source() {
  git rev-parse -q --verify "$tag^{commit}^1" 2> /dev/null || true
}

# tag_extra: the paths the tag's own commit changes beyond tag_neutral ones and a marketplace.json that differs from its
# source's only in the top-level name (empty when it changes only those).
tag_extra() {
  base=$(tag_source)
  [ -n "$base" ] || { echo "everything (its commit has no parent)"; return 0; }
  extra=
  for f in $(git diff --name-only "$base" "$tag"); do
    if [ "$f" = .claude-plugin/marketplace.json ] && [ -z "$(tag_market_problem)" ]; then continue; fi
    tag_neutral "$f" || extra="${extra:+$extra, }$f"
  done
  echo "$extra"
}

# tag_predates [<tip>]: what <tip> (default HEAD) has past the tag's source parent beyond main_neutral paths (empty when
# nothing): the first-parent commits that bring it, as task ids (Merge T-<n>: …) or short shas; a difference no such
# commit explains is named by its paths.
tag_predates() {
  tip=${1:-HEAD}
  base=$(tag_source)
  [ -n "$base" ] || return 0
  stale=
  for f in $(git diff --name-only "$base" "$tip"); do
    main_neutral "$f" || stale="${stale:+$stale, }$f"
  done
  [ -n "$stale" ] || return 0
  ids=
  for sha in $(git log --first-parent --reverse --format=%H "$base..$tip"); do
    code=
    for f in $(git diff --name-only "$sha^1" "$sha"); do
      main_neutral "$f" || { code=1; break; }
    done
    [ -n "$code" ] || continue
    id=$(git log -1 --format=%s "$sha" | sed -n 's/^Merge \(T-[0-9][0-9]*[a-z]*\):.*/\1/p')
    [ -n "$id" ] || id=$(git rev-parse --short "$sha")
    ids="${ids:+$ids, }$id"
  done
  echo "${ids:-$stale}"
}

# require_fresh_tag [<tip>]: the tag is a release commit on top of the version commit, and <tip> has nothing past that
# commit but docs and bookkeeping. <tip> is HEAD by default (scripts/release, at tag time); scripts/ship passes the
# checksums: v<version> commit (T-661): merges on main after it belong to the next release. A tag is never moved, so
# either refusal's fix is a new patch version.
require_fresh_tag() {
  extra=$(tag_extra)
  [ -z "$extra" ] || fail "$tag's commit changes $extra, not only its catalog and marketplace name: release a new patch version"
  predates=$(tag_predates "${1:-HEAD}")
  [ -z "$predates" ] || fail "$tag predates $predates: release a new patch version"
}
