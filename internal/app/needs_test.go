package app

import (
	"context"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// TestBriefSupersededEndsNeedsYou: the 24 h needs-you of a brief line ends when a newer brief supersedes it;
// the newer brief's own line waits as usual.
func TestBriefSupersededEndsNeedsYou(t *testing.T) {
	t.Parallel()
	now := time.Now()
	a, p := lineApp(t, lineRoot(t), &now)
	lineStatus(a, "w:rev", "working", now)
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b1", Role: "lead"},
		To: "pj-rev", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	l := &FakeAgents{}
	a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, &Loop{Prompter: l, Inbox: l}), nil }
	now = now.Add(25 * time.Hour)
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 1 {
		t.Fatalf("precondition: the 25 h brief line's needs-you, got %d", len(p.folded.active[event.MsgTask]))
	}
	if _, err := a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: p.root, CallID: "b2", Role: "lead"},
		To: "pj-rev", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	a.advance(context.Background())
	a.advance(context.Background())
	if len(p.folded.active[event.MsgTask]) != 0 {
		t.Errorf("the needs-you of the superseded line is still in force: %+v", p.folded.active[event.MsgTask])
	}
	if q := p.out.Queued(); len(q) != 1 || q[0].Msg().Line != "[obor owner] second" || len(l.Lines()) != 0 {
		t.Errorf("queued %v, typed %v; want the newer brief's line alone, waiting", q, l.Lines())
	}
}

// msgNeeds is the needs-you facts of h's lines.
func msgNeeds(h *harness) []string {
	var out []string
	for _, f := range h.a.Status().Project.Facts {
		if f.Task == event.MsgTask && f.Step == event.StepNeedsYou {
			out = append(out, f.Fact().Reason)
		}
	}
	return out
}
