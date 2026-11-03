package view

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// T-750: Setup is the project's steering — the difficulty levels in configured order, a pooled and a typed lane with
// their states, the roles the config names with their statuses and the pause — and how dispatches spread by level and
// chosen type, in the last 7 days and in all records: a level dispatch, an override (--types, no level), a record from
// before T-721 (its lane's configured type), a waiting dispatch that is placed later (once), a redo (again) and a
// record older than 7 days (all only).
func TestSetup(t *testing.T) {
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config(
		testkit.Set("lane_types.sonnet", map[string]any{"kind": "claude", "args": []any{}}),
		testkit.Set("lanes", map[string]any{"O-01": "opus", "L-01": ""}),
		testkit.Set("difficulty", map[string]any{"hard": []any{"opus:high", "opus"},
			"medium": []any{"sonnet", "opus"}, "simple": []any{"opus"}}),
		testkit.Set("roles.reviewer", map[string]any{"type": "opus"}),
		testkit.Set("roles.deep_dive", map[string]any{"type": "opus:high"}),
	)
	p.Lane("L-01", "") // detached at main: free, and a pooled lane (lanes.L-01 "")
	p.Commit(o1)       // O-01's task branch has a commit: working
	now := time.Now()
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	// the level dispatch, the override, the pre-T-721 record, the waiting one and the dispatch that places it, a redo
	// and one older than the window
	dispatchRecord(t, p.Root, "T-02", event.StepDispatched, dispatchData{Lane: "O-01", Level: "hard",
		Types: []string{"opus:high"}, Type: "opus:high"}, at(2*time.Hour))
	dispatchRecord(t, p.Root, "T-03", event.StepDispatched, dispatchData{Lane: "O-01", Types: []string{"sonnet"},
		Type: "sonnet"}, at(3*time.Hour))
	dispatchRecord(t, p.Root, "T-04", event.StepDispatched, dispatchData{Lane: "O-01", Level: "medium",
		Types: []string{"sonnet", "opus"}}, at(4*time.Hour)) // before T-721: no Type, the lane's configured type
	dispatchRecord(t, p.Root, "T-05", event.StepWaiting, dispatchData{Level: "simple", Types: []string{"opus"},
		After: []string{"T-01"}}, at(5*time.Hour))
	dispatchRecord(t, p.Root, "T-05", event.StepDispatched, dispatchData{Lane: "O-01", Level: "simple",
		Types: []string{"opus"}, Type: "opus"}, at(4*time.Hour))
	dispatchRecord(t, p.Root, "T-06", event.StepDispatched, dispatchData{Lane: "O-01", Level: "hard",
		Types: []string{"opus:high"}, Type: "opus:high", Redo: true}, at(time.Hour))
	dispatchRecord(t, p.Root, "T-07", event.StepDispatched, dispatchData{Lane: "O-01", Level: "hard",
		Types: []string{"opus:high"}, Type: "opus:high"}, at(8*24*time.Hour))

	until := now.Add(time.Hour).Unix()
	ss := wire.Status{Since: now.Add(-2 * time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "proj", Root: p.Root,
			Agents: []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-rev", Agent: "claude", Status: "working", Age: 600}},
			Paused: &event.Pause{Why: "lunch", Until: until, Caller: "owner"}}}
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRO(store.File(p.Root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v := Build(Input{Status: ss, Lanes: checks.NewLaneTable(cfg), DB: db, Now: now, Events: 20})
	if len(v.Lanes) != 2 || len(v.Roles) == 0 || v.Paused == nil {
		t.Fatalf("precondition: two lanes, the roles and the pause: %+v", v)
	}
	s, err := Detail{Root: p.Root, Project: v}.Setup()
	if err != nil {
		t.Fatal(err)
	}

	// difficulty: each level's configured type references, in their order (an effort stays as written)
	wantDifficulty := []SetupLevel{{Level: "hard", Types: []string{"opus:high", "opus"}},
		{Level: "medium", Types: []string{"sonnet", "opus"}}, {Level: "simple", Types: []string{"opus"}}}
	if !reflect.DeepEqual(s.Difficulty, wantDifficulty) {
		t.Errorf("difficulty %+v, want %+v", s.Difficulty, wantDifficulty)
	}
	// lanes: the configured type (the pool lane's is ""), the configured agent and the state now
	lanes := map[string]SetupLane{}
	for _, l := range s.Lanes {
		lanes[l.Lane] = l
	}
	wantLanes := map[string]SetupLane{"O-01": {Lane: "O-01", Type: "opus", Agent: "pj-o-01", State: "working"},
		"L-01": {Lane: "L-01", Type: "", Agent: "pj-l-01", State: "free"}}
	if !reflect.DeepEqual(lanes, wantLanes) {
		t.Errorf("lanes %+v, want %+v", s.Lanes, wantLanes)
	}
	// roles: the non-optional ones always, a configured optional one, each with its type reference and pane status
	roles := map[string]SetupRole{}
	for _, r := range s.Roles {
		roles[r.Role] = r
	}
	if len(roles) != 4 {
		t.Errorf("roles %+v, want the lead, integrator, reviewer and deep dive", s.Roles)
	}
	if r := roles["reviewer"]; r.Agent != "pj-rev" || r.Type != "opus" || r.Status != "working" {
		t.Errorf("reviewer %+v, want pj-rev on opus, working", r)
	}
	if r := roles["deep_dive"]; r.Type != "opus:high" || r.Status != "" {
		t.Errorf("deep dive %+v, want opus:high and no pane", r)
	}
	if r, ok := roles["lead"]; !ok || r.Agent != "pj-lead" || r.Type != "" {
		t.Errorf("lead %+v, %v; want pj-lead with no type", r, ok)
	}
	if _, ok := roles["worker"]; ok {
		t.Errorf("roles %+v: the worker's rows are the lanes", s.Roles)
	}
	if s.Paused == nil || s.Paused.Reason != "lunch" || s.Paused.By != "owner" || s.Paused.Until == nil ||
		*s.Paused.Until != until {
		t.Errorf("pause %+v, want lunch until %d by the owner", s.Paused, until)
	}

	// distribution: level × chosen type, in level order, recent and all
	want := []DispatchCount{
		{Level: "hard", Type: "opus:high", Recent: 2, All: 3}, // the level dispatch, the redo, and the old one
		{Level: "medium", Type: "opus", Recent: 1, All: 1},    // before T-721: its lane's configured type
		{Level: "simple", Type: "opus", Recent: 1, All: 1},    // waited, then placed: counted once
		{Level: OverrideLevel, Type: "sonnet", Recent: 1, All: 1},
	}
	if !slices.Equal(s.Distribution, want) {
		t.Errorf("distribution %+v\nwant %+v", s.Distribution, want)
	}
	total := 0
	for _, c := range s.Distribution {
		total += c.All
	}
	if total != 6 {
		t.Errorf("all records count %d, want the 6 placed dispatches", total)
	}
}

// T-750: no store is no distribution and no config names none: Setup answers with empty lists, not nil, so the page
// has arrays to draw.
func TestSetupWithoutStore(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Detail{Root: p.Root, Project: Build(Input{Lanes: checks.NewLaneTable(cfg), Now: time.Now()})}.Setup()
	if err != nil {
		t.Fatal(err)
	}
	if s.Distribution == nil || len(s.Distribution) != 0 || s.Lanes == nil || s.Difficulty == nil {
		t.Errorf("setup %+v, want empty lists", s)
	}
}

// dispatchData is the fields of a fact dispatch or waiting this test writes (the app's own type is unexported).
type dispatchData struct {
	Lane  string   `json:"lane,omitempty"`
	Level string   `json:"level,omitempty"`
	Types []string `json:"types,omitempty"`
	Type  string   `json:"type,omitempty"`
	Redo  bool     `json:"redo,omitempty"`
	After []string `json:"after,omitempty"`
}

// dispatchRecord appends one fact record of task to the project's store at at, as serve writes it.
func dispatchRecord(t *testing.T, root, task, step string, data any, at time.Time) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, ?, ?)`,
		at.UTC().Format(time.RFC3339), task, step, event.KindFact, event.ClassFact, string(b)); err != nil {
		t.Fatal(err)
	}
}
