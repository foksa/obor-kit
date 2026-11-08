package outbox

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

var t0 = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// rec is record seq of the project's lines, its time t0 plus seq seconds.
func rec(seq int64, step string, kind string, data any) event.Record {
	raw, _ := json.Marshal(data)
	return event.Record{Seq: seq, TS: t0.Add(time.Duration(seq) * time.Second).Format(time.RFC3339), Task: event.MsgTask,
		Step: step, Kind: kind, Data: raw}
}

func fact(seq int64, step string, data any) event.Record { return rec(seq, step, event.KindFact, data) }

// deliver is line seq's delivery record of kind (an intent's data, or a result's).
func deliver(seq int64, kind string, data any) event.Record {
	return rec(0, StepDeliver+strconv.FormatInt(seq, 10), kind, data) // the key names the line; its time is t0
}

// failed is fact failed for line seq's delivery, refused before a byte (via: ViaInbox for a post).
func failed(seq int64, via string) event.Record {
	step := StepDeliver + strconv.FormatInt(seq, 10)
	return fact(0, event.StepFailed, event.FactData{Reason: step + ": refused", Step: step, Via: via})
}

// noOld is a test's oldOf: a pre-T-927 message has no did key here.
func noOld(json.RawMessage) event.Of { return event.Of{} }

// lines is o's queued lines as seq → state.
func lines(o *Outbox) map[int64]State {
	out := map[int64]State{}
	for _, q := range o.Queued() {
		out[q.Seq()] = q.State()
	}
	return out
}

// briefs is o's open briefs as agent → brief seq.
func briefs(o *Outbox) map[string]int64 {
	out := map[string]int64{}
	for agent, b := range o.Briefs() {
		out[agent] = b.Seq()
	}
	return out
}

// TestFold: records in, the outbox's state and what each record ended out (Fold alone, no app): a newer brief's
// supersede (T-575/T-580), a lead batch's post and its outcomes (T-654), an answer's retire (T-746/T-748), a role
// agent's stop, alarm and turn (T-569/T-571), a brief's end (T-868) and a needs-you line's delivery (T-624).
func TestFold(t *testing.T) {
	t.Parallel()
	needs := event.Of{Kind: RuleNeeds, Seq: 7_000_000}
	cases := []struct {
		name   string
		recs   []event.Record
		settle string // a task whose question is settled another way after the records (RetireAnswers)
		lines  map[int64]State
		briefs map[string]int64
		ended  Ended // the last record's
		check  func(t *testing.T, o *Outbox)
	}{
		{
			name: "a lead line queued; its call's reply and its rule's did key ended",
			recs: []event.Record{fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "[obor] x", Call: "c1",
				Reply: "SENT", Of: needs})},
			lines: map[int64]State{1: Waiting},
			ended: Ended{Of: needs, Reply: &Reply{Method: wire.MethodSend, Call: "c1", Text: "SENT"},
				Msg: &event.Message{To: "pj-lead", Line: "[obor] x", Call: "c1", Reply: "SENT", Of: needs}},
			check: func(t *testing.T, o *Outbox) {
				if q := o.Line(1); !q.IsLead() || q.IsBrief() || !o.Waits(needs) {
					t.Errorf("line 1 lead %v, brief %v, waits %v; want a waiting lead line", q.IsLead(), q.IsBrief(), o.Waits(needs))
				}
			},
		},
		{
			name: "a newer brief supersedes exactly the lines it names; the brief is the newer one",
			recs: []event.Record{
				fact(1, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor pj-lead] A"}),
				fact(2, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor pj-lead] B"}),
				deliver(2, event.KindIntent, LineData{To: "pj-rev"}), // B's delivery in flight: the next brief keeps it
				fact(3, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor pj-lead] C", Call: "b3",
					Reply: "BRIEFED", Supersedes: []int64{1}}),
			},
			lines:  map[int64]State{2: Sending, 3: Waiting},
			briefs: map[string]int64{"pj-rev": 3},
			ended: Ended{Reply: &Reply{Method: wire.MethodBrief, Call: "b3", Text: "BRIEFED"},
				Msg: &event.Message{To: "pj-rev", Line: "[obor pj-lead] C", Call: "b3", Reply: "BRIEFED", Supersedes: []int64{1}}},
		},
		{
			name: "a lead batch: its post claims its members; a later line waits for its own post",
			recs: []event.Record{
				fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "one"}),
				fact(2, event.StepMessage, event.Message{To: "pj-lead", Line: "two"}),
				deliver(1, event.KindIntent, LineData{To: "pj-lead", Lines: []int64{1, 2}}),
				fact(3, event.StepMessage, event.Message{To: "pj-lead", Line: "three"}),
			},
			lines: map[int64]State{1: Sending, 2: Sending, 3: Waiting},
			ended: Ended{Msg: &event.Message{To: "pj-lead", Line: "three"}},
			check: func(t *testing.T, o *Outbox) {
				if got := o.Post(1); !reflect.DeepEqual(got, []int64{1, 2}) || o.Line(2).Post() != 1 || o.Line(3).Post() != 0 {
					t.Errorf("post 1 %v, line 2's post %d, line 3's %d; want [1 2], 1, 0", got, o.Line(2).Post(), o.Line(3).Post())
				}
			},
		},
		{
			name: "a lead batch's result settles exactly its members; a lone result its own line",
			recs: []event.Record{
				fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "one"}),
				fact(2, event.StepMessage, event.Message{To: "pj-lead", Line: "two"}),
				fact(3, event.StepMessage, event.Message{To: "pj-lead", Line: "three"}),
				fact(4, event.StepMessage, event.Message{To: "pj-lead", Line: "four"}),
				deliver(1, event.KindIntent, LineData{To: "pj-lead", Lines: []int64{1, 2}}),
				deliver(1, event.KindResult, "sent"),
				deliver(3, event.KindResult, "sent"), // no intent folded (a record from before T-654)
			},
			lines: map[int64]State{4: Waiting},
			check: func(t *testing.T, o *Outbox) {
				if got := o.Post(1); got != nil {
					t.Errorf("post 1 still open: %v", got)
				}
			},
		},
		{
			name: "a lead batch refused before a byte: its members wait again, the refused post kept, still that post's",
			recs: []event.Record{
				fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "one"}),
				fact(2, event.StepMessage, event.Message{To: "pj-lead", Line: "two"}),
				deliver(1, event.KindIntent, LineData{To: "pj-lead", Lines: []int64{1, 2}}),
				failed(1, ViaInbox),
				failed(2, ViaInbox),
			},
			lines: map[int64]State{1: Waiting, 2: Waiting},
			check: func(t *testing.T, o *Outbox) {
				if q := o.Line(2); q.Refused() != "refused" || !q.RefusedPost() || q.Why() == "" || q.Post() != 1 {
					t.Errorf("line 2 refused %q (post %v), why %q, post %d; want the refusal kept, still post 1's member",
						q.Refused(), q.RefusedPost(), q.Why(), q.Post())
				}
			},
		},
		{
			name: "a newer answer retires the older undelivered one and ends the question",
			recs: []event.Record{
				fact(1, event.StepAnswered, event.Message{To: "pj-l-01", Line: "[obor answer] a", Task: "T-1", Call: "r1"}),
				fact(2, event.StepAnswered, event.Message{To: "pj-l-01", Line: "[obor answer] b", Task: "T-1", Call: "r2",
					Reply: "ANSWERED"}),
			},
			lines: map[int64]State{2: Waiting},
			ended: Ended{Reply: &Reply{Method: wire.MethodResume, Call: "r2", Text: "ANSWERED"}, Answered: "T-1"},
			check: func(t *testing.T, o *Outbox) {
				if !o.Line(2).IsAnswer() {
					t.Error("line 2 is no answer's")
				}
			},
		},
		{
			name: "a drive's answer ends no question",
			recs: []event.Record{
				fact(1, event.StepAnswered, event.Message{To: "pj-l-01", Line: "a", Task: "T-1", Driver: true}),
				fact(2, event.StepAnswered, event.Message{To: "pj-l-01", Line: "b", Task: "T-1", Driver: true}),
			},
			lines: map[int64]State{1: Waiting, 2: Waiting},
			ended: Ended{Reply: &Reply{Method: wire.MethodResume}},
		},
		{
			name: "a settled question: its waiting answer retires, its sending one stays until its delivery closes unsent",
			recs: []event.Record{
				fact(1, event.StepAnswered, event.Message{To: "pj-l-01", Line: "a", Task: "T-1"}),
				fact(2, event.StepAnswered, event.Message{To: "pj-l-02", Line: "b", Task: "T-2"}),
				deliver(2, event.KindIntent, LineData{To: "pj-l-02"}),
				fact(3, event.StepAnswered, event.Message{To: "pj-l-02", Line: "c", Task: "T-2", Driver: true}),
			},
			settle: "T-2",
			lines:  map[int64]State{1: Waiting, 2: SendingSettled},
			ended:  Ended{Reply: &Reply{Method: wire.MethodResume}},
			check: func(t *testing.T, o *Outbox) {
				o.Fold(deliver(2, event.KindResult, RetiredResult), noOld)
				if got := lines(o); !reflect.DeepEqual(got, map[int64]State{1: Waiting}) {
					t.Errorf("after the close: lines %v, want T-1's answer alone", got)
				}
			},
		},
		{
			name: "a role agent's usage-limit stop keeps its brief open, stopped; the alarm line marks it; a turn ends it",
			recs: []event.Record{
				fact(1, event.StepBriefed, event.Message{To: "pj-rev"}),
				fact(2, event.StepWorked, Worked{To: "pj-rev", Brief: 1}),
				fact(3, event.StepStopped, Stopped{Agent: "pj-rev", Error: "usage", Brief: 1, Turn: "t1", Reset: 99,
					Message: event.Message{To: "pj-lead", Line: "[obor] pj-rev stopped: usage", Call: "s3", Reply: "STOPPED"}}),
				fact(4, event.StepMessage, event.Message{To: "pj-lead", Line: "[obor] alarm", Stop: 3}),
			},
			lines:  map[int64]State{3: Waiting, 4: Waiting},
			briefs: map[string]int64{"pj-rev": 1},
			ended:  Ended{Msg: &event.Message{To: "pj-lead", Line: "[obor] alarm", Stop: 3}},
			check: func(t *testing.T, o *Outbox) {
				b := o.Brief("pj-rev")
				s := b.Stop()
				if s == nil || s.Seq != 3 || s.Turn != "t1" || s.Reset != 99 || !s.Alarmed || b.Worked() || b.Logged() {
					t.Fatalf("brief worked %v, logged %v, stop %+v; want stopped by 3, alarmed, its work reset", b.Worked(),
						b.Logged(), s)
				}
				o.Woke("pj-rev", t0.Add(time.Hour))
				if s := b.Stop(); s.Woke != t0.Add(time.Hour) {
					t.Errorf("woke %v, want its first wake", s.Woke)
				}
				o.Fold(fact(5, event.StepTurned, Turned{To: "pj-rev", Brief: 1}), noOld)
				if b.Stop() != nil || !b.Followed() || b.Worked() {
					t.Errorf("after the turn's start: stop %+v, followed %v, worked %v; want followed, no stop", b.Stop(),
						b.Followed(), b.Worked())
				}
			},
		},
		{
			name: "a stop that ends the brief: its lead line queued; the stop, its stored reply and the brief's end ended",
			recs: []event.Record{
				fact(1, event.StepBriefed, event.Message{To: "pj-rev"}),
				fact(2, event.StepStopped, Stopped{Agent: "pj-rev", Error: "api", Message: event.Message{To: "pj-lead",
					Line: "[obor] pj-rev stopped: api", Ends: "pj-rev", Call: "s2", Reply: "STOPPED"}}),
			},
			lines: map[int64]State{2: Waiting},
			ended: Ended{Reply: &Reply{Method: wire.MethodStopped, Call: "s2", Text: "STOPPED"}, BriefEnd: "pj-rev",
				Stopped: &Stopped{Agent: "pj-rev", Error: "api", Message: event.Message{To: "pj-lead",
					Line: "[obor] pj-rev stopped: api", Ends: "pj-rev", Call: "s2", Reply: "STOPPED"}}},
		},
		{
			name: "the finished line ends the brief; the brief's line delivered starts its age",
			recs: []event.Record{
				fact(1, event.StepBriefed, event.Message{To: "pj-rev", Line: "[obor pj-lead] go"}),
				deliver(1, event.KindIntent, LineData{To: "pj-rev"}),
				deliver(1, event.KindResult, "sent"),
			},
			briefs: map[string]int64{"pj-rev": 1},
			check: func(t *testing.T, o *Outbox) {
				if at := o.Brief("pj-rev").At(); !at.Equal(t0) { // the result's own time (seq 0 here: t0)
					t.Errorf("brief at %v, want its line's delivery %v", at, t0)
				}
				e := o.Fold(fact(9, event.StepMessage, event.Message{To: "pj-lead", Line: "[obor] pj-rev finished",
					Ends: "pj-rev"}), noOld)
				if e.BriefEnd != "pj-rev" || o.Brief("pj-rev") != nil {
					t.Errorf("ended %q, brief %v; want the brief ended", e.BriefEnd, o.Brief("pj-rev"))
				}
			},
		},
		{
			name: "a needs-you line delivered: its delivery time kept for the repeat",
			recs: []event.Record{
				fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "[obor] needs-you", Of: needs}),
				deliver(1, event.KindResult, "sent"),
			},
			lines: map[int64]State{},
			check: func(t *testing.T, o *Outbox) {
				if at, ok := o.Delivered(needs); !ok || !at.Equal(t0) || o.Waits(needs) {
					t.Errorf("delivered %v %v, waits %v; want its result's time, no line waiting", at, ok, o.Waits(needs))
				}
			},
		},
		{
			name:  "another task's record: nothing",
			recs:  []event.Record{{Seq: 1, Task: "T-1", Step: event.StepMessage, Kind: event.KindFact}},
			lines: map[int64]State{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			o := New("pj-lead", "claude")
			var e Ended
			for _, r := range c.recs {
				e = o.Fold(r, noOld)
			}
			if c.settle != "" {
				o.RetireAnswers(c.settle, 0)
			}
			if c.lines == nil {
				c.lines = map[int64]State{}
			}
			if got := lines(&o); !reflect.DeepEqual(got, c.lines) {
				t.Errorf("lines %v, want %v", got, c.lines)
			}
			if c.briefs == nil {
				c.briefs = map[string]int64{}
			}
			if got := briefs(&o); !reflect.DeepEqual(got, c.briefs) {
				t.Errorf("briefs %v, want %v", got, c.briefs)
			}
			c.ended.At = event.RecordTime(c.recs[len(c.recs)-1])
			if !reflect.DeepEqual(e, c.ended) {
				t.Errorf("ended %+v, want %+v", e, c.ended)
			}
			if c.check != nil {
				c.check(t, &o)
			}
		})
	}
}

// TestFoldOldOf: a message recorded before T-927 takes its did key from oldOf; one with its own Of, a brief, or
// another step never asks.
func TestFoldOldOf(t *testing.T) {
	t.Parallel()
	o := New("pj-lead", "")
	asked := 0
	old := func(json.RawMessage) event.Of { asked++; return event.Of{Kind: RuleNeeds, Seq: 5} }
	if e := o.Fold(fact(1, event.StepMessage, event.Message{To: "pj-lead", Line: "old"}), old); asked != 1 ||
		e.Of != (event.Of{Kind: RuleNeeds, Seq: 5}) || o.Line(1).Msg().Of != e.Of {
		t.Fatalf("asked %d, ended %+v, line's Of %+v; want the old key, kept on the line", asked, e.Of, o.Line(1).Msg().Of)
	}
	o.Fold(fact(2, event.StepMessage, event.Message{To: "pj-lead", Line: "new", Of: event.Of{Kind: "watch", Seq: 2}}), old)
	o.Fold(fact(3, event.StepBriefed, event.Message{To: "pj-rev", Line: "b"}), old)
	if asked != 1 {
		t.Errorf("oldOf asked %d times, want once (the old message only)", asked)
	}
}

// TestStatus: obor status' rows: a brief whose line is queued is not delivered; a line's age and why it waits.
func TestStatus(t *testing.T) {
	t.Parallel()
	o := New("pj-lead", "")
	o.Fold(fact(1, event.StepBriefed, event.Message{To: "pj-rev", From: "pj-lead", Line: "go"}), noOld)
	o.Fold(fact(2, event.StepBriefed, event.Message{To: "pj-deep"}), noOld)
	o.Line(1).Held = "pj-rev is busy"
	bs := o.BriefStatus()
	want := []wire.BriefStatus{{To: "pj-deep", Since: t0.Add(2 * time.Second).Unix(), Delivered: true},
		{To: "pj-rev", Since: t0.Add(time.Second).Unix()}}
	if !reflect.DeepEqual(bs, want) {
		t.Errorf("briefs %+v, want %+v", bs, want)
	}
	ms := o.MessageStatus(t0.Add(time.Minute))
	if len(ms) != 1 || ms[0].To != "pj-rev" || ms[0].Age != 59 || ms[0].Held != "pj-rev is busy" {
		t.Errorf("messages %+v, want pj-rev's line, 59 s old, held busy", ms)
	}
}
