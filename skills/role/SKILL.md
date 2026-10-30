---
name: role
description: Takes a role name: put this running session into a role by hand (advisor, triage, deep-dive …) from its role doc, and name the herdr pane so `obor brief` reaches it.
disable-model-invocation: true
---

# /obor:role <name> — take a role by hand

`<name>` is the argument after `/obor:role`. No name → list the role docs (step 1) and stop.

## 1. Find the role text
The kit's role text `templates/agents/<name>.md` (in the plugin, two levels above this skill's directory), plus the
project's part `<agents dir>/<name>.md` (`obor config get .paths.agents`, default `docs/agents/`) when it exists: read
both. Only the project's exists → that one alone. Neither → list the available names from both places and stop; do
not invent a role.

## 2. Act as that role
Follow the text's Purpose / Rules / Loop for the rest of the session; where the project's part differs, it wins. Say
in one line which role you took and what you will and won't do. A placeholder like `{{prefix}}`: read it with
`obor config get .prefix`.

## 3. Name the pane
If `HERDR_PANE_ID` is set and the pane is unnamed, run `herdr pane rename "$HERDR_PANE_ID" <prefix>-<name>`
(`<prefix>` = `obor config get .prefix`): the pane label is the name obor serve reads, so `obor status` shows it and
`obor status` lists you. Print the name. A pane already named something else: keep that name and
say so. No `HERDR_PANE_ID` (not in a herdr pane): skip, say so. A pane taken over this way has no `OBOR_AGENT`, so its
session records no inbox: a brief reaches it only once it runs under `obor setup role <role>` or `obor restart
<prefix>-<name>`.

## 4. Known limit
Say it: `OBOR_ROLE` / `OBOR_AGENT` can't change in a running session, so the obor hooks keep the pane's original
identity and its permissions. For a full obor role (worker, lead, integrator) use `obor setup` / `obor restart`.
