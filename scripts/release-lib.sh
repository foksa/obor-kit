# release-lib.sh: what scripts/release and scripts/ship both prove about a release tag, sourced by both (T-643).
# Needs $tag (v<version>). POSIX sh.

# main_neutral <path>: a path main may change after a tag without making it stale: docs (docs/, any *.md) and the
# release's own bookkeeping on main (bin/checksums.txt, the copy-back commit).
main_neutral() {
  case $1 in
    docs/* | *.md | bin/checksums.txt) return 0 ;;
  esac
  return 1
}

# tag_neutral <path>: a path the tag's own commit may change on top of its source parent: the catalog and the
# marketplace name (obor-kit on the tag, obor-kit-dev on main).
tag_neutral() {
  case $1 in
    bin/checksums.txt | .claude-plugin/marketplace.json) return 0 ;;
  esac
  return 1
}

# tag_source: the tag's source parent: its commit's first parent, the version commit it was built from (empty when none).
tag_source() {
  git rev-parse -q --verify "$tag^{commit}^1" 2> /dev/null || true
}

# tag_extra: the paths the tag's own commit changes beyond tag_neutral ones (empty when it changes only those).
tag_extra() {
  base=$(tag_source)
  [ -n "$base" ] || { echo "everything (its commit has no parent)"; return 0; }
  extra=
  for f in $(git diff --name-only "$base" "$tag"); do
    tag_neutral "$f" || extra="${extra:+$extra, }$f"
  done
  echo "$extra"
}

# tag_predates: what HEAD has past the tag's source parent beyond main_neutral paths (empty when nothing): the
# first-parent commits that bring it, as task ids (Merge T-<n>: …) or short shas; a difference no such commit explains
# is named by its paths.
tag_predates() {
  base=$(tag_source)
  [ -n "$base" ] || return 0
  stale=
  for f in $(git diff --name-only "$base" HEAD); do
    main_neutral "$f" || stale="${stale:+$stale, }$f"
  done
  [ -n "$stale" ] || return 0
  ids=
  for sha in $(git log --first-parent --reverse --format=%H "$base..HEAD"); do
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

# require_fresh_tag: the tag is a release commit on top of the version commit, and HEAD has nothing past that commit
# but docs and bookkeeping. A tag is never moved, so either refusal's fix is a new patch version.
require_fresh_tag() {
  extra=$(tag_extra)
  [ -z "$extra" ] || fail "$tag's commit changes $extra, not only its catalog and marketplace name: release a new patch version"
  predates=$(tag_predates)
  [ -z "$predates" ] || fail "$tag predates $predates: release a new patch version"
}
