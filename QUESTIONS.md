# Questions for the lead

## T-1572: `docs/design.md`'s hc paragraph names the old identity lists

The "hc's core" paragraph says serve starts a core "from a scrubbed environment (no `HERDR_`, `OBOR_`, `HC_TERMINAL_`
name)" and refuses "a core whose environment holds a `HERDR_` or `OBOR_` name". One list now decides both
(`project.ForeignIdentity`: each terminal kind's identity, obor's `OBOR_`, a Claude session's markers), hc's own
`HC_TERMINAL_` is that kind's identity and stays, and a core started from a Rex block is refused. Proposal: "from a
scrubbed environment (no other terminal's identity name)" and "refuse a core whose environment holds another terminal's
identity".
