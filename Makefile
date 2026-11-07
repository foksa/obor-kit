# obor (Go). `make build` → dist/obor, `make test` → go test ./..., `make check` → the merge gate (commands.check),
# `make check-affected` → its per-merge tier (commands.check_affected), `make check-compile` → the merge slot's
# compile tier (commands.compile, T-961), `make check-retest` → a red lane test's failing tests once more
# (commands.retest, T-964), `make check-readers` / `make check-web` →
# the two non-Go classes' checks (skills/templates/hooks and the page's Go side, T-901), `make check-release` → the
# kit's own release tooling (the Makefile, scripts/, install.sh; tests/release, T-902),
# `make trim-go-cache` → the Go build cache's entries unused for 48 h (commands.trim_go_cache, nightly, T-817),
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

.PHONY: build test conformance trim-go-cache check check-affected check-compile check-retest check-readers check-web check-release deadcode deps upgrade-check release ship publish clean web web-dev web-test

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-s -w -X obor/internal/version.Version=$(BUILD_VERSION)" -o dist/obor ./cmd/obor

test:
	$(GO) test ./...

# The conformance suite (T-798, T-804, deep dive §6): tests/conformance, behind the conformance build tag so
# go test ./... and make check never run its checks (its stub MCP server and output writer are unit-tested there).
# KIND=herdr|claude|codex|opencode (default: all), TIER=static (default; no session, no model) or live (one throwaway
# herdr session per kind, agents started interactively; KEEP=1 keeps the scratch dir), TURN=1 (live: one model call
# per kind). Prints PASS/FAIL/SKIP/WARN lines and records the versions checked in .obor/state/conformance.json; exit 1
# on a FAIL. Nightly runs TIER=static as a non-gating line (commands.conformance).
conformance:
	@KIND="$(KIND)" TIER="$(TIER)" TURN="$(TURN)" $(GO) run -tags conformance ./tests/conformance

# trim-go-cache (commands.trim_go_cache, T-817) deletes the Go build cache's files unused for 48 h (mtime: Go refreshes
# an entry's when it uses it, at most hourly) and prints "go cache: freed <size>, <size> left". Go's own trim keeps them
# five days, too long at four lanes' rate (the cache reached 53 GiB in about a day).
trim-go-cache:
	@d=$$($(GO) env GOCACHE) || exit 1; \
	if [ ! -d "$$d" ]; then echo "go cache: freed 0 B, 0 B left"; exit 0; fi; \
	if stat -c %s "$$d" >/dev/null 2>&1; then st="stat -c %s"; else st="stat -f %z"; fi; \
	freed=$$(find "$$d" -type f -mtime +1 -exec $$st {} + 2>/dev/null | awk '{s+=$$1} END {printf "%.0f", s+0}'); \
	find "$$d" -type f -mtime +1 -exec rm -f {} +; \
	left=$$(find "$$d" -type f -exec $$st {} + 2>/dev/null | awk '{s+=$$1} END {printf "%.0f", s+0}'); \
	awk -v f="$$freed" -v l="$$left" 'function size(n) { if (n >= 1073741824) return sprintf("%.1f GiB", n/1073741824); \
	  if (n >= 1048576) return sprintf("%.1f MiB", n/1048576); if (n >= 1024) return sprintf("%.1f KiB", n/1024); \
	  return sprintf("%d B", n) } BEGIN { printf "go cache: freed %s, %s left\n", size(f), size(l) }'

# The merge gate (check: the full suite) and its per-merge tier (check-affected, commands.check_affected, T-347): the
# same steps, but go test only on the packages $OBOR_CHANGED_FILES affects (obor affected go); any doubt runs the full
# suite. Serve's lane step runs the class's tier in the task's lane (T-961). check-compile (commands.compile, T-961) is
# what serve's merge slot runs on each merge: build, gofmt, vet and deadcode, no go test. CHECK_CPUS (obor run and the
# gates export it: cores / slots.max) is go test -p. Every class check starts with
# GO_GATE: GOCACHE falls back to $TMPDIR when the default is not writable (a Codex sandbox, T-206), then build, gofmt and
# vet; every failure prints a line fail_grep matches (^FAIL).
GO_GATE = c=$$($(GO) env GOCACHE 2>/dev/null); \
	{ mkdir -p "$$c" && : > "$$c/.obor-probe" && rm -f "$$c/.obor-probe"; } 2>/dev/null || \
	  { GOCACHE="$${TMPDIR:-/tmp}/obor-go-build"; export GOCACHE; }; \
	$(MAKE) -s build || { echo "FAIL make build"; exit 1; }; \
	f=$$(gofmt -l cmd internal); test -z "$$f" || { echo "FAIL gofmt: $$(printf '%s\n' "$$f" | tr '\n' ' ')"; exit 1; }; \
	$(GO) vet ./... || { echo "FAIL go vet"; exit 1; }
check check-affected:
	@$(GO_GATE); \
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
check-compile:
	@$(GO_GATE); \
	$(MAKE) -s deadcode || { echo "FAIL deadcode"; exit 1; }; \
	echo "compile: build, gofmt, vet, deadcode OK"

# check-retest (commands.retest, T-964): a red lane test's failing tests once more on the same tree, not the check's
# whole package tier: go test -short -run "$OBOR_FAILED_TESTS" on $OBOR_FAILED_PKGS (the run named them both), CHECK_CPUS
# as -p. Green → the red was a flake; anything else (and a run with no names: no retest at all, the lane step) is red.
check-retest:
	@rc=0; out=$$($(GO) test -short -count=1 $${CHECK_CPUS:+-p $$CHECK_CPUS} -run "$$OBOR_FAILED_TESTS" $$OBOR_FAILED_PKGS 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"

# check-readers: the packages that read the non-Go files outside a Go rule (merge.classes "readers", T-901). The survey
# greps the .go files under cmd/ and internal/ for each path, package by package; TestOborKitReaders fails when a package
# starts reading one and READER_PKGS lacks it, and when a listed package stopped:
#   skills/**        cmd/obor (skills_test.go: the skill texts, their frontmatter and the files they point to)
#   templates/**     cmd/obor (skills_test.go), internal/app (fetch.go, dispatch.go: the role texts and the job prompt),
#                    internal/config (validate_test.go: templates/config.example.json), internal/guard (session.go: the
#                    role text a session shows), internal/setup (init.go, agent.go: what setup copies into a project)
#   hooks/**         cmd/obor (plugin_wiring_test.go: hooks.json as the plugin registers it)
#   bin/go-commands  cmd/obor (skills_test.go, shim_test.go: the shim's commands)
#   .claude-plugin/  cmd/obor (plugin_wiring_test.go, shim_test.go), internal/agents/claude (claude.go: the manifest's
#                    mcpServers entry), internal/setup (plugin.go, oborinit.go: install's registration)
#   internal/checks  matches only through the classification table and a fixture that name paths without reading them:
#                    listed so the survey needs no per-file exception; its tests run beside cmd/obor's and internal/app's,
#                    so the extra package costs little wall time.
# tests/conformance reads hooks/hooks.json too, but that suite is `make conformance`, nightly. No deadcode step: a changed
# skill or template moves no package's API. Left as they are: docs/** is merge.neutral (guard only, unchanged) — the
# reader list above leaves docs out, so docs/agents/*.md, read by cmd/obor (skills_test.go) and internal/app
# (worktext_test.go), keeps today's hole; root README.md is read by cmd/obor (skills_test.go) but stays unmatched (the
# full check, safe), and bin/obor, githooks/** and web/package.json match no class and run the full check too (the
# release class owns Makefile, scripts/**, install.sh, RELEASING.md, .claude-plugin/marketplace.json and
# tests/release/**, T-902). In .obor/config.json the web class must stay before go (internal/web/dist/** lies under
# internal/**) and the release class before readers (.claude-plugin/marketplace.json lies under readers'
# .claude-plugin/**), since the first class whose glob matches owns the file.
READER_PKGS := ./cmd/obor ./internal/agents/claude ./internal/app ./internal/checks ./internal/config ./internal/guard ./internal/setup
check-readers:
	@$(GO_GATE); \
	rc=0; out=$$($(GO) test -count=1 $${CHECK_CPUS:+-p $$CHECK_CPUS} $(READER_PKGS) 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"

# check-web: the Go side of the page (merge.classes "web", T-901), on the same prologue. `go test ./internal/web` is
# TestDistFresh (the committed internal/web/dist was built from web/ as it is now), types_test (web/src/lib/types.ts
# against the JSON the page reads) and TestPageIsEmbedded (the embedded build). The page's own suite — make web-test:
# svelte-check and Vitest — needs web/node_modules, which only `make web` (npm ci, network) creates and no gate worktree
# has: a lane that touches web/ runs `make web` (that suite runs there, and dist is rebuilt) and the gate proves dist is
# the build of web/ as committed.
check-web:
	@$(GO_GATE); \
	rc=0; out=$$($(GO) test -count=1 $${CHECK_CPUS:+-p $$CHECK_CPUS} ./internal/web 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"

# check-release: obor-kit's own release tooling (merge.classes "release", T-902), which used to run inside cmd/obor on
# every product merge: the Makefile's check, release and publish targets, scripts/release, scripts/ship, scripts/publish
# and install.sh, each against fixture repos in a temp dir (tests/release, beside tests/upgrade). A change to what they
# test (merge.classes "release": Makefile, scripts/**, install.sh, RELEASING.md, .claude-plugin/marketplace.json,
# tests/release/**) runs them; the full check's go test ./... still includes the package, and scripts/release runs this
# target once more before the tag. No deadcode step: its fixture scripts are the tree's, not a package's API.
check-release:
	@$(GO_GATE); \
	rc=0; out=$$($(GO) test -count=1 $${CHECK_CPUS:+-p $$CHECK_CPUS} ./tests/release 2>&1) || rc=$$?; printf '%s\n' "$$out" >&2; \
	n=$$(printf '%s\n' "$$out" | grep -c '^ok' || true); f=$$(printf '%s\n' "$$out" | grep -c '^FAIL[[:space:]]' || true); \
	echo "go test: $$n pass, $$f fail"; test "$$rc" = 0 || exit "$$rc"

# Pinned; the module cache comes before the proxy, so a warm cache needs no network (a Codex sandbox has none).
# Test support (herdrtest, testkit) is excepted. The prompts table's path readers stay for the hooks and CLIs of T-665
# (obor web reads with a store handle it opens itself, view.Load's). -tags conformance: tests/conformance's main is
# behind that tag, and its untagged writer (report.go) is reached only from it.
deadcode:
	@out=$$(GOPROXY="file://$$($(GO) env GOMODCACHE)/cache/download,$$($(GO) env GOPROXY)" $(GO) run golang.org/x/tools/cmd/deadcode@v0.52.0 -tags conformance ./...) || exit $$?; \
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
