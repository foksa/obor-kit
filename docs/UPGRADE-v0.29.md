# Upgrading to v0.29

## v0.29.0
- **Close an open review's fix round before upgrading.** `obor review fix` is gone (T-924): a fix task links itself
  with a `Fixes: RV-<n>` line in its spec, and the new serve ignores links made by `obor review fix`. If a review has
  fix tasks dispatched or merged but not verified when you upgrade, either let its verify finish and close it first,
  or after the upgrade add `Fixes: RV-<n>` to each of those specs (`obor spec set`); a task already verified before
  the upgrade is then verified once more.
- **`commands.after_merge.restart_serve` is gone (T-935).** A config that still sets it is refused naming the key
  (`unknown field "restart_serve"`): remove it. Binary following restarts serve on the new build a `make build`
  leaves behind, within a minute.
