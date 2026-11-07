package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"sync"
	"testing"
	"time"
)

// RV-99 #4: obor resume --regate of a worker's question and its task_done interleaved (hooks at their decisions): one
// is recorded, the other refused by the state the first left, and a late submission never brings the gated task back
// from green to submitted. Both orders: resume waiting on the lock while task_done records (task_done wins, the resume
// is refused: submitted), task_done waiting while the resume records (the regate wins, task_done refused: submitted).
func TestResumeSerialisesWithTaskDone(t *testing.T) {
	t.Parallel()
	report := "Summary: x\nSelf-check: x\nSkipped: x\nOpen questions: none."
	for _, c := range []struct {
		name      string
		submitted bool   // task_done wins (else the regate)
		point     string // the hook point that starts the other call
	}{
		{"resume during task_done's decision", true, hookTaskDoneCheck},
		{"task_done during the resume's decision", false, hookResumeLocked},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, p, fact := answerSeed(t, reasonAsks+"re-gate, not my diff", "", "", true)
			h.a.mu.Lock()
			if !isAsk(fact) || !p.asks("T-1") || p.folded.loop["T-1"].state != stWorking && p.folded.loop["T-1"].state != stDispatched {
				h.a.mu.Unlock()
				t.Fatalf("precondition: T-1 %s with its question in force", p.folded.loop["T-1"].state)
			}
			h.a.mu.Unlock()
			resume := func() (string, error) {
				return h.a.resume(wire.Call{Root: h.root, Role: "lead", Agent: "pj-lead", CallID: "r1", Task: "T-1",
					To: wire.ResumeRegate})
			}
			submit := func(id string) (string, error) {
				return h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-01", CallID: id,
					Task: "T-1", Report: report})
			}
			from := h.lastSeq()
			done := make(chan error, 1)
			var once sync.Once
			waits := make(chan struct{})
			hook := func(point string) {
				switch {
				case point == hookResumeWaits && c.submitted, point == hookTaskDoneWaits && !c.submitted:
					close(waits)
				case point == c.point:
					once.Do(func() {
						if c.submitted { // the resume starts and waits on a.submitMu, task_done records meanwhile
							go func() { _, err := resume(); done <- err }()
							<-waits
							return
						}
						go func() { _, err := submit("s1"); done <- err }()
						<-waits // task_done waits on a.submitMu (the resume holds it), the resume records meanwhile
					})
				}
			}
			h.a.hooked.Store(&hook)
			var first error
			if c.submitted {
				_, first = submit("s1")
			} else {
				_, first = resume()
			}
			var second error
			select {
			case second = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the interleaved call never answered")
			}
			none := func(string) {}
			h.a.hooked.Store(&none)
			doneErr, resumeErr := first, second
			if !c.submitted {
				doneErr, resumeErr = second, first
			}
			facts := h.facts(from)
			submits, regates := 0, 0
			for _, f := range facts {
				switch {
				case strings.Contains(f, "submitted"):
					submits++
				case strings.Contains(f, "unblocked{regate}"):
					regates++
				}
			}
			if c.submitted {
				if doneErr != nil || resumeErr == nil || !strings.Contains(resumeErr.Error(), "T-1 is submitted") {
					t.Errorf("task_done %v, resume %v; want submitted, the resume refused by its new state", doneErr, resumeErr)
				}
				if submits != 1 || regates != 0 {
					t.Errorf("facts %q: want one submitted, no unblocked{regate}", facts)
				}
			} else {
				if resumeErr != nil || doneErr == nil || !strings.Contains(doneErr.Error(), "T-1 is submitted, not open for a submission") {
					t.Errorf("resume %v, task_done %v; want the regate, task_done refused by its new state", resumeErr, doneErr)
				}
				if submits != 0 || regates != 1 {
					t.Errorf("facts %q: want one unblocked{regate}, no submitted", facts)
				}
			}
			h.a.mu.Lock()
			mt := *p.folded.loop["T-1"]
			h.a.mu.Unlock()
			if mt.state != stSubmitted || mt.attempt != 1 {
				t.Fatalf("T-1 %s attempt %d after the race; want submitted in attempt 1", mt.state, mt.attempt)
			}
			addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"}) // the lane's commit, then its gate
			gate := p.folded.loop["T-1"].stepName()
			addRecord(t, h, "T-1", gate, event.KindIntent, gateIn{Kind: "lane", Lane: "L-01", Input: "s1"})
			addRecord(t, h, "T-1", gate, event.KindResult, gateOut{Outcome: outGreen,
				Candidate: port.Candidate{Task: "T-1", SHA: "c", Target: "b", Patch: "p"}})
			if st := p.folded.loop["T-1"].state; st != stGreen {
				t.Fatalf("precondition: T-1 %s after its gate; want green", st)
			}
			if _, err := submit("s2"); err == nil {
				t.Error("a late task_done of a green task was recorded")
			}
			h.a.mu.Lock()
			defer h.a.mu.Unlock()
			if st := p.folded.loop["T-1"].state; st != stGreen {
				t.Errorf("T-1 %s after a late submission; want green (never back to submitted)", st)
			}
		})
	}
}

// The worker's submission can already be decided when the merge loop discovers
// that the owner merged its task outside obor. That terminal fact must win.
func TestTaskDoneCannotReopenConcurrentOutsideMerge(t *testing.T) {
	t.Parallel()
	h, p, _ := answerSeed(t, reasonAsks+"owner deciding", "", "", true)
	addRecord(t, h, "T-1", event.StepWork, event.KindIntent, promptData{Agent: "pj-l-01"})
	addRecord(t, h, "T-1", event.StepDelivered, event.KindFact, event.FactData{Job: "T-1/1/work"})
	hook := func(point string) {
		if point == hookTaskDoneCheck {
			h.a.drive(context.Background(), p, &Loop{Scratch: landedScratch{}}, "T-1")
			if st := p.folded.loop["T-1"].state; st != stMerged {
				t.Fatalf("precondition: outside merge left %s", st)
			}
		}
	}
	h.a.hooked.Store(&hook)
	_, err := h.a.taskDone(wire.Call{Root: h.root, Role: "worker", Agent: "pj-l-01", Lane: "L-01", Task: "T-1",
		CallID: "late", Report: "Summary: done\nSelf-check: pass\nSkipped: none\nOpen questions: none"})
	if err == nil {
		t.Error("late submission reported success after the task merged")
	}
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("late task_done reopened task as %s", st)
	}
	// The lane may already have been reused before a legacy late fact lands.
	addRecord(t, h, "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-01", Agent: "pj-l-01"})
	addRecord(t, h, "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "new-base"})
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Fatalf("precondition: lane holds %q, want T-2", got)
	}
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01"})
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("late submitted fact reopened task as %s", st)
	}
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Errorf("late submission reclaimed reused lane: %q", got)
	}
	p = reloadProj(t, h)
	if st := p.folded.loop["T-1"].state; st != stMerged {
		t.Errorf("replay reopened task as %s", st)
	}
	if got := p.laneTask("L-01"); got != "T-2" {
		t.Errorf("replay reclaimed reused lane: %q", got)
	}
}
