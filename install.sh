#!/bin/sh
# obor install: clone (or update) the kit into ~/.obor/kit at the newest release tag, link ~/.local/bin/obor, then run
# `obor version` once so the release binary, and beside it each kind binary the release ships (obor-kind-opencode,
# T-1046), is fetched and checksum-checked. Re-running this script is the upgrade.
#
#   curl -fsSL https://raw.githubusercontent.com/foksa/obor-kit/main/install.sh | sh
#
# POSIX sh, also bash 3.2 on macOS: no bash-4 features. OBOR_KIT_REPO overrides the repository (tests, forks); paths come
# from HOME (the shim installs its binary under $HOME/.obor/bin).
set -eu

repo=${OBOR_KIT_REPO:-https://github.com/foksa/obor-kit.git}
home=${HOME:-}
if [ -z "$home" ]; then
  echo "FAIL HOME is not set" >&2
  exit 1
fi
kit="$home/.obor/kit"
bindir="$home/.local/bin"

fail() { echo "FAIL $*" >&2; exit 1; }

command -v git > /dev/null 2>&1 || fail "git not found: install git first"

if [ -d "$kit" ]; then
  git -C "$kit" fetch --quiet --tags || fail "cannot fetch tags in $kit"
else
  mkdir -p "$home/.obor" || fail "cannot create $home/.obor"
  git clone --quiet "$repo" "$kit" || fail "cannot clone $repo into $kit"
fi

# Newest release tag by version order, the order the Makefile picks BUILD_VERSION in.
tag=$(git -C "$kit" tag --list 'v*' --sort=-v:refname | head -n 1)
[ -n "$tag" ] || fail "no v* release tag in $kit"
git -C "$kit" -c advice.detachedHead=false checkout --quiet --detach "$tag" || fail "cannot check out $tag"

mkdir -p "$bindir" || fail "cannot create $bindir"
[ ! -d "$bindir/obor" ] || fail "$bindir/obor is a directory: move it away, then re-run"
ln -sf "$kit/bin/obor" "$bindir/obor" || fail "cannot link $bindir/obor"
case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "export PATH=\"$bindir:\$PATH\"" ;;
esac

# One `obor version` from inside the kit: the shim fetches the release binary and the kind binaries into ~/.obor/bin and
# checks their checksums. Its failure (its message on stderr) fails the install.
installed=$(cd "$kit" && "$bindir/obor" version) || fail "obor version failed"
echo "obor installed: $installed (kit $kit at $tag)"
