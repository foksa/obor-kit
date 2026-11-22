package app_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentkinds "obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/testkit"
)

func init() { agentkinds.Register(testkit.HookKind{}) } // a lead of it takes its lines through its hooks

// byHooksWhy is how a lead's held lines reach it.
const byHooksWhy = "its hooks hand it its lines at its next prompt or turn end"

// hookHeldWhy is why a line to the rig's hook lead waits (obor status, its needs-you).
const hookHeldWhy = "pj-lead's kind has no inbox: " + byHooksWhy

// noLiveWhy is why an inbox kind's lead has no live record: none recorded, its socket gone, or a send refused.
const noLiveWhy = "pj-lead's inbox is not live: a refused send, its socket gone, or none recorded"

// noRecordWhy is why a line to an inbox kind's lead waits with no live record (obor status, its needs-you).
const noRecordWhy = noLiveWhy + "; " + byHooksWhy + "; a newer record clears it"

// leadKindRig is the loop rig (opts added) with the lead pj-lead an agent of the kind of model typ (its pane's agent
// kind), idle in w1:p5, no inbox recorded.
func leadKindRig(t *testing.T, typ, kind string, opts ...rigOpt) *loopRig {
	t.Helper()
	cfg := withConfig(testkit.HookModel(), testkit.Set("agents.roles.lead", typ))
	r := newLoopRig(t, append([]rigOpt{noCheck(), cfg}, opts...)...)
	lead := agentPane("w1:p5", "pj-lead", r.p.Root)
	lead.Agent = kind
	r.addPane(lead)
	r.inbox.unrecord("pj-lead")
	return r
}

// hookLeadRig is leadKindRig with the lead a testkit.HookKind agent (no inbox, its hooks act on the answers).
func hookLeadRig(t *testing.T, opts ...rigOpt) *loopRig {
	t.Helper()
	return leadKindRig(t, "hooky", testkit.HookKindName, opts...)
}

// leadLines is the lead's hook asking for its lines under callID.
func (r *loopRig) leadLines(callID string) (string, error) {
	return r.line(callID, &wire.LeadLinesReq{})
}

// hookIntents is the delivery intents of the project's lines that went to the lead's hook (via hook): their members.
func (r *loopRig) hookIntents() [][]int64 {
	var out [][]int64
	for _, rec := range r.records() {
		if rec.Task == event.MsgTask && rec.Kind == event.KindIntent && strings.HasPrefix(rec.Step, outbox.StepDeliver) {
			var d outbox.LineData
			if json.Unmarshal(rec.Data, &d) == nil && d.Via == outbox.ViaHook {
				out = append(out, d.Lines)
			}
		}
	}
	return out
}

// A line to a lead whose kind has no inbox waits for its hooks (the reply and obor status say so; nothing is posted or
// typed); the hook's ask hands it once, recorded as its delivery (intent via hook, result sent), and it leaves obor
// status. A repeated call id gets the same lines, a new one none; later passes and a restart neither post, type nor
// hand it again.
func TestLeadLinesHandedByTheHookOnce(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := hookLeadRig(t)
		r.start()
		line := sentLine("other", "hello")
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if want := "SENT to pj-lead in proj (queued: " + hookHeldWhy + ")"; err != nil || reply != want {
			t.Fatalf("send: %q, %v; want %q", reply, err, want)
		}
		r.passes()
		if w := r.waiting(); len(w) != 1 || w[0].Line != line || w[0].Held != hookHeldWhy {
			t.Fatalf("waiting %+v; want the line held for the lead's hooks", w)
		}
		if n := r.inbox.tries("pj-lead", line) + r.typedTo("pj-lead", line); n != 0 {
			t.Fatalf("posted or typed %d times before the hook asked", n)
		}
		got, err := r.leadLines("h1")
		if err != nil || got != line {
			t.Fatalf("the hook's ask: %q, %v; want %q", got, err, line)
		}
		if w := r.waiting(); len(w) != 0 {
			t.Errorf("waiting %+v after the hook took it", w)
		}
		if res, in := r.lineResults(), r.hookIntents(); len(res) != 1 || res[0] != "sent" || len(in) != 1 || len(in[0]) != 1 {
			t.Errorf("delivery results %q, hook intents %v; want one sent of one line via hook", res, in)
		}
		if again, err := r.leadLines("h1"); err != nil || again != line {
			t.Errorf("the same call id: %q, %v; want the stored lines", again, err)
		}
		if next, err := r.leadLines("h2"); err != nil || next != "" {
			t.Errorf("a new ask: %q, %v; want none", next, err)
		}
		r.passes()
		r.stop()
		r.start()
		r.passes()
		if next, err := r.leadLines("h3"); err != nil || next != "" {
			t.Errorf("after a restart: %q, %v; want none", next, err)
		}
		if w, res := r.waiting(), r.lineResults(); len(w) != 0 || len(res) != 1 {
			t.Errorf("after a restart: waiting %+v, results %q; want none waiting, one delivery", w, res)
		}
		if n := r.inbox.tries("pj-lead", line) + r.typedTo("pj-lead", line); n != 0 {
			t.Errorf("posted or typed %d times besides the hook's", n)
		}
	})
}

// Lines queued for the lead go to its hook as one, oldest first, one per row: the intent names both, each gets its
// result.
func TestLeadLinesHandedAsOne(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := hookLeadRig(t)
		r.start()
		for i, text := range []string{"first", "second"} {
			if _, err := r.line("s"+string(rune('1'+i)), &wire.SendReq{Text: text, From: "other"}); err != nil {
				t.Fatal(err)
			}
		}
		if w := r.waiting(); len(w) != 2 {
			t.Fatalf("precondition: two lines waiting, %+v", w)
		}
		got, err := r.leadLines("h1")
		if want := sentLine("other", "first") + "\n" + sentLine("other", "second"); err != nil || got != want {
			t.Fatalf("the hook's ask: %q, %v; want %q", got, err, want)
		}
		if res, in := r.lineResults(), r.hookIntents(); len(res) != 2 || len(in) != 1 || len(in[0]) != 2 || len(r.waiting()) != 0 {
			t.Errorf("results %q, hook intents %v, waiting %+v; want one intent of both, two results, none waiting", res,
				in, r.waiting())
		}
	})
}

// A line to the hook lead waiting over 24 h is the project's needs-you, naming why it waits; handed to the hook,
// the needs-you ends.
func TestLeadLineHookDeliveryEndsItsNeedsYou(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := hookLeadRig(t)
		r.start()
		if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.later(25 * time.Hour)
		want := "fact needs-you{line to pj-lead from other waiting over 1d for its delivery: " + sentLine("other", "hello") +
			" (undelivered: " + hookHeldWhy + ")}"
		r.waitFor("the needs-you", func() bool { return r.taskStep(event.MsgTask, want) == 1 })
		if got, err := r.leadLines("h1"); err != nil || got != sentLine("other", "hello") {
			t.Fatalf("the hook's ask: %q, %v", got, err)
		}
		r.waitFor("resumed", func() bool { return r.taskStep(event.MsgTask, "fact resumed") == 1 && len(r.msgFacts()) == 0 })
	})
}

// A needs-you line handed to the hook lead is delivered then: its repeat comes 30 min after that hand-over, not after
// the needs-you.
func TestNeedsLineRepeatCountsFromTheHookDelivery(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := hookLeadRig(t, dispatching())
		r.disp.set(func(d *dispatchFakes) {
			d.fail["prepare"] = func() error { return port.Refused{Line: "lane_setup in L-01: rc 1: boom"} }
		})
		r.start()
		r.mustDispatch("d1")
		r.waitFor("the needs line queued", func() bool { return len(r.needsLineFacts()) == 1 && len(r.waiting()) == 1 })
		var seq int64
		for _, f := range r.app.Status().Project.Facts {
			if f.Task == "T-1" && f.Step == event.StepNeedsYou {
				seq = f.Seq
			}
		}
		if seq == 0 || r.waiting()[0].Held != hookHeldWhy {
			t.Fatalf("precondition: T-1's needs-you (seq %d), its line held for the hooks: %+v", seq, r.waiting())
		}
		r.advance(20 * time.Minute)
		if got, err := r.leadLines("h1"); err != nil || !strings.HasPrefix(got, "[obor] needs-you T-1 (L-01): ") {
			t.Fatalf("the hook's ask: %q, %v; want the needs line", got, err)
		}
		r.advance(11 * time.Minute) // 31 min after the needs-you, 11 after the hand-over
		if a := r.againLines(seq); len(a) != 0 {
			t.Fatalf("a repeat 11 min after the hand-over: %q", a)
		}
		r.advance(20 * time.Minute)
		r.waitFor("the repeat 31 min after the hand-over", func() bool { return len(r.againLines(seq)) == 1 })
	})
}

// A lead whose kind has an inbox (Claude) and whose inbox is live gets its lines posted there: the hook's ask is
// refused and hands nothing;
// so is an ask from another agent or with no call id.
func TestLeadLinesRefusedForAnInboxLead(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := lineRig(t, "idle", "idle")
		r.start()
		if _, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"}); err != nil {
			t.Fatal(err)
		}
		r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", sentLine("other", "hello")) == 1 })
		got, err := r.leadLines("h1")
		if err == nil || got != "" || !strings.Contains(err.Error(), "lead_lines: pj-lead takes no lines through its hooks "+
			"(its inbox is live, or its hooks do not act on the answers)") {
			t.Errorf("a Claude lead's ask: %q, %v; want refused", got, err)
		}
		_, err = r.line("h2", &wire.LeadLinesReq{Envelope: wire.Envelope{Agent: "pj-l-01", Role: "worker"}})
		if err == nil || !strings.Contains(err.Error(), "the lead's hooks ask for its lines, not pj-l-01 (role worker)") {
			t.Errorf("a worker's ask: %v; want refused", err)
		}
		if _, err := r.ask(&wire.LeadLinesReq{Envelope: wire.Envelope{Root: r.p.Root, Agent: "pj-lead", Role: "lead"}}); err == nil ||
			!strings.Contains(err.Error(), "a call id is required") {
			t.Errorf("no call id: %v; want refused", err)
		}
		if res := r.lineResults(); len(res) != 1 {
			t.Errorf("delivery results %q, want the post's alone", res)
		}
	})
}

// A lead whose kind has no inbox and whose hooks do not act on the answers (Codex: none at all) keeps
// its lines waiting as a lead with no inbox record does: not held for the hooks, the send's reply and obor status
// say so, the hook's ask is refused and hands nothing, nothing is recorded as delivered.
func TestLeadLinesWaitWithoutHookAnswers(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := leadKindRig(t, "terra", "codex")
		r.start()
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if want := "SENT to pj-lead in proj (queued: no session recorded for pj-lead;"; err != nil || !strings.HasPrefix(reply, want) {
			t.Fatalf("send: %q, %v; want it to start %q", reply, err, want)
		}
		r.passes()
		why := "no inbox recorded for pj-lead: its SessionStart hook records one"
		if w := r.waiting(); len(w) != 1 || w[0].Held != why {
			t.Fatalf("waiting %+v; want the line held for its inbox record, not for the hooks", w)
		}
		if got, err := r.leadLines("h1"); err == nil || got != "" || !strings.Contains(err.Error(), "takes no lines through its hooks") {
			t.Errorf("the hook's ask: %q, %v; want refused", got, err)
		}
		r.passes()
		if w, res, in := r.waiting(), r.lineResults(), r.hookIntents(); len(w) != 1 || len(res) != 0 || len(in) != 0 {
			t.Errorf("waiting %+v, results %q, hook intents %v; want the line still queued, no delivery", w, res, in)
		}
	})
}

// An OpenCode lead (an inbox and hooks that act on the answers) with a live inbox gets its line posted there, once;
// the hook's ask is refused meanwhile, and once its socket is gone (the record not live) the ask hands nothing: a line
// delivered by inbox is never handed again by the hooks.
func TestLeadLineByInboxNotHandedByHooks(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := leadKindRig(t, "deepseek", "opencode")
		r.inbox.record("pj-lead")
		r.start()
		line := sentLine("other", "hello")
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if err != nil || reply != "SENT to pj-lead in proj (posted to its session)" {
			t.Fatalf("send: %q, %v; want it posted", reply, err)
		}
		r.waitFor("the line posted", func() bool { return r.inbox.count("pj-lead", line) == 1 })
		if _, err := r.leadLines("h1"); err == nil || !strings.Contains(err.Error(), "its inbox is live") {
			t.Errorf("the hook's ask with a live inbox: %v; want refused", err)
		}
		r.inbox.unrecord("pj-lead") // the session ended: its socket is gone
		if got, err := r.leadLines("h2"); err != nil || got != "" {
			t.Errorf("the hook's ask after the post: %q, %v; want none (delivered already)", got, err)
		}
		r.passes()
		if res, in := r.lineResults(), r.hookIntents(); len(res) != 1 || res[0] != "sent" || len(in) != 0 {
			t.Errorf("delivery results %q, hook intents %v; want the post's alone", res, in)
		}
	})
}

// An agy lead (an inbox its kind sends with, hooks that act on the answers) whose send is refused (a stale token: the
// poster marks its record refused, so it is not live) gets that line through its hooks, once: the refusal is recorded
// as a failed delivery, the line held for the hooks and handed at the hook's ask (the refused post's delivery, its
// result sent); once a command of the lead records its inbox again, later passes post nothing more.
func TestLeadLineRefusedSendGoesByHooks(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := leadKindRig(t, "gem", "agy",
			withConfig(testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-3-flash"})))
		r.inbox.record("pj-lead")
		r.inbox.staleOnRefuse()
		r.start()
		line := sentLine("other", "hello")
		reply, err := r.line("s1", &wire.SendReq{Text: "hello", From: "other"})
		if err != nil || reply != "SENT to pj-lead in proj (posted to its session)" {
			t.Fatalf("send: %q, %v; want it posted (a live inbox)", reply, err)
		}
		r.waitFor("the send refused", func() bool { return r.msgFailed() == 1 })
		r.passes()
		why := noRecordWhy
		if w := r.waiting(); len(w) != 1 || w[0].Line != line || w[0].Held != why {
			t.Fatalf("waiting %+v; want the line held for the lead's hooks", w)
		}
		if got, err := r.leadLines("h1"); err != nil || got != line {
			t.Fatalf("the hook's ask: %q, %v; want %q", got, err, line)
		}
		r.inbox.record("pj-lead") // the lead's next obor command records its inbox again
		r.passes()
		if res := r.lineResults(); len(res) != 1 || res[0] != "sent" { // the hook's, closing the refused post's intent
			t.Errorf("delivery results %q; want the hook's delivery alone", res)
		}
		if tries, posts := r.inbox.tries("pj-lead", line), r.inbox.count("pj-lead", line); tries != 1 || posts != 0 {
			t.Errorf("tried %d, posted %d; want the one refused try", tries, posts)
		}
		if w := r.waiting(); len(w) != 0 {
			t.Errorf("waiting %+v after the hook took it", w)
		}
	})
}
