package app_test

import (
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

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
				"the lead or the owner sends"},
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
		if n := r.calls(opKeys); n != 0 {
			t.Errorf("keys sent: %d calls", n)
		}
	})
}

// With no inbox record for the lead, its line stays queued for its hooks (obor status lists it, nothing is posted) and
// the reply says it is queued (the hint is TestRunningLeadHint's); across a restart it stays queued; once the record exists it is posted once, and
// a later restart posts it no more.
func TestSendQueuedUntilTheLeadIsRecorded(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.inbox.unrecord("pj-lead")
		r.start()
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if want := "SENT to pj-lead in proj (queued: " + noLiveWhy + "; " + byHooksWhy + ";"; err != nil || !strings.HasPrefix(reply, want) {
			t.Fatalf("send: %q, %v; want it to start %q", reply, err, want)
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
