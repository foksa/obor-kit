package view

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"obor/internal/app/wire"
	"obor/internal/testkit"
)

// T-977: the view (obor status --json, the web) carries serve's promotion when branches.promote is set and no promote
// key when it is not; a waiting dispatch's item carries serve's difficulty and model.
func TestViewPromoteOnAndOff(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	now := time.Now()
	waiting := []wire.TaskStatus{{Task: "T-5", State: "waiting", Phase: wire.PhaseWaiting, Level: "medium",
		Types: []string{"sonnet", "deepseek"}, Difficulty: "medium", Model: "deepseek"},
		{Task: "T-6", State: "waiting", Phase: wire.PhaseWaiting, After: []string{"T-5"}, Difficulty: "hard",
			Model: "opus"}}
	read := func(promote *wire.PromoteStatus) (Project, map[string]json.RawMessage) {
		t.Helper()
		ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
			Project: &wire.ProjectStatus{Name: "proj", Root: p.Root, Tasks: waiting, Promote: promote}}
		v, err := reading{ask: func(context.Context, string) (wire.Status, error) { return ss, nil }}.load(
			context.Background(), p.Root, now, 5)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var js map[string]json.RawMessage
		if err := json.Unmarshal(b, &js); err != nil {
			t.Fatal(err)
		}
		return v, js
	}

	v, js := read(nil)
	if !v.Serve.Up {
		t.Fatalf("precondition: serve up, view %+v", v.Serve)
	}
	if _, ok := js["promote"]; ok || v.Promote != nil {
		t.Errorf("promotion off: promote %s in the JSON, %+v in the view; want neither", js["promote"], v.Promote)
	}
	if w := v.Queue.Waiting; len(w) != 2 || w[0].Task != "T-5" || w[0].Difficulty != "medium" || w[0].Model != "deepseek" ||
		w[1].Task != "T-6" || w[1].WaitsOn != WaitsAfter || w[1].Difficulty != "hard" || w[1].Model != "opus" {
		t.Errorf("waiting %+v; want T-5 medium on deepseek, T-6 after T-5 hard on opus", w)
	}

	on := &wire.PromoteStatus{Branch: "main", SHA: "abcdef0", At: now.Unix(), Merges: 2, Tasks: []string{"T-1", "T-2"},
		Weight: 3, Threshold: 6, Run: &wire.PromoteRun{SHA: "h2", Since: now.Unix(), Step: wire.PromoteReview, Review: "RV-5"}}
	v, js = read(on)
	if v.Promote == nil || v.Promote.Weight != 3 || v.Promote.Run == nil || v.Promote.Run.Review != "RV-5" {
		t.Fatalf("promotion on: view promote %+v, want serve's", v.Promote)
	}
	var got wire.PromoteStatus
	if err := json.Unmarshal(js["promote"], &got); err != nil {
		t.Fatalf("promotion on: the JSON's promote %s: %v", js["promote"], err)
	}
	if got.Threshold != 6 || len(got.Tasks) != 2 || got.Run == nil || got.Run.Step != wire.PromoteReview || got.At != now.Unix() {
		t.Errorf("the JSON's promote %+v, want serve's tasks, weight of 6, the wait for RV-5 and the promoted time", got)
	}
}
