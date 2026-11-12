package app_test

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// A spec set sends no line; a task_done of work done against an older rev is refused once, with the same rev and note.

// specRevLine is task_done's refusal of an older rev: the new rev, the set's note and what to do next.
func specRevLine(task string, rev int, note string) string {
	line := fmt.Sprintf("spec is now rev %d", rev)
	if note != "" {
		line += ": " + note
	}
	return line + " — read obor spec show " + task + ", adjust, then submit again"
}

// workingT1 is the fetch rig with T-1's spec (rev 1) stored, dispatched, and its work job fetched by pj-l-01 at rev 1:
// the task working, the worker able to submit.
func workingT1(t *testing.T) *loopRig {
	t.Helper()
	r := fetchRig(t)
	r.inbox.record("pj-l-01")
	r.start()
	r.setSpec("s0", "# T-1 — Work\n\nDifficulty: medium\n")
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	jr := r.workerNext("n1")
	if !strings.Contains(jr.Text, "The spec of T-1 (rev 1") {
		t.Fatalf("precondition: the work job carries spec rev 1: %q", jr.Text)
	}
	r.waitFor("working", func() bool { return r.taskState() == "working" })
	r.connectedAgain()
	return r
}

// A spec set on a working task replies with the plain set line and sends the worker no line: its next task_done at the
// older rev is the only notice.
func TestSpecSetOnAWorkingTaskSendsNoLine(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := workingT1(t)
		if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\n## Files you may touch\n`more.txt`\n",
			"widen the guard"); got != "SET T-1 rev 2" {
			t.Fatalf("spec set on a working task: %q, want SET T-1 rev 2", got)
		}
		if n := r.inbox.count("pj-l-01", "[obor] "+specRevLine("T-1", 2, "widen the guard")); n != 0 {
			t.Errorf("the spec line posted %d times, want none", n)
		}
		r.passes()
		if n := r.inbox.count("pj-l-01", "[obor] "+specRevLine("T-1", 2, "widen the guard")); n != 0 {
			t.Errorf("the spec line posted %d times after the passes, want none", n)
		}
		if n := r.typedTo("pj-l-01", "[obor] "+specRevLine("T-1", 2, "widen the guard")); n != 0 {
			t.Errorf("the spec line typed %d times, want none", n)
		}
		for _, m := range r.waiting() {
			if m.To == "pj-l-01" {
				t.Errorf("a line to the worker is queued: %+v", m)
			}
		}
		if got := r.setSpecT("s2", "# T-1 — other text\n\nDifficulty: medium\n", "another note"); got != "SET T-1 rev 2" {
			t.Errorf("the same call id again: %q, want the stored reply", got)
		}
	})
}

// A task_done of work fetched at an older rev is refused once, naming the new rev and the set's note, and submits
// nothing; the refusal marks the rev seen, so the worker's next task_done submits.
func TestTaskDoneRefusesAnOlderSpecRev(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := workingT1(t)
		if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\n## Files you may touch\n`more.txt`\n",
			"widen the guard"); got != "SET T-1 rev 2" {
			t.Fatalf("spec set: %q", got)
		}
		want := specRevLine("T-1", 2, "widen the guard")
		if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err == nil ||
			!strings.Contains(err.Error(), want) {
			t.Fatalf("task_done at rev 1: %q, %v; want the refusal naming %q", reply, err, want)
		}
		if r.has("1 fact submitted") {
			t.Errorf("the refused task_done submitted: %q", r.steps())
		}
		if !r.hasPrefix("1 fact spec.seen") {
			t.Errorf("the refusal did not mark the rev seen: %q", r.steps())
		}
		if reply, err := r.callDone("pj-l-01", "worker", "t2", report); err != nil || reply != "submitted" {
			t.Fatalf("task_done once the rev is seen: %q, %v; want submitted", reply, err)
		}
		r.waitStep("1 fact submitted")
	})
}

// A task_done on the rev its job carried submits: a spec set before the worker's fetch is the job's rev.
func TestTaskDoneOnTheJobsSpecRevSubmits(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.start()
		r.setSpec("s0", "# T-1 — Work\n\nDifficulty: medium\n")
		r.setSpec("s1", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n")
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		jr := r.workerNext("n1")
		if !strings.Contains(jr.Text, "The spec of T-1 (rev 2") {
			t.Fatalf("precondition: the work job carries rev 2: %q", jr.Text)
		}
		if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err != nil || reply != "submitted" {
			t.Fatalf("task_done on the job's rev: %q, %v; want submitted", reply, err)
		}
	})
}

// A Blocked: report is no submission and no refusal, whatever rev the worker's job carried.
func TestBlockedTaskDoneOlderSpecRevAccepted(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := workingT1(t)
		r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n", "widen the guard")
		const asked = "not submitted: the owner is told what you need; stop and wait for the answer"
		if reply, err := r.callDone("pj-l-01", "worker", "t1", "Summary: half done\nBlocked: may I edit internal/x.go?\n"); err != nil ||
			reply != asked {
			t.Fatalf("a Blocked report at the older rev: %q, %v; want %q", reply, err, asked)
		}
		if r.hasPrefix("1 fact spec.seen") || r.has("1 fact submitted") {
			t.Errorf("the Blocked report was refused or submitted: %q", r.steps())
		}
	})
}

// A move after a spec set starts its new attempt on the new rev: its work job carries rev 2, and the new worker's
// task_done submits without a refusal.
func TestMoveAfterSpecSetStartsOnTheNewRev(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, working())
		jr := r.workerNext("n1")
		if !strings.Contains(jr.Text, "The spec of T-1 (rev 1") {
			t.Fatalf("precondition: attempt 1's job carries rev 1: %q", jr.Text)
		}
		if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n", "widen the guard"); got !=
			"SET T-1 rev 2" {
			t.Fatalf("spec set: %q", got)
		}
		if reply, err := r.moveTo("m1", "opus"); err != nil ||
			!strings.HasPrefix(reply, "MOVING T-1 on L-01: sonnet → opus") {
			t.Fatalf("move: %q, %v", reply, err)
		}
		r.waitJob("pj-l-01", "T-1/2/work")
		jr = r.workerNext("n2")
		if jr.Job != "T-1/2/work" || !strings.Contains(jr.Text, "The spec of T-1 (rev 2") {
			t.Fatalf("attempt 2's job: %+v; want its work job carrying rev 2", jr)
		}
		if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err != nil || reply != "submitted" {
			t.Fatalf("task_done of the moved attempt: %q, %v; want submitted", reply, err)
		}
	})
}

// A refused task_done whose reply was lost is sent again with its call id: it gets the same refusal, also after a serve
// restart (the log's replay), and submits nothing; only a new call id submits.
func TestTaskDoneSpecRefusalSurvivesARetry(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := workingT1(t)
		r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n", "widen the guard")
		_, first := r.callDone("pj-l-01", "worker", "t1", report)
		if first == nil || !strings.Contains(first.Error(), specRevLine("T-1", 2, "widen the guard")) {
			t.Fatalf("precondition: task_done at rev 1 refused: %v", first)
		}
		if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err == nil || err.Error() != first.Error() {
			t.Errorf("the same call id again: %q, %v; want the same refusal %q", reply, err, first)
		}
		r.stop()
		r.start()
		if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err == nil || err.Error() != first.Error() {
			t.Errorf("the same call id after a restart: %q, %v; want the same refusal %q", reply, err, first)
		}
		if r.has("1 fact submitted") {
			t.Fatalf("a retried refusal submitted: %q", r.steps())
		}
		if reply, err := r.callDone("pj-l-01", "worker", "t2", report); err != nil || reply != "submitted" {
			t.Fatalf("a new call after the refusal: %q, %v; want submitted", reply, err)
		}
		r.waitStep("1 fact submitted")
	})
}

// obor spec set is serve's request: the lead stores a spec (rev 1), the same text again changes nothing, another text is
// the next rev; a repeated call id gets its stored reply; the owner (no role) sets one too; every other role is refused
// and stores nothing. A set on a dispatched task sends no line; the reply is the plain set line.
func TestSpecSet(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		if _, ok, err := r.specRow("T-1"); err != nil || ok {
			t.Fatalf("precondition: a spec before any set: %v, %v", ok, err)
		}
		for _, c := range []struct{ call, text, want string }{
			{"s1", "# T-1 — A\n\nDifficulty: medium\n", "SET T-1 rev 1"},
			{"s2", "# T-1 — A\n\nDifficulty: medium\n", "UNCHANGED T-1 rev 1"},
			{"s3", "# T-1 — B\n\nDifficulty: medium\n", "SET T-1 rev 2"},
			{"s1", "# T-1 — C\n\nDifficulty: medium\n", "SET T-1 rev 1"}, // s1's stored reply: nothing stored
		} {
			if got := r.setSpec(c.call, c.text); got != c.want {
				t.Errorf("spec set %s: %q, want %q", c.call, got, c.want)
			}
		}
		for _, role := range []string{"worker", "integrator"} {
			reply, err := r.ask(
				&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-l-01", Role: role, CallID: "w-" + role},
					Task: "T-1", Spec: "# T-1 — widened by " + role + "\n"})
			if err == nil || !strings.Contains(err.Error(), "only the lead or the owner sets a spec") {
				t.Errorf("spec set by role %q: %q, %v; want refused", role, reply, err)
			}
		}
		row, ok, err := r.specRow("T-1")
		if err != nil || !ok || row.Rev != 2 || row.Text != "# T-1 — B\n\nDifficulty: medium\n" || row.SetBy != "lead" {
			t.Errorf("stored spec %+v, %v, %v; want rev 2 \"B\" set by lead", row, ok, err)
		}
		reply, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
			CallID: "o1"}, Task: "T-1", Spec: "# T-1 — by the owner\n\nDifficulty: medium\n"})
		if err != nil || reply != "SET T-1 rev 3" {
			t.Fatalf("spec set by the owner: %q, %v; want SET T-1 rev 3", reply, err)
		}
		if row, ok, err := r.specRow("T-1"); err != nil || !ok || row.Rev != 3 || row.SetBy != "owner" {
			t.Errorf("stored spec %+v, %v, %v; want rev 3 set by owner", row, ok, err)
		}
	})
}

// A spec with no title is refused beside the Difficulty refusal, and nothing is stored.
func TestSpecSetRefusesASpecWithoutATitle(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		for _, c := range []struct{ call, text string }{
			{"u1", "# T-1\n\nDifficulty: medium\n"},
			{"u2", "# T-1:\n\nDifficulty: medium\n"},
			{"u3", "Difficulty: medium\n"},
		} {
			_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
				Role: "lead", CallID: c.call}, Task: "T-1", Spec: c.text})
			if err == nil || !strings.Contains(err.Error(), "the spec needs a title: `# T-1 — <title>`") {
				t.Errorf("spec set of %q: %v; want a refusal naming the title", c.text, err)
			}
		}
		if _, ok, err := r.specRow("T-1"); err != nil || ok {
			t.Errorf("a refused spec was stored: %v, %v", ok, err)
		}
	})
}

// obor spec set accepts the one "Review:" value and refuses any other, naming it; and it refuses a spec that has both
// `Fixes:` and `Review: on merge` (the fix's verify is its review). A refusal stores nothing.
func TestSpecSetReviewOnMerge(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		good := "# T-1 — A\n\nDifficulty: medium\nReview: on merge\n\n## Do\n"
		if got := r.setSpec("r1", good); got != "SET T-1 rev 1" {
			t.Fatalf("spec set with \"Review: on merge\": %q, want SET T-1 rev 1", got)
		}
		_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", Agent: "lead", CallID: "r2"}, Task: "T-1",
			Spec: "# T-1 — B\n\nDifficulty: medium\nReview: now\n"})
		if err == nil || !strings.Contains(err.Error(), `the "Review:" line takes one value: "on merge"`) {
			t.Errorf("spec set with \"Review: now\": %v; want the refusal naming the one value", err)
		}
		// A fix spec carries Fixes: in the Review line's place; both is refused, and nothing is stored.
		_, err = r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", Agent: "lead", CallID: "r3"}, Task: "T-1",
			Spec: "# T-1 — C\n\nDifficulty: hard\nFixes: RV-12\nReview: on merge\n"})
		if err == nil || !strings.Contains(err.Error(), "a fix (Fixes:) is reviewed by its verify; drop the Review: on merge line") {
			t.Errorf("spec set with \"Fixes:\" and \"Review: on merge\": %v; want the refusal naming the pair", err)
		}
		if row, ok, err := r.specRow("T-1"); err != nil || !ok || row.Rev != 1 || row.Text != good {
			t.Errorf("stored spec %+v, %v, %v; want the accepted text at rev 1", row, ok, err)
		}
		// A fix spec alone (no Review line) is accepted, whatever its difficulty.
		fix := "# T-1 — D\n\nDifficulty: hard\nFixes: RV-12\n\n## Do\n"
		if got := r.setSpec("r4", fix); got != "SET T-1 rev 2" {
			t.Errorf("spec set of a fix spec: %q, want SET T-1 rev 2", got)
		}
	})
}

// An empty Ref: line is refused (an external tracker's id is the point of it), and nothing is stored.
func TestSpecSetRefusesAnEmptyRef(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		_, err := r.ask(&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root,
			Role: "lead", CallID: "e1"}, Task: "T-1", Spec: "# T-1 — A\n\nDifficulty: medium\nRef:\n"})
		if err == nil || !strings.Contains(err.Error(), `the "Ref:" line needs an id`) {
			t.Errorf("spec set with an empty \"Ref:\": %v; want the refusal naming the id", err)
		}
		if _, ok, err := r.specRow("T-1"); err != nil || ok {
			t.Errorf("a refused spec was stored: %v, %v", ok, err)
		}
	})
}

// A dispatch reads the stored spec for Specs.Check (which refuses "": no stored spec, cmd/obor TestSpecs): before the set
// the request is refused with nothing recorded, after it the same request dispatches.
func TestDispatchNeedsStoredSpec(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, dispatching())
		r.disp.refuse["T-1"] = "no spec for T-1 in obor serve's store" // what the real Check says to ""
		r.start()
		if _, err := r.dispatch("d1", "L-01"); err == nil || !strings.Contains(err.Error(), "no spec for T-1") {
			t.Fatalf("dispatch without a stored spec: %v; want refused", err)
		}
		if n := len(r.records()); n != 0 {
			t.Errorf("a refused dispatch recorded %q", r.steps())
		}
		r.setSpec("s1", "# T-1 — Stored\n\nDifficulty: medium\n")
		r.disp.mu.Lock()
		delete(r.disp.refuse, "T-1")
		r.disp.mu.Unlock()
		r.mustDispatch("d2")
		r.disp.mu.Lock()
		texts := slices.Clone(r.disp.texts)
		r.disp.mu.Unlock()
		if !slices.Equal(texts, []string{"", "# T-1 — Stored\n\nDifficulty: medium\n"}) {
			t.Errorf("Specs.Check got %q; want \"\" before the set, the stored spec after", texts)
		}
	})
}

// A fix job carries the stored spec when its rev is not the one the worker last had (a change made while the task was
// not working; a change while it works reaches it as the refusal, and the next fix job does not repeat it); every gate
// gets the current rev to write into the candidate.
func TestFixJobCarriesChangedSpec(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		widen bool
	}{
		{"unchanged", false},
		{"widened while working: the refusal carried it", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t)
				r.work("red.txt", "red\n")
				r.scratch.Script("Test", "T-1", port.OutcomeRed)
				r.start()
				r.setSpec("s1", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`\n")
				r.submit()
				job := r.waitJob("pj-l-01", "T-1/2/fix")
				if !strings.Contains(job, "The spec of T-1 (rev 1") { // the worker's last job carried none
					t.Errorf("first fix job has no spec rev 1:\n%s", job)
				}
				r.waitStep("1 fact sendback")
				if c.widen {
					r.setSpec("s2", "# T-1 — Red\n\nDifficulty: medium\n\n## Files you may touch\n`red.txt`, `more.txt`\n")
					// The worker fetched its fix job at the old rev: its task_done is refused once, then submits.
					if reply, err := r.taskDone(report); err == nil || !strings.Contains(err.Error(), "spec is now rev 2") {
						t.Fatalf("task_done at the old rev: %q, %v; want the refusal naming rev 2", reply, err)
					}
				}
				r.submit()
				r.waitStep("2 fact submitted")
				job = r.waitJob("pj-l-01", "T-1/3/fix")
				if strings.Contains(job, "The spec of T-1") {
					t.Errorf("second fix job repeats a spec the worker has:\n%s", job)
				}
				var specs []string
				for _, in := range r.scratch.Inputs() {
					specs = append(specs, in.Spec)
				}
				if len(specs) != 2 || !strings.Contains(specs[0], "`red.txt`\n") ||
					strings.Contains(specs[1], "more.txt") != c.widen {
					t.Errorf("gate specs %q; want the current rev at each gate", specs)
				}
			})
		})
	}
}

// spec_set's receipt is a folded fact, so a retry of a call id after a serve restart, with another writer's spec in
// between, answers the original reply and does not write the old text over the newer spec.
func TestSpecSetRetryAfterRestartPreservesNewerSpec(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		a, b := "# T-1 — A\n\nDifficulty: medium\n", "# T-1 — B\n\nDifficulty: medium\n"
		if got := r.setSpec("s1", a); got != "SET T-1 rev 1" {
			t.Fatalf("precondition: s1 replied %q", got)
		}
		if got := r.setSpec("s2", b); got != "SET T-1 rev 2" {
			t.Fatalf("precondition: s2 replied %q", got)
		}
		r.stop()
		r.start() // serve restarts: the live reply cache is gone
		if got := r.setSpec("s1", a); got != "SET T-1 rev 1" {
			t.Errorf("s1 retried after the restart: %q, want its original SET T-1 rev 1", got)
		}
		row, ok, err := r.specRow("T-1")
		if err != nil || !ok || row.Rev != 2 || row.Text != b {
			t.Errorf("spec after the retry %+v, %v, %v; want B at rev 2", row, ok, err)
		}
	})
}

// Simultaneous callers of one call id all get the first call's reply, and the spec is set once.
func TestSpecSetConcurrentRetriesShareReply(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.start()
		text := "# T-1 — A\n\nDifficulty: medium\n"
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				got, err := r.ask(
					&wire.SpecSetReq{Envelope: wire.Envelope{Root: r.p.Root, Role: "lead", CallID: "same"}, Task: "T-1",
						Spec: text})
				if err != nil || got != "SET T-1 rev 1" {
					t.Errorf("concurrent retry: %q, %v; want the original SET T-1 rev 1", got, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		row, ok, err := r.specRow("T-1")
		if err != nil || !ok || row.Rev != 1 {
			t.Errorf("spec after the concurrent retries %+v, %v, %v; want rev 1", row, ok, err)
		}
	})
}
