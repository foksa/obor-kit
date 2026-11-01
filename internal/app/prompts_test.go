package app_test

import (
	"strings"
	"testing"

	"obor/internal/app"
	"obor/internal/store"
)

// The prompts table (T-665): the text of every job, brief and start prompt serve sent is recorded right after its
// fact, by the fact's seq, and a failed write never fails the job.

// promptRows is the project's prompts for q, newest first.
func (r *loopRig) promptRows(t *testing.T, q store.PromptQuery) []store.PromptRow {
	t.Helper()
	rows, err := store.ReadPromptsAt(r.p.Root, q, 100)
	if err != nil {
		t.Fatalf("ReadPromptsAt %+v: %v", q, err)
	}
	return rows
}

// deliveredSeq is the seq of the log's delivered fact of job.
func (r *loopRig) deliveredSeq(t *testing.T, job string) int64 {
	t.Helper()
	for _, rec := range r.records() {
		if rec.Kind == app.KindFact && rec.Step == app.StepDelivered && rec.Fact().Job == job {
			return rec.Seq
		}
	}
	t.Fatalf("no delivered fact of %s", job)
	return 0
}

// A fetched job is one row with the exact text job_next gave, keyed by its delivered fact; a refetch adds no row.
func TestPromptsOfAFetchedJob(t *testing.T) {
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")

	jr := r.workerNext("n1")
	if jr.State != "job" || jr.Text == "" {
		t.Fatalf("job_next: %+v", jr)
	}
	r.waitFor("the delivered fact", func() bool { return len(r.deliveries()) == 1 })
	seq := r.deliveredSeq(t, "T-1/1/work")
	rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"})
	if len(rows) != 1 {
		t.Fatalf("the job's prompt rows: %+v, want one", rows)
	}
	if row := rows[0]; row.Seq != seq || row.Text != jr.Text || row.Kind != store.PromptJob || row.Agent != "pj-o1" ||
		row.Task != "T-1" || row.Job != "T-1/1/work" || row.TS == "" {
		t.Errorf("the job's prompt row: %+v; want the delivered fact %d's text", row, seq)
	}

	if jr := r.workerNext("n2"); jr.Job != "T-1/1/work" {
		t.Fatalf("a refetch: %+v, want the same job", jr)
	}
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts after the refetch")
	if rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"}); len(rows) != 1 {
		t.Errorf("a refetch added a prompt row: %+v", rows)
	}
}

// A brief is a row of its briefed fact (the line it types); a --prompt start is one too (its first prompt, kind start).
func TestPromptsOfABriefAndAStart(t *testing.T) {
	r := lineRig(t, "idle", "idle")
	r.start()

	if reply, err := r.line(app.MethodBrief, "b1", app.Call{To: "pj-rev", Note: "verify finding 3"}); err != nil ||
		!strings.HasPrefix(reply, "BRIEFED pj-rev") {
		t.Fatalf("brief: %q, %v", reply, err)
	}
	rows := r.promptRows(t, store.PromptQuery{Agent: "pj-rev"})
	if len(rows) != 1 || rows[0].Kind != store.PromptBrief || rows[0].Text != "[obor pj-lead] verify finding 3" ||
		rows[0].Job != "" || rows[0].Task != "" {
		t.Fatalf("the brief's prompt rows: %+v, want its line", rows)
	}
	if seq := r.briefedSeq(t, 1); rows[0].Seq != seq {
		t.Errorf("the brief's row seq %d, want its briefed fact %d", rows[0].Seq, seq)
	}

	if reply, err := r.line(app.MethodBrief, "s1", app.Call{To: "pj-rev", Prompt: "Review the module map"}); err != nil ||
		!strings.HasPrefix(reply, "BRIEF RECORDED for pj-rev") {
		t.Fatalf("--prompt start: %q, %v", reply, err)
	}
	rows = r.promptRows(t, store.PromptQuery{Agent: "pj-rev"})
	if len(rows) != 2 || rows[0].Kind != store.PromptStart || rows[0].Text != "Review the module map" ||
		rows[1].Kind != store.PromptBrief {
		t.Fatalf("after the start: %+v, want the start newest and the brief before it", rows)
	}
	if seq := r.briefedSeq(t, 2); rows[0].Seq != seq {
		t.Errorf("the start's row seq %d, want its briefed fact %d", rows[0].Seq, seq)
	}
}

// briefedSeq is the seq of the nth briefed fact of pj-rev.
func (r *loopRig) briefedSeq(t *testing.T, n int) int64 {
	t.Helper()
	seen := 0
	for _, rec := range r.records() {
		if rec.Task == app.MsgTask && rec.Kind == app.KindFact && rec.Step == app.StepBriefed {
			if seen++; seen == n {
				return rec.Seq
			}
		}
	}
	t.Fatalf("no briefed fact %d", n)
	return 0
}

// dropPrompts drops the prompts table through the store's own write open (the app's is elsewhere): the next prompt
// write fails as one to a broken store would.
func dropPrompts(t *testing.T, root string) {
	t.Helper()
	db, err := store.OpenRW(app.DBFile(root))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("DROP TABLE prompts"); err != nil {
		t.Fatalf("drop prompts: %v", err)
	}
}

// A failed prompt write is logged and never fails the job: the agent still gets its job and the delivery is recorded.
func TestPromptWriteFailureKeepsTheJob(t *testing.T) {
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	dropPrompts(t, r.p.Root)

	jr := r.workerNext("n1")
	if jr.State != "job" || !strings.Contains(jr.Text, "implement the spec of T-1 exactly") {
		t.Fatalf("job_next after a failed prompt write: %+v", jr)
	}
	r.waitFor("the delivered fact", func() bool { return len(r.deliveries()) == 1 })
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts")
	r.waitFor("the logged failure", func() bool {
		return strings.Contains(r.out.String(), "FAIL prompts of ") && strings.Contains(r.out.String(), "no such table: prompts")
	})
}
