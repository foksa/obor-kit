package app_test

import (
	"context"
	"fmt"
	"obor/internal/app/wire"
	"obor/internal/herdr/herdrtest"
	"strings"
	"testing"
	"time"
)

// T-819: a spec set reaches a working worker (one line, the reply says so) and a task_done of work done against an
// older rev is refused once, with the same rev and note.

// specRevLine is what a set of a working task's spec sends its worker, and what its task_done at the older rev says.
func specRevLine(task string, rev int, note string) string {
	line := fmt.Sprintf("%s spec is now rev %d", task, rev)
	if note != "" {
		line += ": " + note
	}
	return line + " — read obor spec show " + task + " and adjust; say in your report what you redid."
}

// setSpecTask is the lead's obor spec set of task with note, text as its spec.
func (r *loopRig) setSpecTask(callID, task, text, note string) (string, error) {
	return wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Agent: "lead",
		Role: "lead", CallID: callID, Task: task, Spec: text, Note: note})
}

// setSpecT is setSpecTask of T-1, fatal on an error.
func (r *loopRig) setSpecT(callID, text, note string) string {
	r.t.Helper()
	reply, err := r.setSpecTask(callID, "T-1", text, note)
	if err != nil {
		r.t.Fatalf("spec set: %v", err)
	}
	return reply
}

// workingT1 is the fetch rig with T-1's spec (rev 1) stored, dispatched, and its work job fetched by pj-l-01 at rev 1:
// the task working, the worker able to submit. serve is connected to herdr on the stream after the worker's start's
// refresh: spec set delivers its line at once (T-821), and in the resubscribe's gap it would find pj-l-01 unidentified.
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

// A spec set on a working task sends its worker one line through the worker-line delivery (the line names the new rev
// and the note), and the reply says so. A repeated call id gets the stored reply and sends nothing.
func TestSpecSetTellsTheWorkingWorker(t *testing.T) {
	t.Parallel()
	r := workingT1(t)
	want := "SET T-1 rev 2 (worker told)"
	if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\n## Files you may touch\n`more.txt`\n",
		"widen the guard"); got != want {
		t.Fatalf("spec set on a working task: %q, want %q", got, want)
	}
	line := "[obor] " + specRevLine("T-1", 2, "widen the guard")
	r.waitFor("the spec line posted to pj-l-01", func() bool { return r.inbox.count("pj-l-01", line) == 1 })
	r.passes()
	if n := r.inbox.count("pj-l-01", line); n != 1 {
		t.Errorf("the spec line posted %d times, want once", n)
	}
	if n := r.taskStep("msg", "fact spec.line"); n != 1 {
		t.Errorf("spec.line facts %d, want one: %q", n, r.steps())
	}
	if got := r.setSpecT("s2", "# T-1 — other text\n\nDifficulty: medium\n", "another note"); got != want {
		t.Errorf("the same call id again: %q, want the stored reply %q", got, want)
	}
	if n := r.taskStep("msg", "fact spec.line"); n != 1 {
		t.Errorf("the repeated call id sent another line: %q", r.steps())
	}
}

// A task with no working worker gets no line: a waiting one (no lane yet) and a merged one. The reply is the plain
// set line.
func TestSpecSetTellsNoWorkerWhenNotWorking(t *testing.T) {
	t.Parallel()
	t.Run("waiting", func(t *testing.T) {
		t.Parallel()
		r := startWithT2(t)
		if reply, err := r.waitAfter("d1", "T-2"); err != nil || reply != waitingAfter2 {
			t.Fatalf("precondition: T-1 waits for T-2: %q, %v", reply, err)
		}
		reply, err := r.setSpecTask("s1", "T-1", "# T-1 — Widened\n\nDifficulty: medium\n", "widen it")
		if err != nil || reply != "SET T-1 rev 1" {
			t.Fatalf("spec set on a waiting task: %q, %v; want SET T-1 rev 1", reply, err)
		}
		if n := r.taskStep("msg", "fact spec.line"); n != 0 {
			t.Errorf("a waiting task's spec set sent %d lines: %q", n, r.steps())
		}
	})
	t.Run("merged", func(t *testing.T) {
		t.Parallel()
		r := newLoopRig(t, "echo 1 pass")
		r.p.Dispatched("L-01")
		r.p.SubmitAgo("L-01", 0)
		r.p.MergedAt("T-1", "sha", true, time.Now().Unix())
		r.start()
		if n := r.taskStep("T-1", "fact merged"); n != 1 {
			t.Fatalf("precondition: T-1's merged fact in the log: %d, want 1", n)
		}
		reply, err := r.setSpecTask("s1", "T-1", "# T-1 — Widened\n\nDifficulty: medium\n", "widen it")
		if err != nil || reply != "SET T-1 rev 1" {
			t.Fatalf("spec set on a merged task: %q, %v; want SET T-1 rev 1", reply, err)
		}
		if n := r.taskStep("msg", "fact spec.line"); n != 0 {
			t.Errorf("a merged task's spec set sent %d lines: %q", n, r.steps())
		}
	})
}

// A task_done of work fetched at an older rev is refused once, naming the new rev and the set's note, and submits
// nothing; the refusal marks the rev seen, so the worker's next task_done submits.
func TestTaskDoneRefusesAnOlderSpecRev(t *testing.T) {
	t.Parallel()
	r := workingT1(t)
	if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\n## Files you may touch\n`more.txt`\n",
		"widen the guard"); got != "SET T-1 rev 2 (worker told)" {
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
}

// A task_done on the rev its job carried submits: a spec set before the worker's fetch is the job's rev.
func TestTaskDoneOnTheJobsSpecRevSubmits(t *testing.T) {
	t.Parallel()
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
}

// A Blocked: report is no submission and no refusal, whatever rev the worker's job carried.
func TestBlockedTaskDoneOlderSpecRevAccepted(t *testing.T) {
	t.Parallel()
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
}

// A move after a spec set starts its new attempt on the new rev: its work job carries rev 2, and the new worker's
// task_done submits without a refusal.
func TestMoveAfterSpecSetStartsOnTheNewRev(t *testing.T) {
	t.Parallel()
	r := newTakeOverRig(t)
	jr := r.workerNext("n1")
	if !strings.Contains(jr.Text, "The spec of T-1 (rev 1") {
		t.Fatalf("precondition: attempt 1's job carries rev 1: %q", jr.Text)
	}
	if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n", "widen the guard"); got !=
		"SET T-1 rev 2 (worker told)" {
		t.Fatalf("spec set: %q", got)
	}
	if reply, err := r.moveTo("m1", "opus"); err != nil ||
		!strings.HasPrefix(reply, "MOVED T-1 on L-01: sonnet → opus") {
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
}

// T-821: a spec notice is queued, delivered or seen, and spec set's reply says which; a stale task_done's refusal is
// the call id's answer for good.

// workerKind makes pj-l-01's pane run kind, busy (working), and waits until the app sees it, connected to herdr on
// a stream after the rig's pushes (connectedAgain: spec set's delivery at once must not land in a resubscribe's gap).
func (r *loopRig) workerKind(kind string) {
	r.t.Helper()
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind = kind })
	r.workerIs("working")
	r.connectedAgain()
}

// A busy Claude worker is told at once (its inbox reaches a working session): the reply says so. A busy Codex or
// opencode worker is not: the reply says why it is queued, nothing is typed while it works, and the line is typed
// once its pane is idle.
func TestSpecSetBusyWorkerToldOrQueued(t *testing.T) {
	t.Parallel()
	const text = "# T-1 — Work\n\nDifficulty: medium\n\nmore\n"
	line := "[obor] " + specRevLine("T-1", 2, "widen the guard")
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		r := workingT1(t)
		r.workerKind("claude")
		if got := r.setSpecT("s2", text, "widen the guard"); got != "SET T-1 rev 2 (worker told)" {
			t.Fatalf("spec set to a busy Claude worker: %q, want SET T-1 rev 2 (worker told)", got)
		}
		if n := r.inbox.count("pj-l-01", line); n != 1 {
			t.Errorf("the line posted %d times by the time spec set replied, want once", n)
		}
		r.passes()
		if n, typed := r.inbox.count("pj-l-01", line), r.typedTo("pj-l-01", line); n != 1 || typed != 0 {
			t.Errorf("posted %d, typed %d after the passes; want posted once, never typed", n, typed)
		}
	})
	for _, kind := range []string{"codex", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := workingT1(t)
			r.workerKind(kind)
			if got := r.setSpecT("s2", text, "widen the guard"); got != "SET T-1 rev 2 (queued: busy)" {
				t.Fatalf("spec set to a busy %s worker: %q, want SET T-1 rev 2 (queued: busy)", kind, got)
			}
			r.passes()
			if n, typed := r.inbox.count("pj-l-01", line), r.typedTo("pj-l-01", line); n != 0 || typed != 0 {
				t.Fatalf("while busy: posted %d, typed %d; want nothing", n, typed)
			}
			r.workerIs("idle")
			r.waitFor("the line typed at idle", func() bool { return r.typedTo("pj-l-01", line) == 1 })
			r.passes()
			if n, typed := r.inbox.count("pj-l-01", line), r.typedTo("pj-l-01", line); n != 0 || typed != 1 {
				t.Errorf("after idle: posted %d, typed %d; want typed once", n, typed)
			}
		})
	}
}

// A task_done while the line is still queued (its worker busy) is refused by the rev check, and that retires the line:
// the worker idle afterwards gets nothing typed; its next task_done submits.
func TestSpecLineQueuedSubmissionRefusedThenRetired(t *testing.T) {
	t.Parallel()
	r := workingT1(t)
	r.workerKind("opencode")
	if got := r.setSpecT("s2", "# T-1 — Work\n\nDifficulty: medium\n\nmore\n", "widen the guard"); got !=
		"SET T-1 rev 2 (queued: busy)" {
		t.Fatalf("precondition: the line queued: %q", got)
	}
	want := specRevLine("T-1", 2, "widen the guard")
	if reply, err := r.callDone("pj-l-01", "worker", "t1", report); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("task_done with the line queued: %q, %v; want the refusal naming %q", reply, err, want)
	}
	r.workerIs("idle")
	r.passes()
	r.passes()
	if n := r.typedTo("pj-l-01", "[obor] "+want); n != 0 {
		t.Errorf("the line typed %d times after the refusal showed the rev; want retired", n)
	}
	if reply, err := r.callDone("pj-l-01", "worker", "t2", report); err != nil || reply != "submitted" {
		t.Fatalf("task_done after the refusal: %q, %v; want submitted", reply, err)
	}
}

// A refused task_done whose reply was lost is sent again with its call id (wire.verdictAsk on a reset or EOF): it gets
// the same refusal, also after a serve restart (the log's replay), and submits nothing; only a new call id submits.
func TestTaskDoneSpecRefusalSurvivesARetry(t *testing.T) {
	t.Parallel()
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
}
