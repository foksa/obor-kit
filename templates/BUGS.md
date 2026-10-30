# Bugs

The bug inbox, kept by the triage role (`agents/triage.md`). One row per bug; the person's words stay in the notes.
The triage agent writes only this file. Leads own the fixes.

**Severity**: **P0** can't continue / data lost / crash · **P1** wrong result or blocked flow, with a workaround ·
**P2** visible but harmless · **P3** polish.

**Status**: `new` · `needs-info` (one question asked) · `queued` (handed off to the lead) · `T-xx` (a lead
specced the fix) · `fixed (T-xx)` (merged, waiting for the person's retest) · `closed` (person confirmed) ·
`reopened` (still broken: handed off again) · `duplicate of B-xx`.

| ID | Sev | Area | Title | Steps / expected / actual | Env | Commit | Suspect | Status |
|---|---|---|---|---|---|---|---|---|
| B-01 | P2 | ui | Example: Save button does nothing on first click | 1. open Settings 2. click Save. Expected: saved toast. Actual: nothing until a second click ("feels dead") | desktop, Firefox | abc1234 | `src/ui/settings.ts:42` handler bound after render | closed |
