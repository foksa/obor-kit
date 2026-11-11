# Upgrading an obor project from v0.38 to v0.39

## v0.39.0 (obor web shows every task and the project's docs; fixes)

`obor web` has two new pages under each project: **Tasks**, every task serve's store knows (state, difficulty, the
model that ran it, lane, attempts, merge, the review that covered it; search and filters), and **Docs**, the markdown
files git tracks in the project, rendered read-only (BACKLOG, decisions and accepted findings pinned; ids and paths
link). Only files git's index tracks at the moment of the read are served.

`obor flakes` lists a test as FLAKY only when it failed and then passed on a retry in the last 7 days; the nightly's
race step no longer writes flake lines.

Fixes: `obor review close` reads `git status -z` (paths with spaces, quotes or renames); `obor setup rename` refuses
before changing anything when a lane `opencode.json` cannot be read; a failed `obor drive` names `obor drive <T-xx>`
once; the guard knows the combined and abbreviated detach spellings of `git checkout`; `obor config validate` warns
for the default `docs/**` no-check class too.

```sh
# nothing to do
```
