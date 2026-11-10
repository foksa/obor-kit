package app_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/herdr"
	"obor/internal/herdr/herdrtest"
)

// A worked brief ends with the usual finished line once its agent is gone: its pane at its shell or closed, or
// not identified for a minute; a brief not worked on, or under a usage-limit stop, stays open.

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
			r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
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
			r.f.Remove("w1:p6")
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
			r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Name, p.Label = "", "" })
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
		r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
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
		r.agentGone("w1:p6", "pj-rev", func(p *herdrtest.Pane) { p.Kind, p.Name, p.Status = "", "", "" })
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
		r.f.Agent("w1:p5", "pj-lead", "idle", r.p.Root)
		r.f.Shell("w1:p6", "pj-rev", r.p.Root) // its agent stopped
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

// retryWake is the fetch rig with T-1 dispatched to pj-l-01, running as kind with an inbox record or not, working and
// undelivered for 5 minutes: its retry.work wake goes out.
func retryWake(t *testing.T, kind string, recorded bool) *loopRig {
	t.Helper()
	r := fetchRig(t)
	if recorded {
		r.inbox.record("pj-l-01")
	}
	r.start()
	r.mustDispatch("d1")
	r.waitStep("1 result work")
	r.f.Set("w1:p1", func(p *herdrtest.Pane) { p.Kind = kind })
	r.workerIs("working")
	r.waitFor("pj-l-01 seen as "+kind, func() bool {
		for _, a := range r.app.Status().Project.Agents {
			if a.Name == "pj-l-01" && a.Agent == kind {
				return true
			}
		}
		r.push()
		return false
	})
	if len(r.promptsTo("pj-l-01")) != 0 || r.inbox.tries("pj-l-01", wake("T-1/1/work")) != 0 {
		t.Fatalf("precondition: the work job's wake went with the start, nothing sent since: %q", r.steps())
	}
	r.later(5 * time.Minute)
	return r
}

// A worker's retry wake: posted to a Claude session with an inbox record (none typed, no keys); typed to a Claude
// session with none (the fallback), and to Codex and OpenCode whatever an inbox record says.
func TestWakeByKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		kind     string
		recorded bool
		posted   bool
	}{
		{"claude", true, true},
		{"claude", false, false},
		{"codex", true, false},
		{"opencode", true, false},
	} {
		name := c.kind
		if !c.recorded {
			name += " without a record"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bubble(t, func(t *testing.T) {
				r := retryWake(t, c.kind, c.recorded)
				r.waitStep("1 result retry.work")
				same(t, r.result("retry.work"), `"sent"`, "retry.work result")
				w := wake("T-1/1/work")
				if c.posted {
					if n := r.inbox.count("pj-l-01", w); n != 1 {
						t.Errorf("posted %d wakes to pj-l-01, want 1", n)
					}
					eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
					if n := r.keysTo("pj-l-01"); n != 0 {
						t.Errorf("%d send_keys to pj-l-01, want none (no ctrl+u for a post)", n)
					}
					return
				}
				eq(t, r.promptsTo("pj-l-01"), []string{w}, "typed to pj-l-01")
				if n := r.inbox.count("pj-l-01", w); n != 0 {
					t.Errorf("posted %d wakes to pj-l-01, want none", n)
				}
			})
		})
	}
}

// A posted wake refused before a byte went out (a dead socket's dial) is a counted refusal, posted again on a later
// pass under the same key and only once; after a restart (replay) it is not posted again. The refusal is the next post
// alone (failOnce): how many passes run before the test looks is the loop's business, not the test's.
//
// The test waits on the refusal's own record, not on the retry's result: a pass whose herdr stream ends before
// the refused effect returns records no refusal at all (failed: not connected, so no fact), and the post of the pass
// after it goes out uncounted. The rig's pushes end serve's stream (workerIs pushes until it sees the status), so the
// test first waits for serve's stream after its last push: the pass that refuses runs on one stream, and so does the
// pass that posts.
func TestWakePostRefusedThenReplayed(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.inbox.record("pj-l-01")
		r.inbox.failOnce(false)
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.workerIs("working")
		r.connectedAgain() // no push after this: the pass that refuses the post runs on one stream to its end
		r.later(5 * time.Minute)
		w := wake("T-1/1/work")
		const refused = "1 fact failed{retry.work: "
		r.waitFor("the refused wake counted", func() bool { return r.countPrefix(refused) == 1 })
		if n := r.countPrefix(refused); n != 1 {
			t.Fatalf("refused posts %d, want 1: %q", n, r.steps())
		}
		r.waitStep("1 result retry.work") // the pass after the refusal posts it: wait on the record, not on the test's look
		if tried, posted := r.inbox.tries("pj-l-01", w), r.inbox.count("pj-l-01", w); tried != 2 || posted != 1 {
			t.Fatalf("tried %d, posted %d wakes, want 2 and 1 (one refusal, then the post)", tried, posted)
		}
		if r.count("1 intent retry.work") != 1 {
			t.Errorf("the post's retry wrote a second intent: %q", r.steps())
		}
		r.stop()
		r.start()
		r.passes()
		if n := r.inbox.count("pj-l-01", w); n != 1 {
			t.Errorf("posted %d wakes, want 1 (once after the refusal, not again on replay)", n)
		}
		eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
	})
}

// A posted wake that broke after a byte went out is sent (with the error): never posted again under its key, not after
// a restart either.
func TestWakePostPartialNotRepeated(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.inbox.record("pj-l-01")
		r.inbox.fail(true)
		r.start()
		r.mustDispatch("d1")
		r.waitStep("1 result work")
		r.workerIs("working")
		r.later(5 * time.Minute)
		r.waitStep("1 result retry.work")
		same(t, r.result("retry.work"), `"sent: broken pipe"`, "retry.work result")
		r.inbox.ok()
		r.stop()
		r.start()
		r.passes()
		if n := r.inbox.tries("pj-l-01", wake("T-1/1/work")); n != 1 {
			t.Errorf("tried %d posts of the wake, want 1", n)
		}
		eq(t, r.promptsTo("pj-l-01"), []string(nil), "typed to pj-l-01")
	})
}

// A brief to a Claude role agent is posted to its session (none typed); to Codex and OpenCode it is typed (none
// posted); a Claude session with no inbox record gets it typed (as its job wakes are), nothing posted, nothing left
// waiting; its work and idle end it.
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
				r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Kind = c.kind })
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
				post := c.kind == "claude" && c.recorded
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

// obor send's line is posted to the lead's Claude session at once, working or idle (no pane, no idle rule, no
// quiet period), exactly once, as "[obor <sender>] <text> (reply: …)": nothing typed into the lead's pane, no keys; its
// delivery result is sent; a repeated call id gets the stored reply and records nothing; a worker cannot send; text and
// sender are required.
func TestSendPostedOnceToTheLead(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "working", "idle")
		r.start()
		for _, c := range []struct {
			call wire.SendReq
			want string
		}{
			{wire.SendReq{Envelope: wire.Envelope{Role: "worker", Agent: "pj-l-01"}, Text: "x", From: "other"},
				"a lead or the owner"},
			{wire.SendReq{From: "other"}, "a text is required"},
			{wire.SendReq{Text: "x"}, "the sender project is required"},
		} {
			if reply, err := r.line("bad", &c.call); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("send %+v: %q, %v; want refused: %s", c.call, reply, err, c.want)
			}
		}
		reply, err := r.line("s1", &wire.SendReq{Text: "release v0.12 is out", From: "other"})
		if err != nil || reply != "SENT to pj-lead in proj (posted to its session)" {
			t.Fatalf("send: %q, %v", reply, err)
		}
		if reply, err := r.line("s1", &wire.SendReq{Text: "again", From: "other"}); err != nil || !strings.HasPrefix(reply,
			"SENT") {
			t.Errorf("the same call id: %q, %v; want the stored reply", reply, err)
		}
		if n := r.taskStep(event.MsgTask, "fact message"); n != 1 {
			t.Fatalf("message facts %d, want 1: %q", n, r.steps())
		}
		line := sentLine("other", "release v0.12 is out")
		r.waitFor("the line posted to the working lead", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		r.waitFor("its delivery result", func() bool { return len(r.waiting()) == 0 })
		r.agentIs("w1:p5", "pj-lead", "idle")
		r.passes()
		r.passes()
		if n := r.inbox.count("pj-lead", line); n != 1 {
			t.Errorf("posted %d times, want once", n)
		}
		if got := r.lineResults(); len(got) != 1 || got[0] != "sent" {
			t.Errorf("delivery results %q, want one sent", got)
		}
		if n := r.typedTo("pj-lead", line); n != 0 {
			t.Errorf("typed %d times into the lead's pane, want 0", n)
		}
		for _, req := range r.srv.Requests() {
			if req.Method == "agent.send_keys" {
				t.Errorf("keys sent: %s", req.Params)
			}
		}
	})
}

// With no inbox record for the lead, its line stays queued (obor status lists it, nothing is tried) and the
// reply says how the lead registers; across a restart it stays queued; once the record exists it is posted once, and
// a later restart posts it no more.
func TestSendQueuedUntilTheLeadIsRecorded(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.inbox.unrecord("pj-lead")
		r.start()
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if want := "SENT to pj-lead in proj (queued: no session recorded for pj-lead; `obor restart pj-lead` once it is idle registers it, or quit it and run `obor lead`)"; err != nil || reply != want {
			t.Fatalf("send: %q, %v; want %q", reply, err, want)
		}
		r.passes()
		r.stop()
		r.start()
		r.passes()
		if w := r.waiting(); len(w) != 1 || w[0].Line != sentLine("other", "hello") || r.inbox.count("pj-lead", sentLine("other", "hello")) != 0 {
			t.Fatalf("after a restart with no record: waiting %+v, posted %d; want queued, untried", w,
				r.inbox.count("pj-lead", sentLine("other", "hello")))
		}
		if n := r.taskStep(event.MsgTask, "intent deliver.1") + r.typedTo("pj-lead", sentLine("other", "hello")); n != 0 {
			t.Fatalf("tried with no record: %q", r.steps())
		}
		r.inbox.record("pj-lead")
		r.waitFor("the line posted once recorded", func() bool { return r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 })
		r.waitFor("no line waits", func() bool { return len(r.waiting()) == 0 })
		r.stop()
		r.start()
		r.passes()
		if n := r.inbox.count("pj-lead", sentLine("other", "hello")); n != 1 {
			t.Errorf("posted %d times across restarts, want once", n)
		}
	})
}

// A line waiting longer than 24 h (here: the lead has no inbox record) is the project's needs-you (a
// notification, obor status's needs-you with no command to answer it); posted at last, it ends (resumed).
func TestSendWaitingADayNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.inbox.unrecord("pj-lead")
		r.start()
		if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.later(23 * time.Hour)
		want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
			" (undelivered: no inbox recorded for pj-lead: its SessionStart hook records one)}" // why it waits
		if n := r.taskStep(event.MsgTask, want); n != 0 {
			t.Fatalf("a needs-you before 24 h: %q", r.steps())
		}
		r.later(2 * time.Hour)
		r.waitFor("the needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
		facts := r.msgFacts()
		if len(facts) != 1 || facts[0].Step != event.StepNeedsYou || r.answerOf(facts[0]) != "" {
			t.Errorf("facts %+v, want the line's needs-you", facts)
		}
		r.passes()
		if n := r.taskStep(event.MsgTask, want); n != 1 {
			t.Errorf("needs-you recorded %d times, want once", n)
		}
		r.inbox.record("pj-lead")
		// The records the assertions below read: the line posted, the resumed fact in the log and folded out of the facts
		// in force (the log and the status fold are not updated in one step).
		r.waitFor("resumed", func() bool {
			return r.taskStep(event.MsgTask, "fact resumed") == 1 && r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 &&
				len(r.msgFacts()) == 0
		})
		if r.inbox.count("pj-lead", sentLine("other", "hello")) != 1 || len(r.msgFacts()) != 0 {
			t.Errorf("posted %d, facts %+v; want posted once, none in force", r.inbox.count("pj-lead", sentLine("other", "hello")),
				r.msgFacts())
		}
	})
}

// obor brief types its line into the idle role pane (through Liner); once that pane has worked
// and is idle again, the lead gets "[obor] <agent> finished (briefed <age> ago)" posted once. Idle right after the
// typing is no finish; the lead itself cannot be briefed.
func TestBriefFinishedLineToTheLead(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.start()
		if _, err := r.line("b0", &wire.BriefReq{To: "pj-lead", Text: "x"}); err == nil || !strings.Contains(err.Error(),
			"is the lead") {
			t.Errorf("a brief of the lead: %v, want refused", err)
		}
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
		r.waitFor("the finished line recorded", func() bool { return r.taskStep(event.MsgTask, "fact message") == 1 })
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
		r.f.Agent("w1:p5", "", "idle", r.p.Root) // the lead's pane, unnamed
		rev := r.f.Agent("w1:p6", "pj-rev", "working", r.p.Root)
		rev.Label = "pj-rev"
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
		r.f.Agent("w1:p5", "pj-lead", "idle", r.p.Root)
		sol := r.f.Agent("w1:p6", "", "idle", r.p.Root)
		sol.Label, sol.Kind = "pj-sol", "codex"
		r.start()
		reply, err := r.line("b1", &wire.BriefReq{To: "pj-sol", Text: "look at x"})
		if err != nil || reply != "BRIEFED pj-sol (delivered when its pane is idle; the lead hears when it finishes)" {
			t.Fatalf("brief of the labelled pane: %q, %v; want the named pane's reply", reply, err)
		}
		r.waitFor("the brief typed into w1:p6", func() bool { return r.typedTo("w1:p6", "[obor pj-lead] look at x") == 1 })
		r.waitBriefDelivered()
		r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Status = "working" })
		r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p6", "agent_status": "working", "agent": "codex"})
		r.waitFor("its work logged", func() bool { return r.taskStep(event.MsgTask, "fact worked") == 1 })
		r.clock.Add(2 * time.Minute)
		r.f.Set("w1:p6", func(p *herdrtest.Pane) { p.Status = "idle" })
		r.srv.Push(herdr.EventStatus, map[string]any{"pane_id": "w1:p6", "agent_status": "idle", "agent": "codex"})
		const done = "[obor] pj-sol finished (briefed 2m ago)"
		r.waitFor("the finished line posted to the lead", func() bool { return r.inbox.count("pj-lead", done) == 1 })
	})
}

// A role agent with no inbox: brief A waits, then a fresh start's brief B (no line: the start carried it) ends
// A; the inbox's arrival delivers nothing for A, before or after a restart of serve.
func TestBriefSupersedesAFreshStartsOldLine(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working")
		r.inbox.unrecord("pj-rev")
		r.start()
		const a = "[obor pj-lead] first"
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "first"}); err != nil {
			t.Fatal(err)
		}
		if w := r.waiting(); len(w) != 1 || w[0].Line != a {
			t.Fatalf("waiting %+v, want the first brief's line alone", w)
		}
		reply, err := r.line("b2", &wire.BriefReq{To: "pj-rev"})
		if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
			t.Fatalf("the fresh start's brief: %q, %v; want it to say it replaces the waiting one", reply, err)
		}
		if w := r.waiting(); len(w) != 0 {
			t.Errorf("waiting %+v after the fresh start's brief, want none: the old line is superseded", w)
		}
		sup := r.supersededLines()
		if len(sup) != 1 || sup[0].By == 0 || sup[0].Seq >= sup[0].By {
			t.Fatalf("superseded lines %+v, want the old line replaced by the fresh start's brief", sup)
		}
		var by event.Message
		for _, rec := range r.records() {
			if rec.Seq == sup[0].By {
				_ = json.Unmarshal(rec.Data, &by)
			}
		}
		if by.Line != "" {
			t.Errorf("the newer brief carried line %q, want none (its start carried it)", by.Line)
		}
		// The inbox appears: the old line is not delivered, before or after a restart.
		r.inbox.record("pj-rev")
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.passes()
		r.stop()
		r.start()
		r.passes()
		if n := r.inbox.count("pj-rev", a) + r.typedTo("pj-rev", a); n != 0 {
			t.Errorf("the superseded line was delivered %d times, want none", n)
		}
		if n := len(r.supersededLines()); n != 1 {
			t.Errorf("superseded lines after a restart: %d, want the one", n)
		}
	})
}

// Brief A waits; obor brief B ends it and is delivered once, and A is never delivered.
func TestBriefSupersedesAQueuedBrief(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working") // the reviewer is busy: brief A waits
		r.start()
		if _, err := r.line("b1", &wire.BriefReq{To: "pj-rev", Text: "first"}); err != nil {
			t.Fatal(err)
		}
		const a = "[obor pj-lead] first"
		r.waitFor("the first brief queued", func() bool { return len(r.waiting()) == 1 })
		reply, err := r.line("b2", &wire.BriefReq{To: "pj-rev", Text: "second"})
		if err != nil || !strings.HasSuffix(reply, "(replaces 1 undelivered brief)") {
			t.Fatalf("the second brief: %q, %v", reply, err)
		}
		const b = "[obor pj-lead] second"
		if w := r.waiting(); len(w) != 1 || w[0].Line != b {
			t.Fatalf("waiting %+v, want the second brief's line alone", w)
		}
		r.agentIs("w1:p6", "pj-rev", "idle")
		r.waitFor("the second brief delivered", func() bool { return r.inbox.count("pj-rev", b) == 1 })
		r.passes()
		if n := r.inbox.count("pj-rev", a) + r.typedTo("pj-rev", a); n != 0 {
			t.Errorf("the superseded brief was delivered %d times, want none", n)
		}
		if n := r.inbox.count("pj-rev", b) + r.typedTo("pj-rev", b); n != 1 {
			t.Errorf("the second brief delivered %d times, want once", n)
		}
	})
}

// The lead's lines are never superseded: a send to the lead and another brief's finished line stay queued
// across a brief to a different role agent.
func TestBriefLeavesLeadLinesQueued(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "working")
		sol := r.f.Agent("w1:p7", "pj-sol", "working", r.p.Root)
		sol.Label = "pj-sol"
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

// DD-v0.12 A3: obor send and obor brief to a project without a merge loop (no integrator role) are refused as obor
// dispatch is: nothing there would ever deliver the line.
func TestLinesRefusedWithoutAMergeLoop(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t, noCheck())
		r.setConfig("agents.roles", map[string]any{"reviewer": ""})
		r.start()
		if st := r.app.Status().Project; st.Merging {
			t.Fatalf("precondition: %s merges", st.Name)
		}
		for _, c := range []wire.Req{&wire.SendReq{Text: "hello", From: "other"}, &wire.BriefReq{To: "pj-rev",
			Text: "verify"}} {
			reply, err := r.line(c.Method()+"1", c)
			if err == nil || err.Error() != "merge loop off for proj (no integrator role)" || reply != "" {
				t.Errorf("%s: %q, %v; want the merge loop's refusal", c.Method(), reply, err)
			}
		}
		if n := len(r.waiting()); n != 0 || r.taskStep(event.MsgTask, "fact message")+r.taskStep(event.MsgTask, "fact briefed") != 0 {
			t.Errorf("recorded while refused: %d lines, %q", n, r.steps())
		}
	})
}
