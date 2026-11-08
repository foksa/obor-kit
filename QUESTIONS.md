# Questions for the lead

- T-872 (DD-architecture U1): a review's start, a verify and a brief to the reviewer or the deep dive are no longer
  admitted (they read the head once; a publish no longer waits for them, nor they for it); the publish admission is
  `config_set`'s only. Lead-owned `docs/design.md` §Briefs (`:594`) still says "A brief to the reviewer or the deep dive
  is admitted first (T-730, T-734): serve moves its role worktree to the head it hands it (T-853) before the brief is
  recorded; a brief to any other role agent (advisor, researcher …) is not admitted first (T-737)." Proposed: "A brief
  to the reviewer or the deep dive moves an idle deep dive's role worktree to the integration branch's head (T-853)
  before the brief is recorded; nothing waits for a publish in flight (T-872)." No answer needed, just a doc line.
- T-972 (serve restarts on a new binary only in its first idle pass): lead-owned `docs/design.md` §Following a new
  obor binary says the restart goes "at once when idle …, else once it is, at the latest after 10 minutes". Proposed:
  "… at once when idle (no effect running, no nightly, no verdict call admitted), else at the first pass after the work
  ends, however long that takes; a pending restart holds nothing back (T-972)." No answer needed, just a doc line.
- T-1007 (a full lane also runs the checks of release and web files): lead-owned `docs/design.md` §Change-based merge
  checks lists `merge.classes`' keys as name/paths/checks/full. Proposed, added after the `full` key: "A class may set
  `with_full: true` (T-1007): a full merge whose files matched it also runs its `checks` once after `check`, in config
  order; `with_full` on a `full` class is an error (`obor config validate`)." No answer needed, just a doc line.
