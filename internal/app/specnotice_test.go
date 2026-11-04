package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"slices"
	"strings"
	"testing"
)

// T-821 (RV-105 #1, #3): a spec rev's line to a working worker is its own kind of line: delivered to a busy worker
// whose kind's inbox reaches a working session (Claude's), else held until the pane is idle; kept across an ask, an
// answer, an unblock and a submission; retired once the worker saw the rev (spec.seen, a job fetched at it), the task
// ended (merged, dropped, rejected) or a newer rev's line replaced it.

// specRevText is the spec line the tests queue to pj-l-01 for T-1 at rev.
func specRevText(rev int) string { return "[obor] " + specRevLine("T-1", rev, "widen the guard") }

// specQueued is the ident harness with pj-l-01 of kind identified in w1:p1, busy (working), its inbox recorded, and
// T-1's spec line at rev 2 recorded (fact spec.line).
func specQueued(t *testing.T, kind string) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.pane("w1:p1").Agent = kind
	h.started("pj-l-01", "w1:p1", "t1", "s1")
	h.connect()
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.status("w1:p1", func(p *port.Pane) { p.Status = "working" })
	if w := h.where("pj-l-01"); !strings.HasPrefix(w, "identified w1:p1 ") {
		t.Fatalf("precondition: pj-l-01 is %q, want identified at w1:p1", w)
	}
	specLineRec(t, h, 2)
	return h
}

// specLineRec records T-1's spec line at rev to pj-l-01.
func specLineRec(t *testing.T, h *harness, rev int) {
	t.Helper()
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepSpecLine, Kind: event.KindFact},
		event.Message{To: "pj-l-01", From: "pj-lead", Line: specRevText(rev), Task: "T-1"}) {
		t.Fatal("spec line not recorded")
	}
}

// A busy Claude worker gets the line posted to its session's inbox now; a busy Codex or opencode worker gets nothing
// until its pane is idle, then the line typed, once.
func TestSpecLineToABusyWorkerByKind(t *testing.T) {
	t.Parallel()
	t.Run("claude", func(t *testing.T) {
		t.Parallel()
		h := specQueued(t, "claude")
		in, liner := &kindSpy{}, &linerSpy{}
		h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
		if !slices.Equal(in.calls, []string{"claude pj-l-01: " + specRevText(2)}) || len(liner.calls) != 0 ||
			len(h.lines()) != 0 {
			t.Errorf("posted %q, typed %q, queued %q; want the line posted to the busy worker's inbox", in.calls,
				liner.calls, h.lines())
		}
	})
	for _, kind := range []string{"codex", "opencode"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			h := specQueued(t, kind)
			in, liner := &kindSpy{}, &linerSpy{}
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
			if len(in.calls) != 0 || len(liner.calls) != 0 || len(h.lines()) != 1 {
				t.Fatalf("busy: posted %q, typed %q, queued %q; want the line held", in.calls, liner.calls, h.lines())
			}
			h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
			h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: in, Typer: liner})
			if len(in.calls) != 0 || !slices.Equal(liner.calls, []string{"w1:p1: " + specRevText(2)}) ||
				len(h.lines()) != 0 {
				t.Errorf("idle: posted %q, typed %q, queued %q; want the line typed once", in.calls, liner.calls, h.lines())
			}
		})
	}
}

// A spec line stays queued across what settles an answer (an ask, an answer, an unblock, a submission) and is retired
// by what shows its worker the rev or ends the task; a newer rev's line replaces it. Its worker idle afterwards gets
// the kept line (or the newer one), never a retired one.
func TestSpecLineKeptAndRetired(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		rec  event.Record
		data any
		want []string // the lines typed at the next idle
	}{
		{"ask", event.Record{Task: "T-1", Attempt: 1, Step: event.StepNeedsYou, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-l-01", Reason: reasonAsks + "may I edit x.go?"}, []string{specRevText(2)}},
		{"answer", event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact},
			event.Message{To: "pj-l-01", From: "pj-lead", Line: "[obor answer] yes", Task: "T-1"},
			[]string{specRevText(2), "[obor answer] yes"}},
		{"unblock", event.Record{Task: "T-1", Attempt: 1, Step: event.StepUnblocked, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-lead", Reason: wire.ResumeReprompt, Merge: true}, []string{specRevText(2)}},
		{"submit", event.Record{Task: "T-1", Attempt: 1, Step: event.StepSubmitted, Kind: event.KindFact},
			event.FactData{Lane: "L-01", Agent: "pj-l-01", Merge: true}, []string{specRevText(2)}},
		{"spec seen", event.Record{Task: "T-1", Attempt: 1, Step: event.StepSpecSeen, Kind: event.KindFact},
			event.FactData{Rev: 2, Call: "t1", Reason: "task_done: refused"}, nil},
		{"job fetched at the rev", event.Record{Task: "T-1", Attempt: 1, Step: event.StepDelivered, Kind: event.KindFact},
			event.FactData{Job: "T-1/1/work", Via: "mcp", Rev: 2}, nil},
		{"merged", event.Record{Task: "T-1", Attempt: 1, Step: event.StepMerged, Kind: event.KindFact},
			event.MergedData{Head: "abc"}, nil},
		{"dropped", event.Record{Task: "T-1", Attempt: 1, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Reason: "not needed"}, nil},
		{"rejected", event.Record{Task: "T-1", Attempt: 1, Step: verdictStep(wire.MethodReject, "r1"), Kind: event.KindResult},
			verdictResult{Reply: "REJECTED T-1"}, nil},
		{"newer rev", event.Record{Task: event.MsgTask, Step: event.StepSpecLine, Kind: event.KindFact},
			event.Message{To: "pj-l-01", From: "pj-lead", Line: specRevText(3), Task: "T-1"}, []string{specRevText(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := specQueued(t, "opencode")
			if q := h.lines(); len(q) != 1 {
				t.Fatalf("precondition: one queued line, got %q", q)
			}
			if !h.a.record(h.proj(), tc.rec, tc.data) {
				t.Fatal("record refused")
			}
			h.status("w1:p1", func(p *port.Pane) { p.Status = "idle" })
			liner := &linerSpy{}
			for range 3 { // one line per agent per pass
				h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: &kindSpy{}, Typer: liner})
			}
			var want []string
			for _, l := range tc.want {
				want = append(want, "w1:p1: "+l)
			}
			if !slices.Equal(liner.calls, want) || len(h.lines()) != 0 {
				t.Errorf("typed %q, queued %q; want %q", liner.calls, h.lines(), want)
			}
		})
	}
}

// A spec line whose delivery is in flight (its intent in the log) when its worker sees the rev may have landed: it
// stays until its outcome, as an answer's does (T-748); a spec line of another task is not retired.
func TestSpecLineInFlightOrOtherTaskStays(t *testing.T) {
	t.Parallel()
	h := specQueued(t, "opencode")
	seq := h.proj().queuedLines()[0].seq
	claimIntent(t, h, seq)
	if !h.a.record(h.proj(), event.Record{Task: event.MsgTask, Step: event.StepSpecLine, Kind: event.KindFact},
		event.Message{To: "pj-l-02", From: "pj-lead", Line: "[obor] T-2 spec is now rev 2", Task: "T-2"}) {
		t.Fatal("T-2's line not recorded")
	}
	if !h.a.record(h.proj(), event.Record{Task: "T-1", Attempt: 1, Step: event.StepSpecSeen, Kind: event.KindFact},
		event.FactData{Rev: 2}) {
		t.Fatal("spec.seen not recorded")
	}
	if s, ok := lineAt(h, h.proj(), seq); !ok || s != lineSendingSettled {
		t.Errorf("the in-flight line: state %d (queued %v), want sending, settled", s, ok)
	}
	if q := h.lines(); len(q) != 2 {
		t.Errorf("queued %q; want the in-flight line and T-2's kept", q)
	}
}
