# Questions for the lead

- T-1556: `docs/design.md` quotes the wake line itself twice: "dispatch" says a fresh start's first prompt is the work
  job's wake, `[obor job <id>] call the obor job_next tool` (~line 520), and "Prompts" says a job wake with an inbox
  record is posted with the same line (~line 953). The line now says the tool is connected and is the first step
  (`internal/app/wire/call.go`). Should both quotes carry the new text?
- An answer's line retires when its task's worker takes its job (`delivered`: its `job_next`, the question of a failed
  delivery settled). `docs/design.md` ("A queued line is in one state": "An answer's line retires once its question is
  settled another way — the worker submits or asks again, …") should list that trigger too; the file is lead-owned.
