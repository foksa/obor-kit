package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// T-977: serve's status carries the promotion the monitor shows: the tasks merged since and their weight against the
// threshold, the run under way (the suite, then the wait for review RV-n), the RED run while its needs-you is open, and
// when the promoted sha was promoted; none of it with branches.promote unset.

// promoteJSON is the status' promote object as JSON keys (nil: none sent).
func promoteJSON(t *testing.T, a *App) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a.Status().Project)
	if err != nil {
		t.Fatal(err)
	}
	var ps map[string]json.RawMessage
	if err := json.Unmarshal(b, &ps); err != nil {
		t.Fatal(err)
	}
	raw, ok := ps["promote"]
	if !ok {
		return nil
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPromoteStatusOff(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.fx.Promote = nil // branches.promote unset
	r.git.between("h0", "h1", "T-1")
	r.pass()
	if st := r.a.Status().Project; st == nil || st.Promote != nil {
		t.Fatalf("promotion off: status %+v, want a project with no promote", st)
	}
	if js := promoteJSON(t, r.a); js != nil {
		t.Errorf("promotion off: the JSON has promote %v, want no key", js)
	}
}

func TestPromoteStatusWeightAndSuite(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.busy()
	r.spec("T-1", "simple")
	r.spec("T-2", "medium")
	r.git.between("h0", "h1", "T-1", "T-2")
	r.pass()
	if runs, _, _ := r.f.calls(); len(runs) != 0 {
		t.Fatalf("precondition: weight 3 < 6 with the loop busy runs nothing: %q", runs)
	}
	st := r.a.Status().Project.Promote
	if st == nil || strings.Join(st.Tasks, ",") != "T-1,T-2" || st.Weight != 3 || st.Threshold != 6 || st.Merges != 2 {
		t.Fatalf("status promote = %+v, want T-1,T-2 merged since, weight 3 of 6", st)
	}
	if st.Run != nil || st.Red != nil || st.At != 0 {
		t.Errorf("nothing runs, nothing RED, no fact promoted: run %+v, red %+v, at %d; want none", st.Run, st.Red, st.At)
	}
	js := promoteJSON(t, r.a)
	for _, k := range []string{"tasks", "weight", "threshold", "branch", "sha"} {
		if _, ok := js[k]; !ok {
			t.Errorf("the JSON's promote %v lacks %q", js, k)
		}
	}

	// The third merge triggers; the suite runs (held) and the status says so.
	r.spec("T-3", "hard")
	r.git.set("staging", "h2")
	r.git.between("h0", "h2", "T-1", "T-2", "T-3")
	r.f.block, r.f.began = true, make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	r.a.promotion(ctx, r.p, r.fx)
	<-r.f.began
	st = r.a.Status().Project.Promote
	cancel()
	r.a.effects.Wait()
	if st == nil || st.Run == nil || st.Run.Step != wire.PromoteSuite || st.Run.SHA != "h2" || st.Run.Since != r.now.Unix() {
		t.Fatalf("status promote while the suite runs = %+v (run %+v), want the suite at h2 since now", st, st.Run)
	}
	if st.Weight != 6 || strings.Join(st.Tasks, ",") != "T-1,T-2,T-3" {
		t.Errorf("status promote = %+v, want T-1,T-2,T-3 at weight 6", st)
	}
}

func TestPromoteStatusWaitsForReview(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, true)
	r.git.last, r.git.lastBody = "RV-4 closed · s0 · approved · earlier", "Covered: T-0\nStart: h0"
	r.git.between("h0", "h1", "T-1", "T-2")
	r.pass() // the review request and the GREEN run
	r.pass()
	st := r.a.Status().Project.Promote
	if st == nil || st.Run == nil || st.Run.Step != wire.PromoteReview || st.Run.SHA != "h1" || st.Run.Review != "" {
		t.Fatalf("GREEN, the review only requested: status promote %+v, want waiting for review, no RV yet", st)
	}
	r.a.mu.Lock()
	q := r.p.folded.review
	r.a.mu.Unlock()
	for _, rec := range []struct {
		step string
		d    ReviewData
	}{{event.StepReviewFire, ReviewData{Request: q.seq}}, {event.StepReviewStarted, ReviewData{ID: "RV-5", Head: "h1"}}} {
		if !r.a.record(r.p, event.Record{Task: event.ReviewTask, Step: rec.step, Kind: event.KindFact}, rec.d) {
			t.Fatal("the log refused the review's fact")
		}
	}
	r.pass()
	if st := r.a.Status().Project.Promote; st == nil || st.Run == nil || st.Run.Step != wire.PromoteReview ||
		st.Run.Review != "RV-5" || st.Run.Since != r.now.Unix() {
		t.Errorf("RV-5 open: status promote %+v, want waiting for review RV-5 since the GREEN result", st)
	}
}

func TestPromoteStatusRedThenPromoted(t *testing.T) {
	t.Parallel()
	r := newPromoteRig(t, false)
	r.git.between("h0", "h1", "T-1", "T-2")
	r.f.verdicts["h1"] = "RED"
	r.pass()
	r.pass()
	if got := r.needs(); len(got) != 1 {
		t.Fatalf("precondition: the RED needs-you is open: %q", got)
	}
	st := r.a.Status().Project.Promote
	if st == nil || st.Red == nil || st.Red.SHA != "h1" || st.Red.Tests != "check 1 failed — TestFlaky" ||
		st.Red.Since != "h0" || !slices.Equal(st.Red.Merges, []string{"T-1", "T-2"}) || st.Red.At != r.now.Unix() {
		t.Fatalf("RED at h1: status promote %+v, want red h1 naming the tests and T-1, T-2 since h0", st)
	}
	if st.Run != nil {
		t.Errorf("RED: run %+v, want none", st.Run)
	}
	r.git.set("staging", "h2")
	r.git.between("h0", "h2", "T-1", "T-2", "T-3")
	r.pass() // the GREEN run at h2
	r.pass() // its fast-forward
	if got := r.needs(); len(got) != 0 {
		t.Fatalf("precondition: the GREEN promotion ended the needs-you: %q", got)
	}
	r.pass() // after_promote; the pass reads main at h2
	r.pass()
	st = r.a.Status().Project.Promote
	if st == nil || st.Red != nil || st.Run != nil || st.SHA != "h2" || st.At != r.now.Unix() || len(st.Tasks) != 0 ||
		st.Weight != 0 {
		t.Errorf("promoted h2: status promote %+v, want main at h2 promoted now, nothing RED, running or merged since", st)
	}
}
