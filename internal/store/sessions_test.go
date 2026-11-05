package store

import (
	"fmt"
	"slices"
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

// T-881: the fold keeps every rule of the scans it replaces, one case each: the timestamp tie (of two at one second,
// the later), the earlier owner's precedence (an id another agent's current session holds is refused, and free again
// once it moved on), the confirmation, and an older record kept without ever being its agent's current session.
func TestOwnedRules(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		log     []Session
		want    string            // the owned records, "agent:id …", in log order
		current map[string]string // agent → its current session's id, as the fold keeps it
	}{
		{
			name:    "the later of two at one second is current, and holds its id",
			log:     []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "a", ID: "y", Started: 10}, {Agent: "b", ID: "y", Started: 20}},
			want:    "a:x a:y",
			current: map[string]string{"a": "y"},
		},
		{
			name:    "the earlier owner keeps an id a later agent reports",
			log:     []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "b", ID: "x", Started: 20}},
			want:    "a:x",
			current: map[string]string{"a": "x"},
		},
		{
			name: "an id its holder moved on from is free again",
			log: []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "b", ID: "x", Started: 20},
				{Agent: "a", ID: "y", Started: 30}, {Agent: "b", ID: "x", Started: 40}},
			want:    "a:x a:y b:x",
			current: map[string]string{"a": "y", "b": "x"},
		},
		{
			name: "an older record is kept but is no one's current session",
			log: []Session{{Agent: "a", ID: "x", Started: 30}, {Agent: "a", ID: "y", Started: 10},
				{Agent: "b", ID: "y", Started: 20}},
			want:    "a:x a:y b:y",
			current: map[string]string{"a": "x", "b": "y"},
		},
		{
			name: "the confirmation takes its record's place",
			log: []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "a", ID: "y", Started: 20},
				{Agent: "a", ID: "x", Transcript: "/t/x", Started: 15}},
			want:    "a:x a:y",
			current: map[string]string{"a": "y"},
		},
		{
			// A stored log's own history: the confirmation starts earlier than the record it replaces, so the agent's
			// current session is its newest again — the id another agent took since stays that agent's too, as the scan
			// this replaces leaves it (the rule decides joins, not the history).
			name: "a confirmation that starts earlier hands the current session back",
			log: []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "a", ID: "y", Started: 20},
				{Agent: "b", ID: "x", Started: 30}, {Agent: "a", ID: "y", Transcript: "/t/y", Started: 5}},
			want:    "a:x a:y b:x",
			current: map[string]string{"a": "x", "b": "x"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := Owned(c.log)
			var ids []string
			for _, s := range got {
				ids = append(ids, s.Agent+":"+s.ID)
			}
			if joined := strings.Join(ids, " "); joined != c.want {
				t.Errorf("owned %q, want %q", joined, c.want)
			}
			var f sessions
			for _, s := range c.log {
				f.add(s)
			}
			for agent, id := range c.current {
				if s, ok := f.current(agent); !ok || s.ID != id {
					t.Errorf("%s's current session %+v %v, want %q", agent, s, ok, id)
				}
				if s, ok := Current(got, agent); !ok || s.ID != id {
					t.Errorf("Current(%s) %+v %v, want %q", agent, s, ok, id)
				}
			}
		})
	}
}

// admitsSlow is Admits as the readers applied it before T-881: a scan of the records so far, and one per agent of
// them for the holder (Holder).
func admitsSlow(ss []Session, s Session) bool {
	if s.Agent == "" || s.ID == "" {
		return false
	}
	for _, r := range ss {
		if r.Agent == s.Agent && r.ID == s.ID && (r.Transcript != "" || s.Transcript == "") {
			return false
		}
	}
	seen := map[string]bool{s.Agent: true}
	for _, r := range ss {
		if seen[r.Agent] {
			continue
		}
		seen[r.Agent] = true
		if cur, _ := Current(ss, r.Agent); cur.ID == s.ID {
			return false
		}
	}
	return true
}

// ownedSlow is Owned as it ran before T-881, record by record over admitsSlow: the reference TestOwnedMatchesTheScan
// holds the fold to.
func ownedSlow(ss []Session) []Session {
	var out []Session
	for _, s := range ss {
		if !admitsSlow(out, s) {
			continue
		}
		placed := false
		for i := range out {
			if out[i].Agent == s.Agent && out[i].ID == s.ID {
				out[i], placed = s, true
				break
			}
		}
		if !placed {
			out = append(out, s)
		}
	}
	return out
}

// T-881: the fold's one pass is the scans it replaces, on every log of four records over a small alphabet (agent ×
// session × start × transcript: 16 kinds of record): the same owned records in the same order, and the current
// session it keeps each agent's newest by Current's rule. Admits (one fold per call) holds to its scan on every log of
// three, the size one call's own work makes affordable.
func TestOwnedMatchesTheScan(t *testing.T) {
	t.Parallel()
	var kinds []Session
	for _, agent := range []string{"a", "b"} {
		for _, id := range []string{"x", "y"} {
			for _, started := range []int64{10, 20} {
				for _, transcript := range []string{"", "/t"} {
					kinds = append(kinds, Session{Agent: agent, ID: id, Started: started, Transcript: transcript})
				}
			}
		}
	}
	var log []Session
	var walk func(n int, check func() error)
	walk = func(n int, check func() error) {
		if n == 0 {
			if err := check(); err != nil {
				t.Fatal(err)
			}
			return
		}
		for _, s := range kinds {
			log = append(log, s)
			walk(n-1, check)
			log = log[:len(log)-1]
		}
	}
	walk(4, func() error {
		got, want := Owned(log), ownedSlow(log)
		if !slices.Equal(got, want) {
			return fmt.Errorf("Owned(%+v) = %+v, want %+v", log, got, want)
		}
		var f sessions
		for _, s := range log {
			f.add(s)
		}
		for _, agent := range []string{"a", "b"} {
			cur, ok := f.current(agent)
			if want, wantOK := Current(got, agent); ok != wantOK || cur != want {
				return fmt.Errorf("current(%s) of %+v = %+v %v, want %+v %v", agent, log, cur, ok, want, wantOK)
			}
		}
		return nil
	})
	walk(3, func() error {
		want := ownedSlow(log)
		for _, s := range kinds {
			if got, wantAdmits := Admits(want, s), admitsSlow(want, s); got != wantAdmits {
				return fmt.Errorf("log %+v: Admits(%+v, %+v) = %v, want %v", log, want, s, got, wantAdmits)
			}
		}
		return nil
	})
}
