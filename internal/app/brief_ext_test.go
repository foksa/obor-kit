package app_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// briefWorked is the line rig with pj-rev briefed, its line delivered and its work seen (working, no idle yet).
func briefWorked(t *testing.T) *loopRig {
	r := lineRig(t, "idle", "idle")
	r.start()
	if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify finding 3"}); err != nil {
		t.Fatal(err)
	}
	r.waitBriefDelivered()
	r.agentIs("w1:p6", "pj-rev", "working")
	r.waitFor("the work in the log", func() bool { return r.taskStep(event.MsgTask, "fact worked") == 1 })
	r.passes()
	if finishedLines(r) != 0 || len(r.briefs()) != 1 {
		t.Fatalf("working: %d finished lines, briefs %+v; want none, one open", finishedLines(r), r.briefs())
	}
	return r
}

func TestBriefEndsWhenItsAgentIsGone(t *testing.T) {
	t.Parallel()
	t.Run("exits at its shell", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := briefWorked(t)
			r.agentGone("w1:p6", "pj-rev", func(p *port.Pane) { p.Agent, p.Status = "", "" })
			r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
			r.passes()
			if n, b := finishedLines(r), r.briefs(); n != 1 || len(b) != 0 {
				t.Errorf("%d finished lines, briefs %+v; want one, none open", n, b)
			}
		})
	})
	t.Run("pane closed", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := briefWorked(t)
			r.removePane("w1:p6")
			r.waitFor("the finished line", func() bool { r.push(); return finishedLines(r) == 1 })
			if b := r.briefs(); len(b) != 0 {
				t.Errorf("briefs %+v, want none open", b)
			}
		})
	})
	t.Run("not identified", func(t *testing.T) {
		t.Parallel()
		bubble(t, func(t *testing.T) {
			r := briefWorked(t)
			// The pane still runs its agent, but nothing proves it is pj-rev (its label and name gone).
			r.setName("w1:p6", "")
			r.agentGone("w1:p6", "pj-rev", func(p *port.Pane) { p.Label = "" })
			r.passes()
			r.later(30 * time.Second)
			if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 {
				t.Fatalf("30s not identified: %d finished lines, briefs %+v; want none, one open", n, b)
			}
			r.later(31 * time.Second)
			r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
			if b := r.briefs(); len(b) != 0 {
				t.Errorf("61s not identified: briefs %+v, want none open", b)
			}
		})
	})
}

// A brief not worked on stays open when its agent stops (a brief to a stopped role agent starts it).
func TestBriefNotWorkedStaysOpenWhenItsAgentStops(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.start()
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify finding 3"}); err != nil {
			t.Fatal(err)
		}
		r.waitBriefDelivered()
		r.agentGone("w1:p6", "pj-rev", func(p *port.Pane) { p.Agent, p.Status = "", "" })
		r.later(2 * time.Minute)
		if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 || b[0].To != "pj-rev" {
			t.Errorf("%d finished lines, briefs %+v; want none, pj-rev's open", n, b)
		}
	})
}

// A usage-limit stop in force keeps the brief open though its agent exits.
func TestBriefUnderAStopStaysOpenWhenItsAgentIsGone(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := claudeRoleStop(t, true, "rate_limit", resetIn(time.Hour))
		r.agentGone("w1:p6", "pj-rev", func(p *port.Pane) { p.Agent, p.Status = "", "" })
		r.later(2 * time.Minute)
		if n, b := finishedLines(r), r.briefs(); n != 0 || len(b) != 1 {
			t.Errorf("%d finished lines, briefs %+v; want none, one open", n, b)
		}
	})
}

// A worked brief left open in the log (an earlier serve's) whose agent is gone ends on the first pass after replay.
func TestReplayedWorkedBriefOfAGoneAgentEnds(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, noCheck())
		r.addPane(port.Pane{ID: "w1:p5", Label: "pj-lead", Agent: "claude", Status: "idle", Cwd: r.p.Root})
		r.addPane(port.Pane{ID: "w1:p6", Label: "pj-rev", Cwd: r.p.Root}) // its agent stopped
		at := r.clock.Now().Add(-2 * time.Hour)
		brief, _ := json.Marshal(event.Message{To: "pj-rev", From: "pj-lead", Line: "[obor pj-lead] look at x"})
		if err := app.WriteLog(r.p.Root, at, event.Record{Task: event.MsgTask, Step: event.StepBriefed,
			Kind: event.KindFact, Data: brief}); err != nil {
			t.Fatal(err)
		}
		var seq int64
		recs, err := event.ReadLog(r.p.Root)
		if err != nil {
			t.Fatal(err)
		}
		for _, rec := range recs {
			if rec.Task == event.MsgTask && rec.Step == event.StepBriefed {
				seq = rec.Seq
			}
		}
		if seq == 0 {
			t.Fatal("no briefed fact in the log")
		}
		worked, _ := json.Marshal(map[string]any{"to": "pj-rev", "brief": seq})
		delivered, _ := json.Marshal(map[string]any{"to": "pj-rev"})
		if err := app.WriteLog(r.p.Root, at,
			event.Record{Task: event.MsgTask, Step: "deliver." + strconv.FormatInt(seq, 10), Kind: event.KindIntent, Data: delivered},
			event.Record{Task: event.MsgTask, Step: "deliver." + strconv.FormatInt(seq, 10), Kind: event.KindResult},
			event.Record{Task: event.MsgTask, Step: event.StepWorked, Kind: event.KindFact, Data: worked}); err != nil {
			t.Fatal(err)
		}
		r.start()
		r.waitFor("the finished line", func() bool { return finishedLines(r) == 1 })
		if b := r.briefs(); len(b) != 0 {
			t.Errorf("briefs %+v, want none open", b)
		}
		if n := r.typedTo("pj-rev", "[obor pj-lead] look at x"); n != 0 {
			t.Errorf("the delivered brief typed %d times, want none", n)
		}
	})
}

// A brief to a Claude or OpenCode role agent is posted to its session (none typed); to Codex it is typed (none posted);
// a Claude session with no inbox record gets it typed (as its job wakes are), nothing posted, nothing left waiting;
// its work and idle end it.
func TestBriefByKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind     string
		recorded bool
	}{
		{"claude", true},
		{"claude", false},
		{"codex", true},
		{"opencode", true},
	} {
		name := c.kind
		if !c.recorded {
			name += " without a record"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := lineRig(t, "idle", "idle")
				if !c.recorded {
					r.inbox.unrecord("pj-rev")
				}
				r.setPane("w1:p6", func(p *port.Pane) { p.Agent = c.kind })
				r.start()
				const line = "[obor pj-lead] verify"
				if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify"}); err != nil {
					t.Fatal(err)
				}
				r.waitFor("the brief delivered", func() bool {
					return r.inbox.count("pj-rev", line)+r.typedTo("pj-rev", line) == 1
				})
				r.passes()
				posted, typed := r.inbox.tries("pj-rev", line), r.typedTo("pj-rev", line)
				post := c.kind != "codex" && c.recorded
				if post && (posted != 1 || typed != 0) || !post && (posted != 0 || typed != 1) {
					t.Errorf("%s: posted %d, typed %d", name, posted, typed)
				}
				if c.kind == "claude" && !c.recorded {
					if w := r.waiting(); len(w) != 0 {
						t.Errorf("waiting %+v, want none: the fallback typed it", w)
					}
				}
				r.agentIs("w1:p6", "pj-rev", "working")
				r.agentIs("w1:p6", "pj-rev", "idle")
				r.waitFor("the finished line posted", func() bool {
					return r.inbox.count("pj-lead", "[obor] pj-rev finished (briefed 0s ago)") == 1
				})
			})
		})
	}
}

// obor brief types its line into the idle role pane (through Liner); once that pane has worked
// and is idle again, the lead gets "[obor] <agent> finished (briefed <age> ago)" posted once. Idle right after the
// typing is no finish.
func TestBriefFinishedLineToTheLead(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.start()
		reply, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "verify finding 3"})
		if err != nil || reply != "BRIEFED pj-rev (delivered when its pane is idle; the lead hears when it finishes)" {
			t.Fatalf("brief: %q, %v", reply, err)
		}
		r.waitFor("the brief posted", func() bool { return r.inbox.count("pj-rev", "[obor pj-lead] verify finding 3") == 1 })
		r.waitBriefDelivered()
		r.passes()
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact message"); n != 0 {
			t.Fatalf("a finished line before the reviewer worked: %q", r.steps())
		}
		r.agentIs("w1:p6", "pj-rev", "working")
		r.clock.Add(5 * time.Minute)
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact message"); n != 0 {
			t.Fatalf("a finished line while the reviewer works: %q", r.steps())
		}
		r.agentIs("w1:p6", "pj-rev", "idle")
		const done = "[obor] pj-rev finished (briefed 5m ago)"
		r.waitFor("the finished line posted", func() bool { return r.inbox.count("pj-lead", done) == 1 })
		r.agentIs("w1:p6", "pj-rev", "working")
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.passes()
		if n := r.taskStep(event.MsgTask, "fact message"); n != 1 || r.inbox.count("pj-lead", done) != 1 {
			t.Errorf("finished lines %d (posted %d), want one: the brief ended", n, r.inbox.count("pj-lead", done))
		}
		if typed := append(r.promptsTo("pj-rev"), r.promptsTo("pj-lead")...); len(typed) != 0 {
			t.Errorf("typed %q, want the brief and the finished line posted, none typed (T-499)", typed)
		}
	})
}

// A brief without a line (a review's start carried it) is recorded while the agent already works on it;
// idle → the lead's finished line.
func TestBriefOfAFreshStart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working")
		r.start()
		reply, err := r.line("b1", &wire.BriefReq{To: "pj-rev"})
		if err != nil || reply != "BRIEF RECORDED for pj-rev (the lead hears when it finishes)" {
			t.Fatalf("brief: %q, %v", reply, err)
		}
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.waitFor("the finished line posted", func() bool {
			return r.inbox.count("pj-lead", "[obor] pj-rev finished (briefed 0s ago)") == 1
		})
		for _, p := range r.prompts() {
			if p.to == "pj-rev" {
				t.Errorf("typed into pj-rev: %q (its start carried the brief)", p.text)
			}
		}
	})
}

// A brief to an agent serve identifies no pane of (no label, session or start proves it) is still queued, and the
// reply says so and how to start it; an identified pane's reply is the usual one. The lead's line needs no pane:
// posted to its session.
func TestLineToAnUnnamedPane(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, noCheck())
		r.addPane(port.Pane{ID: "w1:p5", Agent: "claude", Status: "idle", Cwd: r.p.Root}) // the lead's pane, unnamed
		r.addPane(port.Pane{ID: "w1:p6", Label: "pj-rev", Agent: "claude", Status: "working", Cwd: r.p.Root})
		r.start()
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if want := "SENT to pj-lead in proj (posted to its session)"; err != nil || reply != want {
			t.Errorf("send: %q, %v; want %q", reply, err, want)
		}
		reply, err = r.line("b1", &wire.BriefReq{To: "pj-sol", Text: "look at x"})
		if want := "BRIEFED pj-sol (queued: no pane runs pj-sol; `obor restart pj-sol` starts it)"; err != nil || reply != want {
			t.Errorf("brief: %q, %v; want %q", reply, err, want)
		}
		reply, err = r.line("b2", &wire.BriefReq{To: "pj-rev", Text: "verify"})
		if want := "BRIEFED pj-rev (delivered when its pane is idle; the lead hears when it finishes)"; err != nil || reply != want {
			t.Errorf("brief of a named pane: %q, %v; want %q", reply, err, want)
		}
		r.waitFor("the lead's line posted", func() bool { return r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 })
		r.passes()
		if w := r.waiting(); len(w) != 2 || w[0].To != "pj-sol" || w[1].To != "pj-rev" {
			t.Errorf("lines waiting %+v, want pj-sol's (unnamed) and pj-rev's (working) still queued", w)
		}
	})
}

// A role pane with a label but no agent name is found by its label: a brief to that agent has its line typed into the
// pane (by id: herdr has no name to address; a Codex pane, so typed), its work and idle end the brief, and the lead
// gets the finished line.
func TestBriefToALabelledPaneWithoutAName(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, noCheck())
		r.addPane(port.Pane{ID: "w1:p5", Label: "pj-lead", Agent: "claude", Status: "idle", Cwd: r.p.Root})
		r.addPane(port.Pane{ID: "w1:p6", Label: "pj-sol", Agent: "codex", Status: "idle", Cwd: r.p.Root})
		r.setName("w1:p6", "") // herdr holds no name for it: only the label proves it
		r.start()
		reply, err := r.line("b1", &wire.BriefReq{To: "pj-sol", Text: "look at x"})
		if err != nil || reply != "BRIEFED pj-sol (delivered when its pane is idle; the lead hears when it finishes)" {
			t.Fatalf("brief of the labelled pane: %q, %v; want the named pane's reply", reply, err)
		}
		r.waitFor("the brief typed into w1:p6", func() bool { return r.typedTo("w1:p6", "[obor pj-lead] look at x") == 1 })
		r.waitBriefDelivered()
		r.statusEvent("w1:p6", "working", "codex")
		r.waitFor("its work logged", func() bool { return r.taskStep(event.MsgTask, "fact worked") == 1 })
		r.clock.Add(2 * time.Minute)
		r.statusEvent("w1:p6", "idle", "codex")
		const done = "[obor] pj-sol finished (briefed 2m ago)"
		r.waitFor("the finished line posted to the lead", func() bool { return r.inbox.count("pj-lead", done) == 1 })
	})
}

// The lead's lines are never superseded: a send to the lead and another brief's finished line stay queued
// across a brief to a different role agent.
func TestBriefLeavesLeadLinesQueued(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working")
		r.addPane(port.Pane{ID: "w1:p7", Label: "pj-sol", Agent: "claude", Status: "working", Cwd: r.p.Root})
		r.inbox.unrecord("pj-lead") // its lines wait
		r.start()
		if _, err := r.line("s1", &wire.BriefReq{To: "pj-sol"}); err != nil { // a fresh start's brief: sol works
			t.Fatal(err)
		}
		r.agentIs("w1:p7", "pj-sol", "idle")
		const done = "[obor] pj-sol finished (briefed 0s ago)"
		r.waitFor("the finished line queued for the lead", func() bool {
			for _, m := range r.waiting() {
				if m.To == "pj-lead" && m.Line == done {
					return true
				}
			}
			return false
		})
		if _, err := r.line("x1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "first"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.line("b2", &wire.BriefReq{To: "pj-rev", Text: "second"}); err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, m := range r.waiting() {
			got[m.Line] = true
		}
		for _, line := range []string{done, sentLine("other", "hello"), "[obor pj-lead] second"} {
			if !got[line] {
				t.Errorf("waiting %+v lacks %q", r.waiting(), line)
			}
		}
		if got["[obor pj-lead] first"] {
			t.Errorf("the superseded brief still waits: %+v", r.waiting())
		}
		if len(got) != 3 {
			t.Errorf("waiting %d lines, want the two lead lines and the second brief", len(got))
		}
	})
}
