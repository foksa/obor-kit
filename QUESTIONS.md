# Questions for the lead

- T-1562: `docs/design.md` ("opencode lanes") says OpenCode's own tools other than the worktree ones are "read tools …
  pass, and any other is unguarded". `opencode_session_rename` (a title, metadata only) now passes, and
  `opencode_session_move` goes as the guard's `Edit` event for the destination directory, so a role whose writes stay
  in its own tree has a move into another worktree or project denied. Proposed: "… its session rename and read tools
  pass, its session move goes as the guard's `Edit` event for the directory the session would run in (a lane worker's
  move into another worktree or project is denied, as a write there is), and any other is unguarded."
- T-1556: `docs/design.md` quotes the wake line itself twice: "dispatch" says a fresh start's first prompt is the work
  job's wake, `[obor job <id>] call the obor job_next tool` (~line 520), and "Prompts" says a job wake with an inbox
  record is posted with the same line (~line 953). The line now says the tool is connected and is the first step
  (`internal/app/wire/call.go`). Should both quotes carry the new text?
- An answer's line retires when its task's worker takes its job (`delivered`: its `job_next`, the question of a failed
  delivery settled). `docs/design.md` ("A queued line is in one state": "An answer's line retires once its question is
  settled another way — the worker submits or asks again, …") should list that trigger too; the file is lead-owned.
- T-1554: "Following a new obor binary" says a pending self-restart goes "at the first pass after the work ends, however
  long that takes (a pending restart holds nothing back)". `internal/app/follow.go` bounds it at `followWait` (30
  minutes, above the gate's 20-minute default timeout), cutting the work if it has not ended by then (it replays), and
  holds nothing for work of a task waiting on a person. Proposed: "… else at the first pass after the work ends, and at
  the latest 30 minutes after the binary was seen; a task waiting on a person holds nothing back …".
