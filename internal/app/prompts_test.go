package app_test

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"testing"

	"obor/internal/store"
)

// The prompts table (T-665, T-670): the text of every job, brief and start prompt serve sent is recorded right after
// its fact, by the fact's seq; every distinct text a job's fetch returned gets its own row (the job's newest), and a
// failed write never fails the job.

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
		if rec.Kind == event.KindFact && rec.Step == event.StepDelivered && rec.Fact().Job == job {
			return rec.Seq
		}
	}
	t.Fatalf("no delivered fact of %s", job)
	return 0
}

// A fetched job is one row with the exact text job_next gave, keyed by its delivered fact; a refetch with the same
// text adds no row.
func TestPromptsOfAFetchedJob(t *testing.T) {
	t.Parallel()
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
	if row := rows[0]; row.Seq != seq || row.Text != jr.Text || row.Kind != store.PromptJob || row.Agent != "pj-l-01" ||
		row.Task != "T-1" || row.Job != "T-1/1/work" || row.TS == "" || row.Rev != 0 {
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

// A brief is a row of its briefed fact (the line it types); a start's first prompt is one too (kind start).
func TestPromptsOfABriefAndAStart(t *testing.T) {
	t.Parallel()
	r := lineRig(t, "idle", "idle")
	r.start()

	if reply, err := r.line(wire.MethodBrief, "b1", wire.Call{To: "pj-rev", Note: "verify finding 3"}); err != nil ||
		!strings.HasPrefix(reply, "BRIEFED pj-rev") {
		t.Fatalf("brief: %q, %v", reply, err)
	}
	rows := r.promptRows(t, store.PromptQuery{Agent: "pj-rev"})
	if len(rows) != 1 || rows[0].Kind != store.PromptBrief || rows[0].Text != "[obor pj-lead] verify finding 3" ||
		rows[0].Job != "" || rows[0].Task != "" || rows[0].Rev != 0 {
		t.Fatalf("the brief's prompt rows: %+v, want its line", rows)
	}
	if seq := r.briefedSeq(t, 1); rows[0].Seq != seq {
		t.Errorf("the brief's row seq %d, want its briefed fact %d", rows[0].Seq, seq)
	}

	if reply, err := r.line(wire.MethodBrief, "s1", wire.Call{To: "pj-rev", Prompt: "Review the module map"}); err != nil ||
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
		if rec.Task == event.MsgTask && rec.Kind == event.KindFact && rec.Step == event.StepBriefed {
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
	db, err := store.OpenRW(event.DBFile(root))
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
	t.Parallel()
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

// specTexts are the T-1 specs a test sets: a new rev says something else.
const (
	specOne = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing.\n"
	specTwo = "# T-1 — Test\n\nDifficulty: medium\n\nDo the thing, again.\n"
)

// spec sets spec as T-1's spec, by the lead with call id.
func (r *loopRig) spec(callID, text string) string {
	r.t.Helper()
	reply, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Agent: "pj-lead",
		Role: "lead", CallID: callID, Task: "T-1", Spec: text})
	if err != nil {
		r.t.Fatalf("spec set %s: %v", callID, err)
	}
	return reply
}

// A second review job of an attempt: the lead set a spec rev after the review, so the moved target's gate (T-961: a
// move keeps the task's patch, and only a new spec rev ends the accept) sends the next review job (review.2), whose
// fetch is a row of its own; the old job's row stays (T-670).
func TestPromptsReviewJobAfterASpecRev(t *testing.T) {
	t.Parallel()
	r := newLoopRig(t)
	r.work("shared.txt", "lane\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	r.start()
	r.submit()
	first := r.waitJob("pj-int", "T-1/1/review")
	r.spec("sp1", specOne)
	r.scratch.Move() // an unrelated commit on main
	if _, err := r.verdict(wire.MethodAccept, "c1", r.patch(first), ""); err != nil {
		t.Fatal(err)
	}
	second := r.waitJob("pj-int", "T-1/1/review.2")
	if r.patch(second) != r.patch(first) || !strings.Contains(second, "Do the thing.") {
		t.Fatalf("precondition: the move keeps the patch and the job carries the new rev:\n%s", second)
	}
	rows := r.promptRows(t, store.PromptQuery{Task: "T-1"})
	if len(rows) != 2 {
		t.Fatalf("the review jobs' rows: %+v, want two", rows)
	}
	if rows[0].Job != "T-1/1/review.2" || rows[0].Text != strings.TrimPrefix(second, "[obor job T-1/1/review.2] ") ||
		rows[1].Job != "T-1/1/review" || rows[1].Text != strings.TrimPrefix(first, "[obor job T-1/1/review] ") {
		t.Errorf("the review jobs' rows: %+v; want the re-gated job's text newest", rows)
	}
}

// A work job fetched again after the lead set a new spec rev has a new text: the table keeps both rows, the newest the
// new text and the rev it was built from (T-670); a third, identical refetch adds none.
func TestPromptsWorkJobRefetchedAfterASpecRev(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")

	first := r.workerNext("n1")
	r.waitFor("the delivered fact", func() bool { return len(r.deliveries()) == 1 })
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", reply)
	}
	second := r.workerNext("n2")
	if second.Job != "T-1/1/work" || second.Text == first.Text || !strings.Contains(second.Text, "Do the thing.") {
		t.Fatalf("the refetch: %+v; want the work job with the new spec rev", second)
	}
	seq := r.deliveredSeq(t, "T-1/1/work")
	rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"})
	if len(rows) != 2 {
		t.Fatalf("the job's prompt rows: %+v, want two", rows)
	}
	if row := rows[0]; row.Text != second.Text || row.Rev != 1 || row.Seq != seq || row.Agent != "pj-l-01" ||
		row.Task != "T-1" || row.Kind != store.PromptJob || row.TS == "" {
		t.Errorf("the newest row: %+v; want the refetch's text, rev 1, under the delivered fact %d", row, seq)
	}
	if row := rows[1]; row.Text != first.Text || row.Rev != 0 || row.Seq != seq {
		t.Errorf("the first row: %+v; want the first fetch's text, rev 0", row)
	}

	if third := r.workerNext("n3"); third.Text != second.Text {
		t.Fatalf("an identical refetch: %+v, want the same text", third)
	}
	if rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"}); len(rows) != 2 {
		t.Errorf("an identical refetch added a prompt row: %+v", rows)
	}
}

// A fix job fetched again after the spec rev changed: its text carries the new spec, and the table keeps both rows.
func TestPromptsFixJobRefetchedAfterASpecRev(t *testing.T) {
	t.Parallel()
	r, _ := fetched(t)
	if _, err := r.verdict(wire.MethodSendback, "s1", "", "add a test"); err != nil {
		t.Fatal(err)
	}
	r.integratorStops()
	r.waitStep("2 result fix")
	first := r.workerNext("f1")
	if first.Job != "T-1/2/fix" || !strings.Contains(first.Text, "add a test") {
		t.Fatalf("the fix job: %+v", first)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 3 })
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", reply)
	}
	second := r.workerNext("f2")
	if second.Job != "T-1/2/fix" || second.Text == first.Text || !strings.Contains(second.Text, "Do the thing.") {
		t.Fatalf("the refetch: %+v; want the fix job with the new spec rev", second)
	}
	rows := r.promptRows(t, store.PromptQuery{Job: "T-1/2/fix"})
	if len(rows) != 2 {
		t.Fatalf("the fix job's rows: %+v, want two", rows)
	}
	if rows[0].Text != second.Text || rows[0].Rev != 1 || rows[1].Text != first.Text || rows[1].Rev != 0 {
		t.Errorf("the fix job's rows: %+v; want the refetch's text first, rev 1", rows)
	}
}

// A review job fetched again after the lead changed the spec it reviews (the gate merged rev 1): its text says so, and
// the table keeps both rows, the newest the new text.
func TestPromptsReviewJobRefetchedAfterASpecChange(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.work("feature.txt", "feature\n")
	r.start()
	if reply := r.spec("sp1", specOne); reply != "SET T-1 rev 1" {
		t.Fatalf("spec set: %q, want SET T-1 rev 1", reply)
	}
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.workerNext("n1")
	r.submit()
	r.waitStep("1 fact submitted")
	r.waitStep("1 result review")
	r.integratorIs("working")
	r.passes()
	first, _, err := r.jobNext("pj-int", "integrator", "", "i1", "")
	if err != nil || first.Job != "T-1/1/review" || !strings.Contains(first.Text, "Spec to review: rev 1") {
		t.Fatalf("the review job: %+v, %v; want the spec rev 1 the gate merged", first, err)
	}
	r.waitFor("delivered", func() bool { return len(r.deliveries()) == 2 })

	if reply := r.spec("sp2", specTwo); reply != "SET T-1 rev 2" {
		t.Fatalf("spec set: %q, want SET T-1 rev 2", reply)
	}
	again, _, err := r.jobNext("pj-int", "integrator", "", "i2", "")
	if err != nil || again.Job != "T-1/1/review" || again.Text == first.Text ||
		!strings.Contains(again.Text, "the lead changed it after the gate") || !strings.Contains(again.Text, "Do the thing, again.") {
		t.Fatalf("the review refetch: %+v, %v; want the review job with the changed spec", again, err)
	}
	rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/review"})
	if len(rows) != 2 {
		t.Fatalf("the review job's rows: %+v, want two", rows)
	}
	if rows[0].Text != again.Text || rows[0].Agent != "pj-int" || rows[0].Task != "T-1" ||
		rows[1].Text != first.Text {
		t.Errorf("the review job's rows: %+v; want the refetch's text first", rows)
	}
	if rows[0].Rev != 0 || rows[0].Rev != rows[1].Rev { // a review job's fetch carries no spec rev
		t.Errorf("the review job's rows' revs: %d, %d; want none", rows[0].Rev, rows[1].Rev)
	}
	if n := len(r.deliveries()); n != 2 {
		t.Errorf("delivered facts: %d, want 2 (the refetch records none)", n)
	}
}

// A refetch after serve restarted with the spec unchanged adds no row: the newest row is read from the store, not kept
// in the process (T-670).
func TestPromptsRefetchAfterARestart(t *testing.T) {
	t.Parallel()
	r := fetchRig(t)
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	first := r.workerNext("n1")
	r.waitFor("the delivered fact", func() bool { return len(r.deliveries()) == 1 })
	if rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"}); len(rows) != 1 {
		t.Fatalf("the job's prompt rows: %+v, want one", rows)
	}
	r.stop()
	r.start()

	again := r.workerNext("n2")
	if again.Job != "T-1/1/work" || again.Text != first.Text {
		t.Fatalf("the refetch after the restart: %+v, want the same text", again)
	}
	if rows := r.promptRows(t, store.PromptQuery{Job: "T-1/1/work"}); len(rows) != 1 {
		t.Errorf("the refetch after the restart added a row: %+v", rows)
	}
	eq(t, r.deliveries(), []string{"T-1/1/work mcp"}, "delivered facts after the restart")
}
