# Upgrading an obor project from v0.32 to v0.33

## v0.33.0 (serve prunes ended tasks' records after 30 days; store schema 14)

Serve's nightly backup now deletes, after the backup is written, the records of every task that ended over 30 days
ago (all but its merged or dropped fact) and the session records as old that are no agent's current session. The
store's schema becomes 14 at the first start and an older obor refuses it: **rolling back across this release means
restoring a nightly backup** (`.obor/state/backup/`) taken before the upgrade, as for schemas 12 and 13.

```sh
# nothing to do
```
