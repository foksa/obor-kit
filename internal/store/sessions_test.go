package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// an agent's current session is its newest by start (of two at one second, the later recorded); none: ok
// false; no database: no sessions.
func TestCurrentSession(t *testing.T) {
	ss := []Session{{Agent: "a", ID: "1", Started: 10}, {Agent: "b", ID: "2", Started: 30}, {Agent: "a", ID: "3", Started: 20},
		{Agent: "a", ID: "4", Started: 5}, {Agent: "b", ID: "5", Started: 30}}
	if s, ok := FoldSessions(ss).Current("a"); !ok || s.ID != "3" {
		t.Errorf("a: %+v %v, want 3 (the newest, not the last recorded)", s, ok)
	}
	if s, _ := FoldSessions(ss).Current("b"); s.ID != "5" {
		t.Errorf("b: %+v, want 5 (a tie: the later)", s)
	}
	if _, ok := FoldSessions(ss).Current("c"); ok {
		t.Error("c: none")
	}
	if ss, err := ReadSessionsAt(t.TempDir()); err != nil || len(ss) != 0 {
		t.Errorf("no database: %v %v", ss, err)
	}
}

// Owned keeps a fact only when the fold admits it: a repeat of (agent, id) and an id that is another
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
	if h := FoldSessions(ss[:1]).Holder("b", "x"); h != "a" {
		t.Errorf("holder of x %q, want a", h)
	}
}

// the confirmation of the id a launch recorded before its start — the same (agent, id), now with the
// transcript the session reports — is that record, not a second one; a repeat that names no new transcript records
// nothing.
func TestOwnedConfirmation(t *testing.T) {
	ss := []Session{{Agent: "a", ID: "x", Started: 10}, {Agent: "a", ID: "x", Transcript: "/t/x.jsonl", Started: 20}}
	got := Owned(ss)
	if len(got) != 1 || got[0].ID != "x" || got[0].Transcript != "/t/x.jsonl" || got[0].Started != 20 {
		t.Fatalf("owned %+v, want x with the confirmation's transcript and start", got)
	}
	f := FoldSessions(got)
	if f.Add(Session{Agent: "a", ID: "x", Started: 30}) {
		t.Error("a later repeat with no transcript is admitted")
	}
	if f.Add(Session{Agent: "a", ID: "x", Transcript: "/t/x.jsonl", Started: 30}) {
		t.Error("a repeat of the confirmed record is admitted")
	}
}

// the fold keeps every rule of the scans it replaces, one case each: the timestamp tie (of two at one second,
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
			var f SessionFold
			for _, s := range c.log {
				f.Add(s)
			}
			for agent, id := range c.current {
				if s, ok := f.Current(agent); !ok || s.ID != id {
					t.Errorf("%s's current session %+v %v, want %q", agent, s, ok, id)
				}
				if s, ok := currentSlow(got, agent); !ok || s.ID != id {
					t.Errorf("Current(%s) %+v %v, want %q", agent, s, ok, id)
				}
			}
		})
	}
}

// admitsSlow is admission applied as a scan of the records so far: a scan of the records, and one per agent of
// them for the holder (Current's rule, scanned).
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
		if cur, _ := currentSlow(ss, r.Agent); cur.ID == s.ID {
			return false
		}
	}
	return true
}

// currentSlow is Current's rule as a scan of ss.
func currentSlow(ss []Session, agent string) (Session, bool) {
	var cur Session
	ok := false
	for _, s := range ss {
		if s.Agent == agent && (!ok || s.Started >= cur.Started) {
			cur, ok = s, true
		}
	}
	return cur, ok
}

// ownedSlow is the record-by-record reference over admitsSlow: TestOwnedMatchesTheScan
// holds the fold to it.
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

// the fold's one pass is the scans it replaces, on every log of four records over a small alphabet (agent ×
// session × start × transcript: 16 kinds of record): the same owned records in the same order, and the current
// session it keeps each agent's newest by Current's rule. One Add held to its scan on every log of three, the size
// a per-call fold can afford.
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
		var f SessionFold
		for _, s := range log {
			f.Add(s)
		}
		for _, agent := range []string{"a", "b"} {
			cur, ok := f.Current(agent)
			if want, wantOK := currentSlow(got, agent); ok != wantOK || cur != want {
				return fmt.Errorf("current(%s) of %+v = %+v %v, want %+v %v", agent, log, cur, ok, want, wantOK)
			}
		}
		return nil
	})
	walk(3, func() error {
		want := ownedSlow(log)
		for _, s := range kinds {
			f := FoldSessions(want)
			if got, wantAdmits := f.Add(s), admitsSlow(want, s); got != wantAdmits {
				return fmt.Errorf("log %+v: the fold admits %+v of %+v = %v, want %v", log, s, want, got, wantAdmits)
			}
		}
		return nil
	})
}

// a log folds in one pass — the index Add builds is the one the fold keeps, so the work per fact does not grow
// with the log. The quadratic build this replaces rebuilt the whole index for every fact: allocations per fact were the
// log's length (2,046 at 2,000 facts; 5,060 at 5,000), against a handful here. A bound on allocations, not wall clock.
func TestFoldKeepsItsIndex(t *testing.T) {
	const n = 2000
	ss := make([]Session, n)
	for i := range ss {
		ss[i] = Session{Agent: fmt.Sprintf("a%d", i%16), ID: fmt.Sprintf("s%d", i), Started: int64(i)}
	}
	allocs := testing.AllocsPerRun(5, func() {
		var f SessionFold
		for _, s := range ss {
			f.Add(s)
		}
	})
	if per := allocs / n; per > 20 {
		t.Errorf("folding %d facts allocated %.1f objects per fact, want under 20: the index is not built once", n, per)
	} else {
		t.Logf("folding %d facts: %.1f objects per fact", n, per)
	}
}

// putSessions writes ss as session facts, seq 1… in order, the i-th at ts[i] (for the prune tests).
func putSessions(t *testing.T, db *sql.DB, ss []Session, ts []time.Time) {
	t.Helper()
	for i, s := range ss {
		b, _ := json.Marshal(s)
		if _, err := db.Exec("INSERT INTO events (seq, ts, task, attempt, step, kind, class, data) VALUES (?, ?, ?, 0, ?, 'fact', '', ?)",
			i+1, ts[i].UTC().Format(time.RFC3339), SessionTask, StepSession, string(b)); err != nil {
			t.Fatal(err)
		}
	}
}

// sessionSeqs is the seqs of db's session facts, in order.
func sessionSeqs(t *testing.T, db *sql.DB) []int64 {
	t.Helper()
	rows, err := db.Query("SELECT seq FROM events WHERE task = ? ORDER BY seq", SessionTask)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		out = append(out, seq)
	}
	return out
}

// the prune deletes an old session id no agent's current session is, all its records; a current session
// keeps its start and its confirmation however old; a session written since the cutoff stays, and so does an old one
// at the horizon (the table's newest row). A second prune deletes nothing.
func TestPruneSessions(t *testing.T) {
	t.Parallel()
	db, err := OpenRW(newPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	old, recent := cutoff.Add(-24*time.Hour), cutoff.Add(24*time.Hour)
	putSessions(t, db, []Session{
		{Agent: "a", ID: "s1", Started: 10},
		{Agent: "a", ID: "s2", Started: 20},
		{Agent: "a", ID: "s2", Transcript: "/t/s2.jsonl", Started: 20},
		{Agent: "b", ID: "s3", Started: 30},
		{Agent: "c", ID: "s5", Started: 50},
		{Agent: "c", ID: "s4", Started: 40}, // not c's current, old, but the table's newest row
	}, []time.Time{old, old, old, recent, old, old})
	n, err := PruneSessions(db, cutoff, 6)
	if err != nil || n != 1 {
		t.Fatalf("PruneSessions = %d, %v; want s1's record", n, err)
	}
	if got := sessionSeqs(t, db); !slices.Equal(got, []int64{2, 3, 4, 5, 6}) {
		t.Errorf("session facts kept %v, want a's current s2 (both), b's s3, c's s5 and s4 (at the horizon)", got)
	}
	if _, err := db.Exec("INSERT INTO events (seq, ts, task, attempt, step, kind, class) VALUES (7, ?, 'T-1', 1, 'x', 'fact', '')",
		recent.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if n, err := PruneSessions(db, cutoff, 7); err != nil || n != 1 {
		t.Errorf("with a newer row PruneSessions = %d, %v; want s4's record", n, err)
	}
	if n, err := PruneSessions(db, cutoff, 7); err != nil || n != 0 {
		t.Errorf("a second prune = %d, %v; want nothing", n, err)
	}
}

// when the sessions left would fold differently (a's old session y, gone, was its current one when b's w was
// admitted; without it a's w holds w again and b's would be refused), nothing is deleted and that is the error.
func TestPruneSessionsKeepsTheFold(t *testing.T) {
	t.Parallel()
	db, err := OpenRW(newPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cutoff := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	old, recent := cutoff.Add(-24*time.Hour), cutoff.Add(24*time.Hour)
	ss := []Session{{Agent: "a", ID: "w", Started: 100}, {Agent: "a", ID: "y", Started: 200}, {Agent: "b", ID: "w", Started: 250},
		{Agent: "a", ID: "z", Started: 300}}
	putSessions(t, db, ss, []time.Time{old, old, recent, recent})
	if got := Owned(ss); len(got) != 4 {
		t.Fatalf("precondition: owned %+v, want all four", got)
	}
	n, err := PruneSessions(db, cutoff, 10)
	if err == nil || n != 0 || !strings.Contains(err.Error(), "would fold differently") {
		t.Errorf("PruneSessions = %d, %v; want refused", n, err)
	}
	if got := sessionSeqs(t, db); len(got) != 4 {
		t.Errorf("session facts %v, want all four kept", got)
	}
}
