# Questions for the lead

- T-872 (DD-architecture U1): a review's start, a verify and a brief to the reviewer or the deep dive are no longer
  admitted (they read the head once; a publish no longer waits for them, nor they for it); the publish admission is
  `config_set`'s only. Lead-owned `docs/design.md` §Briefs (`:594`) still says "A brief to the reviewer or the deep dive
  is admitted first (T-730, T-734): serve moves its role worktree to the head it hands it (T-853) before the brief is
  recorded; a brief to any other role agent (advisor, researcher …) is not admitted first (T-737)." Proposed: "A brief
  to the reviewer or the deep dive moves an idle deep dive's role worktree to the integration branch's head (T-853)
  before the brief is recorded; nothing waits for a publish in flight (T-872)." No answer needed, just a doc line.
