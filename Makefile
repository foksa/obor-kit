# obor (Go). `make build` → dist/obor, `make test` → go test ./..., `make check` → the merge gate (commands.check),
# `make check-affected` → its per-merge tier (commands.check_affected),
# `make deadcode` → its dead-code check, `make deps` → a warm module cache for an offline machine, `make upgrade-check`
# → the previous release upgraded to this build (tests/upgrade),
# `make release V=x.y.z` → the whole release (scripts/release); `make ship` → the owner's push, publish and install
# (scripts/ship); `make release` without V → only the local release files; `make publish` → the four of them to a
# GitHub release (explicit, outward-facing).
GO ?= go
VERSION_FILE := VERSION
VERSION := $(shell cat $(VERSION_FILE))
# The version stamped into the binary (-X): the tag when HEAD is exactly at one (v0.14.0 at a release), else the newest
# release tag by version order plus -dev+<short sha> (v0.14.0-dev+abc1234, -dirty when the tree is modified) — release
# tags sit on release commits off main, so `git describe` on main names an older tag (T-559). Else the VERSION file's
# bare number with a v (v0.14.0) when this is not a git checkout or has no release tag. `make release` always stamps
# v$(VERSION): its binaries are built before the release commit (with their catalog) is tagged.
BUILD_VERSION := $(shell dirty=; [ -z "$$(git status --porcelain --untracked-files=no 2>/dev/null)" ] || dirty=-dirty; \
	tag=$$(git describe --tags --exact-match 2>/dev/null) || tag=; \
	if [ -n "$$tag" ]; then printf '%s%s\n' "$$tag" "$$dirty"; exit 0; fi; \
	tag=$$(git tag --list 'v*' --sort=-v:refname 2>/dev/null | head -n 1); \
	sha=$$(git rev-parse --short HEAD 2>/dev/null) || exit 1; \
	[ -n "$$tag" ] || exit 1; \
	printf '%s-dev+%s%s\n' "$$tag" "$$sha" "$$dirty")
ifeq ($(strip $(BUILD_VERSION)),)
BUILD_VERSION := v$(shell cat $(VERSION_FILE))
endif
GOOS_LIST := darwin linux
GOARCH_LIST := arm64 amd64

.PHONY: build test conformance check check-affected deadcode deps upgrade-check release ship publish clean web web-dev web-test

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X obor/internal/version.Version=$(BUILD_VERSION)" -o dist/obor ./cmd/obor

test:
	$(GO) test ./...

# The conformance suite (T-798, deep dive §6): tests/conformance, behind the conformance build tag so go test ./... and
# make check never run it. KIND=herdr (default: all kinds that exist), TIER=static (default). Prints PASS/FAIL/SKIP/WARN
# lines; exit 1 on a FAIL. Nightly runs TIER=static as a non-gating line (commands.conformance).
conformance:
	@KIND="$(KIND)" TIER="$(TIER)" $(GO) run -tags conformance ./tests/conformance

# The merge gate (check: the full suite) and its per-merge tier (check-affected, commands.check_affected, T-347): the
# same steps, but go test only on the packages $OBOR_CHANGED_FILES affects (obor affected go); any doubt runs the full
# suite. CHECK_CPUS (obor run and the gates export it: cores / slots.max) is go test -p. GOCACHE falls back to $TMPDIR
# when the default is not writable (a Codex sandbox, T-206); every failure prints a line fail_grep matches (^FAIL).
check check-affected:
	@c=$$($(GO) env GOCACHE 2>/dev/null); \
	{ mkdir -p "$$c" && : > "$$c/.obor-probe" && rm -f "$$c/.obor-probe"; } 2>/dev/null || \
	  { GOCACHE="$${TMPDIR:-/tmp}/obor-go-build"; export GOCACHE; }; \
	$(MAKE) -s build || { echo "FAIL make build"; exit 1; }; \
	f=$$(gofmt -l cmd internal); test -z "$$f" || { echo "FAIL gofmt: $$(printf '%s\n' "$$f" | tr '\n' ' ')"; exit 1; }; \
	$(GO) vet ./... || { echo "FAIL go vet"; exit 1; }; \
	pkgs=./...; \
	if [ "$@" = check-affected ]; then \
	  if a=$$(./dist/obor affected go); then \
	    case "$$a" in \
	      "full: "*) echo "affected: full suite ($${a#full: })" ;; \
	      "") pkgs= ;; \
	      *) pkgs=$$(printf '%s' "$$a" | tr '\n' ' '); echo "affected: $$(printf '%s\n' "$$a" | grep -c .) package(s): $$pkgs" ;; \
	    esac; \
	  else echo "affected: full suite (obor affected go failed)"; fi; \
	fi; \
	if [ -z "$$pkgs" ]; then echo "go test: 0 pass, 0 fail (no affected Go packages, go test skipped)"; else \
	  rc=0; out=$$($(GO) test -count=1 $${CHECK_CPUS:+-p $$CHECK_CPUS} $$pkgs 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	  n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	  echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"; \
	fi; \
	$(MAKE) -s deadcode || { echo "FAIL deadcode"; exit 1; }

# Pinned; the module cache comes before the proxy, so a warm cache needs no network (a Codex sandbox has none).
# Test support (herdrtest, testkit) is excepted. The prompts table's path readers stay for the hooks and CLIs of T-665
# (obor web reads with a store handle it opens itself, view.Load's).
deadcode:
	@out=$$(GOPROXY="file://$$($(GO) env GOMODCACHE)/cache/download,$$($(GO) env GOPROXY)" $(GO) run golang.org/x/tools/cmd/deadcode@v0.52.0 ./...) || exit $$?; \
	out=$$(printf '%s\n' "$$out" | grep -v -e '/herdrtest/' -e '/testkit/' \
	  -e '^internal/store/prompts\.go:[0-9:]*: unreachable func: ReadPromptsAt$$'); \
	if [ -n "$$out" ]; then printf '%s\n' "$$out"; exit 1; fi

# upgrade-check (T-772): a project made by the previous release survives this build installed over it. The previous
# release is the catalog's newest x.y.z below VERSION (PREV=x.y.z another, v0.23.0 or later: an older one is
# refused, the check's fixture needs a spec's Model:), its binary the kit's cache
# (~/.obor/bin, fetched with curl and checked against bin/checksums.txt when missing); the new build is dist/obor; the
# steps are the sh blocks of docs/UPGRADE-v<x.y>.md. The check is tests/upgrade's TestUpgradeCheck: a failure names
# its step and the output. scripts/release runs it before the tag.
upgrade-check:
	@$(MAKE) -s build || { echo "FAIL make build"; exit 1; }; \
	OBOR_UPGRADE_CHECK=1 OBOR_UPGRADE_PREV="$(PREV)" $(GO) test -count=1 -run '^TestUpgradeCheck$$' ./tests/upgrade || \
	  { echo "FAIL upgrade check (above: the step that failed and its output)"; exit 1; }; \
	echo "upgrade check: OK"

# deps warms the module cache an offline machine needs (`make deadcode` with GOPROXY=file://…): after a go.mod change
# run it once with network. `make build` alone warms only what it compiles, and deadcode loads every package.
deps:
	$(GO) mod download

# release: with V=x.y.z the lead's one command (scripts/release: the version commit and full check on main, the scratch
# worktree with marketplace obor-kit and the four binaries, the tag, and the catalog back on main); without V only the
# local release files, as before. DRY=1 (with V) prints the steps and changes nothing. The owner's next command: ship.
release:
ifeq ($(strip $(V)),)
	@plugin=$$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' .claude-plugin/plugin.json | head -n 1); \
	[ "$$plugin" = "$(VERSION)" ] || { \
	  echo "FAIL .claude-plugin/plugin.json version $$plugin != VERSION $(VERSION): make them match (the plugin version keys Claude's cache dir, so a mismatch serves the old copy)" >&2; \
	  exit 1; }
	mkdir -p dist bin
	: > dist/checksums.tmp
	for goos in $(GOOS_LIST); do \
	  for goarch in $(GOARCH_LIST); do \
	    name="obor-$(VERSION)-$$goos-$$goarch"; \
	    CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO) build -trimpath -ldflags "-s -w -X obor/internal/version.Version=v$(VERSION)" -o "dist/$$name" ./cmd/obor || exit $$?; \
	    shasum -a 256 "dist/$$name" >> dist/checksums.tmp || exit $$?; \
	  done; \
	done
	awk -v version="$(VERSION)" 'index($$2, "dist/obor-" version "-") != 1' bin/checksums.txt > dist/checksums.old.tmp
	cat dist/checksums.old.tmp dist/checksums.tmp | LC_ALL=C sort -k2,2 > dist/checksums.sorted.tmp
	mv dist/checksums.sorted.tmp bin/checksums.txt
	rm -f dist/checksums.tmp dist/checksums.old.tmp
else
	@sh scripts/release "$(V)" $(if $(DRY),--dry-run)
endif

# ship is the owner's one command after make release V=x.y.z (scripts/ship): push main up to the release (its
# `checksums: vx.y.z` commit, not main's tip) and the tag, publish the GitHub release, install the kit; each step is
# skipped when it is already done.
ship:
	@sh scripts/ship "$(VERSION)"

# publish is outward-facing and explicit: after `make release`, committing bin/checksums.txt and tagging, `make publish`
# uploads exactly the four binaries of v$(VERSION), each checked against the tag's catalog (scripts/publish; --verify-tag:
# no tag, no upload). The platform list there is literal, so GOOS_LIST/GOARCH_LIST overrides do not change it.
publish:
	@sh scripts/publish "$(VERSION)"

clean:
	rm -rf dist

# web builds obor web's page (web/, Svelte, T-760) into internal/web/dist, which the binary embeds: run it before every
# commit that touches web/ and commit dist/ with it (internal/web's TestDistFresh fails on a stale build). It needs
# Node and the network for npm ci; make check and make release never call it (the gate has no network).
web:
	cd web && npm ci --ignore-scripts --no-audit --no-fund && npm run check && npm test && npm run build

# web-dev serves the page from web/ with hot reload on http://127.0.0.1:5173, its /api proxied to a running obor web
# (OBOR_WEB, default http://127.0.0.1:8787).
web-dev:
	cd web && npm ci --ignore-scripts --no-audit --no-fund && npm run dev

# web-test is the page's own tests (Vitest) and type check.
web-test:
	cd web && npm run check && npm test
