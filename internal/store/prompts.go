package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The prompts table (T-665): the text of every job, brief and start prompt serve sent, one row per prompt it sent
// (Seq: the delivered fact for a job, the briefed one for a brief or a start). Its own id is the key (T-670): a job
// fetched again after its text changed (a new spec rev, a re-gate) gets another row, so a job may have several, its
// newest first. Serve is its only writer (WritePrompt); hooks and CLIs read it read-only (ReadPromptsAt). A payload,
// not state: never folded or replayed, so the log's replay never carries it. A line (a message fact) carries its text
// already and has no row here. Rows older than 14 days are pruned with the daily backup (PrunePrompts).

// Prompt kinds: a job's text (job_next's reply), a brief's line, a fresh start's first prompt.
const (
	PromptJob   = "job"
	PromptBrief = "brief"
	PromptStart = "start"
)

// PromptRow is one prompt serve sent: the seq of the fact it belongs to, the agent it went to, its kind, the job id
// (jobs only), the task (a job's; "" for a brief or a start), when it was recorded (RFC 3339), the spec rev a job's
// text was built from (0: none) and its text.
type PromptRow struct {
	Seq   int64
	Agent string
	Kind  string
	Job   string
	Task  string
	TS    string
	Rev   int
	Text  string
}

// WritePrompt stores r: a new row, whatever the table holds (each prompt has its own id, T-670). Serve's only write.
func WritePrompt(db *sql.DB, r PromptRow) error {
	switch {
	case r.Seq <= 0:
		return errors.New("prompts: no seq")
	case r.Agent == "":
		return errors.New("prompts: no agent")
	case r.Kind != PromptJob && r.Kind != PromptBrief && r.Kind != PromptStart:
		return fmt.Errorf("prompts: %q is no kind", r.Kind)
	}
	if _, err := db.Exec(`INSERT INTO prompts (seq, agent, kind, job, task, ts, rev, text) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Seq, r.Agent, r.Kind, r.Job, r.Task, r.TS, r.Rev, r.Text); err != nil {
		return fmt.Errorf("prompts: %s %d: %w", r.Agent, r.Seq, err)
	}
	return nil
}

// PromptQuery selects the prompts a reader wants: exactly one of an agent's, a task's (its jobs') or one job's (the
// others empty).
type PromptQuery struct{ Agent, Task, Job string }

// ReadPrompts is db's prompts for q, newest first (its rows' own order), at most limit (limit ≤ 0: none). One query:
// the caller keeps no transaction open.
func ReadPrompts(db *sql.DB, q PromptQuery, limit int) ([]PromptRow, error) {
	col, val := "", ""
	switch {
	case q.Agent != "" && q.Task == "" && q.Job == "":
		col, val = "agent", q.Agent
	case q.Task != "" && q.Agent == "" && q.Job == "":
		col, val = "task", q.Task
	case q.Job != "" && q.Agent == "" && q.Task == "":
		col, val = "job", q.Job
	default:
		return nil, errors.New("prompts: a query names one of an agent, a task or a job")
	}
	if limit <= 0 {
		return nil, nil
	}
	rows, err := db.Query("SELECT seq, agent, kind, job, task, ts, rev, text FROM prompts WHERE "+col+
		" = ? ORDER BY id DESC LIMIT ?", val, limit)
	if err != nil {
		return nil, fmt.Errorf("prompts: %w", err)
	}
	defer rows.Close()
	var out []PromptRow
	for rows.Next() {
		var r PromptRow
		if err := rows.Scan(&r.Seq, &r.Agent, &r.Kind, &r.Job, &r.Task, &r.TS, &r.Rev, &r.Text); err != nil {
			return nil, fmt.Errorf("prompts: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("prompts: %w", err)
	}
	return out, nil
}

// ReadPromptsAt is ReadPrompts on the project at root, read-only (a hook, a CLI: serve may be down). No database yet:
// none.
func ReadPromptsAt(root string, q PromptQuery, limit int) ([]PromptRow, error) {
	db, err := openROAt(root)
	if err != nil || db == nil {
		return nil, err
	}
	defer db.Close()
	return ReadPrompts(db, q, limit)
}

// PrunePrompts deletes the rows recorded before before (serve's nightly backup keeps 14 days): how many it deleted.
func PrunePrompts(db *sql.DB, before time.Time) (int64, error) {
	res, err := db.Exec("DELETE FROM prompts WHERE ts < ?", before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("prompts: prune: %w", err)
	}
	return res.RowsAffected()
}
