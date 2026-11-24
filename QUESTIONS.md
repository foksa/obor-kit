# Questions for the lead

- T-1556: `docs/design.md` quotes the wake line itself twice: "dispatch" says a fresh start's first prompt is the work
  job's wake, `[obor job <id>] call the obor job_next tool` (~line 520), and "Prompts" says a job wake with an inbox
  record is posted with the same line (~line 953). The line now says the tool is connected and is the first step
  (`internal/app/wire/call.go`). Should both quotes carry the new text?
