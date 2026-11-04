package store

import (
	"strings"
	"testing"
)

// T-577: an agent's current session is its newest by start (of two at one second, the later recorded); none: ok
// false; no database: no sessions.
func TestCurrentSession(t *testing.T) {
	ss := []Session{{Agent: "a", ID: "1", Started: 10}, {Agent: "b", ID: "2", Started: 30}, {Agent: "a", ID: "3", Started: 20},
		{Agent: "a", ID: "4", Started: 5}, {Agent: "b", ID: "5", Started: 30}}
	if s, ok := Current(ss, "a"); !ok || s.ID != "3" {
		t.Errorf("a: %+v %v, want 3 (the newest, not the last recorded)", s, ok)
	}
	if s, _ := Current(ss, "b"); s.ID != "5" {
		t.Errorf("b: %+v, want 5 (a tie: the later)", s)
	}
	if _, ok := Current(ss, "c"); ok {
		t.Error("c: none")
	}
	if ss, err := ReadSessionsAt(t.TempDir()); err != nil || len(ss) != 0 {
		t.Errorf("no database: %v %v", ss, err)
	}
}

// T-586, T-588: Owned keeps a fact only when Admits takes it: a repeat of (agent, id) and an id that is another
// agent's current session are dropped; an id its holder has moved on from is free again.
func TestOwnedSessions(t *testing.T) {
	ss := []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "b", ID: "x", Started: 20}, {Agent: "a", ID: "x", Started: 30},
		{Agent: "a", ID: "y", Started: 40}, {Agent: "b", ID: "x", Started: 50}, {Agent: "", ID: "z"}}
	var got []string
	for _, s := range Owned(ss) {
		got = append(got, s.Agent+":"+s.ID)
	}
	if want := "a:x a:y b:x"; strings.Join(got, " ") != want {
		t.Errorf("owned %q, want %q", strings.Join(got, " "), want)
	}
	if h := Holder(ss[:1], "b", "x"); h != "a" {
		t.Errorf("holder of x %q, want a", h)
	}
}

// T-813: the confirmation of the id a launch recorded before its start — the same (agent, id), now with the
// transcript the session reports — is that record, not a second one; a repeat that names no new transcript records
// nothing.
func TestOwnedConfirmation(t *testing.T) {
	ss := []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "a", ID: "x", Transcript: "/t/x.jsonl", Started: 20}}
	got := Owned(ss)
	if len(got) != 1 || got[0].ID != "x" || got[0].Transcript != "/t/x.jsonl" || got[0].Started != 20 {
		t.Fatalf("owned %+v, want x with the confirmation's transcript and start", got)
	}
	if Admits(got, Session{Agent: "a", ID: "x", Started: 30}) {
		t.Error("a later repeat with no transcript is admitted")
	}
	if Admits(got, Session{Agent: "a", ID: "x", Transcript: "/t/x.jsonl", Started: 30}) {
		t.Error("a repeat of the confirmed record is admitted")
	}
}
