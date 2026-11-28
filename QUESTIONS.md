# Questions for the lead

## T-1640: `docs/design.md`'s inbox paragraphs do not name pi

"Lines to the lead" describes Claude's, OpenCode's and agy's inboxes, and the role-agent bullet lists the kinds with
an inbox as "(Claude, OpenCode, agy)". pi's sessions have one too now: its extension binds
`${HOME}/.obor/pi/<session>.sock`, names it in the session-start hook's env as `OBOR_PI_INBOX` (`inbox_record`), and
`deliver` posts a `{"type":"user","text"}` line that the extension sends to pi, a turn when idle or a steer when busy.
Proposal: name pi beside OpenCode's inbox and add it to the role-agent kinds that get a posted line.

## T-1572: `docs/design.md`'s hc paragraph names the old identity lists

The "hc's core" paragraph says serve starts a core "from a scrubbed environment (no `HERDR_`, `OBOR_`, `HC_TERMINAL_`
name)" and refuses "a core whose environment holds a `HERDR_` or `OBOR_` name". One list now decides both
(`project.ForeignIdentity`: each terminal kind's identity, obor's `OBOR_`, a Claude session's markers), hc's own
`HC_TERMINAL_` is that kind's identity and stays, and a core started from a Rex block is refused. Proposal: "from a
scrubbed environment (no other terminal's identity name)" and "refuse a core whose environment holds another terminal's
identity".
