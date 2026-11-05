package app

// T-893 (DD-encapsulation E1): Describe words every record kind the readers show from the keys serve writes. Each case
// is the data as a log holds it, so a renamed or retyped key in one record type fails here; the table must cover every
// worded kind. T-688: a gate that is not green and a hold are bad; a green gate is not, even when its summary counts a
// failure.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"obor/internal/app/event"
)

func TestDescribeWordsEveryKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, kind, step, data string
		want                   Described
	}{
		{"a green gate whose summary counts a fail", event.KindResult, event.StepGate,
			`{"outcome":"green","line":"check 25 pass, 0 fail","spec_rev":3}`,
			Described{Line: "gate T-1 pass (check 25 pass, 0 fail)", Outcome: "green", Text: "check 25 pass, 0 fail", SpecRev: 3}},
		{"a red gate, its second run", event.KindResult, event.StepGate + ".2",
			`{"outcome":"red","line":"RED T-1: e2e failed"}`,
			Described{Line: "gate T-1 fail (RED T-1: e2e failed)", Bad: true, Outcome: "red", Text: "RED T-1: e2e failed"}},
		{"an accept", event.KindResult, "accept.c1", `{"job":"T-1/1/review","reply":"ok"}`,
			Described{Line: "accepted T-1"}},
		{"the integrator's sendback", event.KindResult, "sendback.c2", `{"job":"T-1/1/review","note":"a nit"}`,
			Described{Line: "sendback T-1: a nit"}},
		{"a dispatch", event.KindFact, event.StepDispatched,
			`{"lane":"L-01","agent":"pj-l-01","level":"hard","types":["opus","sonnet"],"type":"opus:high"}`,
			Described{Line: "dispatched T-1 (L-01)", Lane: "L-01", Agent: "pj-l-01", Level: "hard",
				Types: []string{"opus", "sonnet"}, Type: "opus:high"}},
		// T-751: the owner's drive, new and taken over
		{"a drive", event.KindFact, event.StepDispatched, `{"lane":"L-01","drive":true}`,
			Described{Line: "drive T-1 (L-01)", Lane: "L-01"}},
		{"a take-over", event.KindFact, event.StepDrive, `{"lane":"L-01","agent":"pj-l-01","drive":true}`,
			Described{Line: "drive T-1 (L-01): taken over by the owner", Lane: "L-01", Agent: "pj-l-01"}},
		{"a submission", event.KindFact, event.StepSubmitted, `{"lane":"L-01","agent":"pj-l-01","report":"Summary: x"}`,
			Described{Line: "submitted T-1", Lane: "L-01", Agent: "pj-l-01", Report: "Summary: x"}},
		{"a red gate's sendback", event.KindFact, event.StepSendback, `{"note":"fix it"}`,
			Described{Line: "sendback T-1: fix it"}},
		{"a hold", event.KindFact, event.StepNeedsYou, `{"lane":"L-01","cause":"held","reason":"held: decide"}`,
			Described{Line: "held T-1: decide", Bad: true, Lane: "L-01"}},
		{"a question is not worded", event.KindFact, event.StepNeedsYou, `{"lane":"L-01","reason":"asks: may I?"}`,
			Described{Lane: "L-01"}},
		{"a merge", event.KindFact, event.StepMerged, `{"head":"abcdef0123"}`, Described{Line: "merged T-1 abcdef0"}},
		{"a merge outside serve", event.KindFact, event.StepMerged, `{"outside":true}`,
			Described{Line: "merged T-1 (outside obor serve)"}},
		{"a review request", event.KindFact, event.StepReviewRequest, `{"after":["T-1","T-2"],"by":"lead"}`,
			Described{Line: "review requested (after T-1,T-2)"}},
		{"a review's start", event.KindFact, event.StepReviewStarted, `{"id":"RV-9"}`, Described{Line: "review started RV-9"}},
		{"a review request's end", event.KindFact, event.StepReviewRm, ``, Described{Line: "review request ended"}},
		{"a line", event.KindFact, event.StepMessage, `{"to":"pj-l-01","line":"look","from":"pj-lead"}`,
			Described{Line: "line to pj-l-01: look", To: "pj-l-01", From: "pj-lead", Text: "look"}},
		{"an intent is not worded", event.KindIntent, event.StepGate, `{"kind":"lane"}`, Described{}},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		r := event.Record{Task: "T-1", Kind: c.kind, Step: c.step}
		if c.data != "" {
			r.Data = json.RawMessage(c.data)
		}
		covered[describedKey(r)] = true
		if got := Describe(r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	for key := range worded {
		if !covered[key] {
			t.Errorf("worded kind %q has no case here", key)
		}
	}
}

// A record whose data is not its kind's shape is shown as unreadable, bad news, never dropped (T-893: a message's drive
// is an attempt number; a bool there was the view's mirror's silent mistake).
func TestDescribeShowsADecodeError(t *testing.T) {
	t.Parallel()
	r := event.Record{Task: event.MsgTask, Kind: event.KindFact, Step: event.StepMessage,
		Data: json.RawMessage(`{"to":"pj-lead","line":"hi","drive":true}`)}
	d := Describe(r)
	if !d.Bad || !strings.HasPrefix(d.Line, "message msg: unreadable record ") || !strings.Contains(d.Line, "drive") {
		t.Errorf("a retyped key: %+v, want a bad line naming the unreadable record and its key", d)
	}
	r.Data = json.RawMessage(`{"to":"pj-lead","line":"hi","drive":3}`)
	if d := Describe(r); d.Bad || d.Line != "line to pj-lead: hi" {
		t.Errorf("precondition: the same record with its real shape: %+v", d)
	}
}
