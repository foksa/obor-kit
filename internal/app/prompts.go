package app

import (
	"time"

	"obor/internal/store"
)

// The text of every prompt serve sent (T-665; the store's prompts table, store/prompts.go): a job's text (job_next's
// reply), a brief's line, a fresh start's first prompt. A payload, not state: never folded or replayed, so a restart
// keeps only what the log carries. A failed write is logged and never fails the job or brief it belongs to.

// promptKeep is how long the prompts table keeps a row: serve's nightly backup prunes the rest (T-665).
const promptKeep = 14 * 24 * time.Hour

// prompt records text, sent to agent for fact seq, in the project's prompts table (kind store.PromptJob,
// store.PromptBrief or store.PromptStart; job: the job id, jobs only; task: a job's, "" for a brief or a start).
// Nothing for an empty text.
func (a *App) prompt(p *proj, seq int64, kind, agent, job, task, text string) {
	if seq <= 0 || text == "" {
		return
	}
	err := store.WritePrompt(p.log.db, store.PromptRow{Seq: seq, Agent: agent, Kind: kind, Job: job, Task: task,
		TS: a.opt.Now().UTC().Format(time.RFC3339), Text: text})
	if err != nil {
		a.logf("FAIL prompts of %s: %v", p.name, err)
	}
}

// prunePrompts drops the prompts older than promptKeep, now's 14 days (serve's nightly backup): a failed prune is
// logged, the backup stands.
func (a *App) prunePrompts(p *proj, now time.Time) {
	n, err := store.PrunePrompts(p.log.db, now.Add(-promptKeep))
	switch {
	case err != nil:
		a.logf("FAIL prompts of %s: %v", p.name, err)
	case n > 0:
		a.logf("%s pruned %d prompts older than %s", p.name, n, promptKeep)
	}
}
