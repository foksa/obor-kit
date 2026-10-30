# Phase 7 — obor: rename, easy onboarding, a serve that outlives herdr

Owner 2026-10-30. Draft: `docs/plan/phase-7-draft.md`; advisor: `docs/plan/phase-7-advice.md` (hk-fable), accepted
whole. A new user: install obor (one script), `obor init` (no questions, installs the plugin), `obor lead`.

## Decisions (owner 2026-10-30)
- Names: command and plugin `obor`, repo and marketplace `obor-kit`, `.obor/`, `~/.obor`, `OBOR_*`, herdr plugin
  `obor.events`, line tags `[obor …]`. Lane names, prefixes and agent labels stay.
- Releases: a public GitHub repo `obor-kit` + GitHub Releases for the four binaries (the repo's docs go public with
  it). Install: `~/.obor/kit` (a checkout at the latest tag), `obor` linked into `~/.local/bin`, no sudo.
- serve: one launchd agent per project (`obor.serve.<name>`); `obor serve` in a pane stays for development.
- No per-project kit pin; `herd.version` is deleted.

## Releases and tasks
| Release | # | Task | Difficulty |
|---|---|---|---|
| v0.16 | P7-0a | Rename herd → obor: `scripts/rename.sh` (committed, exclusions per advice §5) + the four one-release fallbacks (shim `bin/herd`, `HERD_*` env read after `OBOR_*`, `[herd job` tag, fail closed on `.herd/` without `.obor/` → `run obor migrate`); herd-kit's own `.herd/` moves in the same commit. One lane, no other lane working | hard |
| v0.16 | P7-0b | `obor migrate` (advice §5 steps 1–7), idempotent; first run on herd-kit by the lead | medium |
| v0.16 | P7-1 | Release location: `bin/release-url` real, `make release` uploads and asserts plugin version = `VERSION`, `install.sh` | simple (repo creation: owner confirms) |
| v0.17 | P7-3 | `internal/service`: `obor serve install/uninstall/restart`, status line, serve waits for herdr instead of exiting | medium/hard |
| v0.17 | P7-4 | Delete `herd.version` (config, shim, docs) | simple |
| v0.17 | P7-2 | `obor init` (advice §1 steps 1–7), fake `claude` + stub herdr tests | hard |
| v0.17 | P7-5 | Delete the setup skill's question flow, `setup init`, `warnPlugin`; design doc Setup/Stable (lead) | simple |

Order: P7-0a alone → P7-0b → P7-1 → tag v0.16, axis-alies migrates once (advice §5) → P7-3 ∥ P7-4 → P7-2 → P7-5 →
deep dive → v0.17 (drops the four fallbacks); axis-maps starts on it with `obor init`, run by the owner alone.

## Done when
The owner sets up axis-maps from a fresh install with `obor init` and no lead help; a herdr restart leaves every
serve running; the review after each release, deep dive at the end, sizes reported.
