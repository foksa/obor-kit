# Upgrading an obor project from v0.21 to v0.22

## v0.22.0 (web redesign, drive messaging and commits)
Re-run the installer. Serve, `obor monitor` and `obor web` follow the new binary by themselves.

Nothing else is required.

- **obor web**: same address and routes; reload an open page once. The Setup page now collects edits and commits them
  together ("Review & commit").
- **Drives**: a drive started on v0.21 does not know `obor tell-lead` from its first prompt; tell it, or restart its
  agent with `obor drive T-xx`. It can now `git commit` in its lane without `OBOR_MERGE=1`.
- Only projects that change obor web's page itself (obor-kit) need Node, for `make web`.
