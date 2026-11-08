package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// startedHead is the head the log's review.started names ("": none).
func startedHead(t *testing.T, h *harness) string {
	t.Helper()
	for _, rec := range readLog(t, h.root) {
		if rec.Task == event.ReviewTask && rec.Step == event.StepReviewStarted {
			var d ReviewData
			_ = json.Unmarshal(rec.Data, &d)
			return d.Head
		}
	}
	return ""
}

// T-853: the reviewer or the deep dive working — its pane working, a brief open to it — holds no merge: T-1 publishes
// in the same pass, and obor status shows no hold phase.
func TestMergeDuringAReviewPublishesAtOnce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, pane, to string }{
		{"reviewer working", "w1:rev", ""}, {"deep dive working", "w1:dd", ""},
		{"reviewer briefed", "", "pj-rev"}, {"deep dive briefed", "", "pj-deep-dive"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedRoles(t)
			ctx := context.Background()
			if c.pane != "" {
				h.work(c.pane, true)
			}
			if c.to != "" {
				h.a.opt.Loop = func(string) (*Loop, error) {
					return &Loop{Workflow: WorkflowOf(&schedScratch{h: h}, &reviewGitFake{head: "base1"}), Agents: &agentsSpy{}}, nil
				}
				if _, err := h.a.briefCall(wire.BriefReq{Envelope: wire.Envelope{Root: h.root, CallID: "b1",
					Role: "lead"}, To: c.to, Text: "look"}); err != nil {
					t.Fatal(err)
				}
				f := &schedScratch{h: h}
				h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{Workflow: WorkflowOf(f), Agents: f, Typer: f}, nil }
			}
			h.a.mu.Lock()
			briefed := c.to != "" && h.a.p.out.Brief(c.to) != nil
			working := c.pane != "" && !h.a.roleIdle(h.a.p, map[string]string{"w1:rev": "pj-rev", "w1:dd": "pj-deep-dive"}[c.pane])
			h.a.mu.Unlock()
			if !briefed && !working {
				t.Fatalf("precondition: the role neither briefed nor working")
			}
			if got := h.phase("T-1"); got != wire.PhasePublish {
				t.Errorf("accepted, %s: phase %q, want %q (no merge hold)", c.name, got, wire.PhasePublish)
			}
			h.pass(ctx)
			if got := h.state("T-1"); got != stMerged {
				t.Errorf("%s: T-1 %s after one pass, want merged", c.name, got)
			}
		})
	}
}

// The role worktree agents are the current config's: a deep dive added or a role removed while serve runs counts at
// the next check; a config that does not load keeps the last good set.
func TestWorktreeAgentsFollowTheConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, from, to, agent string
		before, after         string // the agent's role worktree role before and after the change
	}{
		{"deep dive added", `{"name":"pj","prefix":"pj","roles":{"integrator":"","reviewer":""}}`, admitCfg,
			"pj-deep-dive", "", "deep_dive"},
		{"role removed", admitCfg, `{"name":"pj","prefix":"pj","roles":{"integrator":"","deep_dive":""}}`,
			"pj-rev", "reviewer", ""},
		{"config broken", admitCfg, `{"name":`, "pj-rev", "reviewer", "reviewer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, _ := seedRoles(t)
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.from)
			role := func() string {
				h.a.mu.Lock()
				defer h.a.mu.Unlock()
				return h.a.p.wtRole(c.agent)
			}
			if got := role(); got != c.before {
				t.Fatalf("precondition: %s's role %q, want %q", c.agent, got, c.before)
			}
			h.write(filepath.Join(h.root, ".obor", "config.json"), c.to)
			if got := role(); got != c.after {
				t.Errorf("after the change: %s's role %q, want %q", c.agent, got, c.after)
			}
		})
	}
}

// T-733: an admission in force (config_set's, T-872: the only one) between the publish's selection and its execution
// refuses the publish's start (scratchStep decides at the start, not pickAccepted at the selection); released, the
// next pass publishes.
func TestAdmissionDecidedAtPublishStart(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, false)
	h, ctx := r.h, context.Background()
	s := r.selectPublish()
	release, err := h.a.admit(ctx, h.a.p)
	if err != nil {
		t.Fatal(err)
	}
	s(ctx, r.fx)
	h.a.effects.Wait()
	if n := len(r.s.began); n != 0 || countStep(t, h, nth(event.StepPublish, 1)) != 0 || h.state("T-1") != stAccepted {
		t.Fatalf("publish T-1 started while admitted: %d publishes, T-1 %s", n, h.state("T-1"))
	}
	release()
	h.pass(ctx)
	if got := h.state("T-1"); got != stMerged {
		t.Errorf("released: T-1 %s, want merged", got)
	}
}

// T-872: a publish runs during a review's start and both complete. The start reads the head (base1) and goes on with
// it while T-1's publish runs: the review starts on base1, the reviewer's worktree there, before the publish ends;
// released, T-1 merges, and its merge falls into the next review's scope.
func TestPublishDuringAReviewStart(t *testing.T) {
	t.Parallel()
	r := newAdmitRig(t, true)
	h, ctx := r.h, context.Background()
	if _, err := h.a.reviewCall(wire.ReviewReq{Envelope: wire.Envelope{Root: h.root, CallID: "r1", Role: "lead",
		Agent: "pj-lead"}}); err != nil {
		t.Fatal(err)
	}
	began := ""
	r.git.onHead = func() { // inside the review's start, at its head read: a pass begins T-1's publish
		h.a.advance(ctx)
		select {
		case began = <-r.s.began:
		case <-time.After(5 * time.Second):
		}
	}
	h.a.reviewPass(ctx, h.a.p, r.fx)
	if began != "T-1" {
		t.Fatalf("the publish of %q began during the review's start, want T-1's", began)
	}
	if got := h.state("T-1"); got == stMerged {
		t.Fatalf("precondition: T-1 merged before its publish was released")
	}
	if len(r.agents.launches) != 1 || startedHead(t, h) != "base1" || r.git.detachedAt() != "reviewer@base1" {
		t.Errorf("publish running: %d launches, started on %q, detached %q; want the review started on base1, the"+
			" reviewer's worktree there", len(r.agents.launches), startedHead(t, h), r.git.detachedAt())
	}
	r.released(ctx)
	if got := h.state("T-1"); got != stMerged {
		t.Errorf("released: T-1 %s, want merged", got)
	}
}
