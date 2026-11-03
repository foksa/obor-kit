# Upgrading an obor project from v0.20 to v0.21

## v0.21.0 (Setup page, editing, obor drive)
Re-run the installer. Serve, `obor monitor` and `obor web` follow the new binary by themselves (v0.20 and later).

Nothing else is required.

- **Setup page**: `obor web` → a project → Setup (`#/<project>/setup`). Edits there commit `.obor/config.json` on
  main; commit or discard any hand edit of that file first, and change keys `.obor/local.json` overrides in
  local.json itself (the page refuses them with the path).
- **Optional cleanup**: after v0.20's role move, `roles.reviewer.worktree` and `roles.deep_dive.worktree` only reserve
  the name; delete them from `.obor/config.json`.
- **`obor drive` / `obor land`**: new commands, no setup needed; see `skills/lead/commands.md`.
