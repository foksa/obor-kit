# Upgrading an obor project from v0.19 to v0.20

## v0.20.0 (lanes are a pool)
Re-run the installer, then `obor serve restart` in each project. Nothing changes until you blank a lane's type.

- **Untyped lanes pool the work**: `lanes.<LANE>` may be `""` (any type). A task's `Type:` and `Difficulty:` pick the
  model and effort per dispatch, and a free untyped lane takes it; the worktree, pane and label are reused. To pool a
  lane, blank its type with `obor config set lanes.<LANE> ""`. Typed lanes are unchanged: they stay reservations for
  their type. `obor setup lane <LANE> [<type>]` takes the type as optional, `obor init` makes L-01 and L-02 untyped,
  and `obor setup pane --start` on an untyped lane refuses (a worker has no type without a dispatch) (T-724).
