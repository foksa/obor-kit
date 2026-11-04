# Upgrading an obor project from v0.23 to v0.24

## v0.24.0 (obor monitor removed)
Re-run the installer, then `obor serve restart` in each project. Nothing in a project's files changes.

Close the monitor pane (labelled `<prefix>-monitor`) in each project's herdr workspace: `obor monitor` is gone, and
`obor init` no longer makes the "system" tab. obor web and `obor status` show the same view.

```sh
# nothing to do
```

What changes:
- `obor monitor`, `obor setup monitor` and the monitor pane are gone. `obor monitor` prints one line naming
  `obor web` or `obor status` for one minor release, then it is deleted.
- A pane still labelled `<prefix>-monitor` is no agent: the restore hook types nothing in it, and nothing restarts it.
