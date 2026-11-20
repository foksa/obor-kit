# Upgrading an obor project from v0.41 to v0.42

## v0.42.0 (obor init is the one command for a new project)

**obor init in an empty folder.** `cd` into an empty folder and run `obor init`, in herdr or in a herdr-app (hc)
terminal: init picks the terminal it is typed in (`--terminal herdr|hc` overrides; typed outside both while both run,
herdr, with a line), writes the choice as `project.terminal`, makes one "obor init" commit (a file you wrote at one of
init's own paths goes into it; any other file still refuses), and ends by starting the lead, in the same pane when
that pane is alone in its tab, else in a new `lead` tab. `obor lead` in a folder with no config runs `obor init`. Init
asks nothing; the lead's first session asks one question: keep or change the models and lanes. Existing projects:
init and `obor lead` behave as before, except that init now starts the lead when none runs.

**herdr's own agent resume.** With herdr's `[session] resume_agents_on_restore` left on, a herdr restart no longer
leaves obor's agents running without obor's setup: serve replaces each one it restores with its own start (same
session) and stops the ones with no open work. The line `resume_agents_on_restore = false` is now optional; it saves
the double start.

**OpenCode.** The OpenCode kind passes its own fixed flags (`--auto --standalone`); `obor setup rename` no longer
rewrites a lane's `opencode.json`. `obor config validate` messages name the kind they are about.

```sh
# nothing to do
```
