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

// T-750, T-765: Setup is the project's steering — the difficulty levels in configured order with what runs each model
// ref (kind, model id, the ref's effort), then the roles' refs no level names, every model the config knows, the lanes
// with their states, the roles the config names with their statuses and the pause — and how dispatches spread by
// level and chosen model, in the last 7 days and in all records: a level dispatch, an override (--model, no level), a
// record from before T-721 (unknown: every lane is pooled), a waiting dispatch that is placed later (once), a redo
// (again) and a record older than 7 days (all only).
func TestSetup(t *testing.T) {
	p := testkit.New(t)
	l1 := p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "") // detached at main: free
	p.Config(
		testkit.Set("models.kimi", map[string]any{"kind": "opencode", "model": "opencode-go/kimi"}),
		testkit.Set("difficulty", map[string]any{"hard": []any{"opus:high", "opus"},
			"medium": []any{"sonnet", "kimi:high"}, "simple": []any{"opus"}}),
		testkit.Set("roles.reviewer", "opus"),
		testkit.Set("roles.deep_dive", "deepseek"),
	)
	p.Commit(l1) // L-01's task branch has a commit: working
	now := time.Now()
	at := func(ago time.Duration) time.Time { return now.Add(-ago) }
	// the level dispatch, the override, the pre-T-721 record, the waiting one and the dispatch that places it, a redo
	// and one older than the window
	dispatchRecord(t, p.Root, "T-02", event.StepDispatched, dispatchData{Lane: "L-01", Level: "hard",
		Types: []string{"opus:high"}, Type: "opus:high"}, at(2*time.Hour))
	dispatchRecord(t, p.Root, "T-03", event.StepDispatched, dispatchData{Lane: "L-01", Types: []string{"sonnet"},
		Type: "sonnet"}, at(3*time.Hour))
	dispatchRecord(t, p.Root, "T-04", event.StepDispatched, dispatchData{Lane: "L-01", Level: "medium",
		Types: []string{"sonnet", "opus"}}, at(4*time.Hour)) // before T-721: no Type, unknown
	dispatchRecord(t, p.Root, "T-05", event.StepWaiting, dispatchData{Level: "simple", Types: []string{"opus"},
		After: []string{"T-01"}}, at(5*time.Hour))
	dispatchRecord(t, p.Root, "T-05", event.StepDispatched, dispatchData{Lane: "L-01", Level: "simple",
		Types: []string{"opus"}, Type: "opus"}, at(4*time.Hour))
	dispatchRecord(t, p.Root, "T-06", event.StepDispatched, dispatchData{Lane: "L-01", Level: "hard",
		Types: []string{"opus:high"}, Type: "opus:high", Redo: true}, at(time.Hour))
	dispatchRecord(t, p.Root, "T-07", event.StepDispatched, dispatchData{Lane: "L-01", Level: "hard",
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

	// difficulty: each level's configured model refs, in their order (an effort stays as written)
	wantDifficulty := []SetupLevel{{Level: "hard", Types: []string{"opus:high", "opus"}},
		{Level: "medium", Types: []string{"sonnet", "kimi:high"}}, {Level: "simple", Types: []string{"opus"}}}
	if !reflect.DeepEqual(s.Difficulty, wantDifficulty) {
		t.Errorf("difficulty %+v, want %+v", s.Difficulty, wantDifficulty)
	}
	// types: each level's ref with what runs it (kind, model id, the ref's effort), then a role's ref no level names
	wantTypes := []SetupType{
		{Ref: "opus:high", Name: "opus", Kind: "claude", Model: "opus", Effort: "high"},
		{Ref: "opus", Name: "opus", Kind: "claude", Model: "opus"},
		{Ref: "sonnet", Name: "sonnet", Kind: "claude", Model: "sonnet"},
		{Ref: "kimi:high", Name: "kimi", Kind: "opencode", Model: "opencode-go/kimi", Effort: "high"},
		{Ref: "deepseek", Name: "deepseek", Kind: "opencode", Model: "opencode-go/deepseek-v4.1-flash"},
	}
	if !reflect.DeepEqual(s.Types, wantTypes) {
		t.Errorf("types %+v\nwant %+v", s.Types, wantTypes)
	}
	// models: every model the config knows, built-in ones first, with its kind, model id and its kind's efforts
	var names []string
	byName := map[string]SetupModel{}
	for _, m := range s.Models {
		names = append(names, m.Name)
		byName[m.Name] = m
	}
	if !slices.Equal(names, cfg.ModelNames()) || names[len(names)-1] != "kimi" {
		t.Fatalf("models %q, want the config's %q", names, cfg.ModelNames())
	}
	if m := byName["opus"]; m.Kind != "claude" || m.Model != "opus" || !m.Builtin || !slices.Contains(m.Efforts, "high") {
		t.Errorf("model opus %+v, want a built-in claude opus with claude's efforts", m)
	}
	if m := byName["kimi"]; m.Kind != "opencode" || m.Model != "opencode-go/kimi" || m.Builtin {
		t.Errorf("model kimi %+v, want the project's opencode kimi", m)
	}
	// lanes: L-01 … L-NN, their agents and states now (no record names what runs: no model)
	wantLanes := []SetupLane{{Lane: "L-01", Agent: "pj-l-01", State: "working"}, {Lane: "L-02", Agent: "pj-l-02", State: "free"}}
	if !reflect.DeepEqual(s.Lanes, wantLanes) {
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
	if r := roles["deep_dive"]; r.Type != "deepseek" || r.Status != "" {
		t.Errorf("deep dive %+v, want deepseek and no pane", r)
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
		{Level: "medium", Type: "", Recent: 1, All: 1},        // before T-721 on a configured lane: unknown
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

// T-752: a dispatch from before T-721 takes the type of the lane it ran, from the store's own records of that lane
// when the config no longer names it — the type a dispatch record of the lane names, or the one its lane row records
// — and is unknown ("") when no record names one; on a lane the config names it is unknown (every lane is pooled).
func TestSetupDistributionTypesALaneGoneFromTheConfig(t *testing.T) {
	p := testkit.New(t)
	p.Lane("L-01", "") // the one configured lane
	p.Config()
	// G-01 and G-02 are lanes gone from the config, each with a record that names its type; G-03 has none
	dispatchRecord(t, p.Root, "T-11", event.StepDispatched, dispatchData{Lane: "G-01", Level: "medium",
		Types: []string{"opus"}, Type: "opus"}, time.Now().Add(-time.Hour))
	p.State("G-02", map[string]any{"task": "T-12", "branch": "task/T-12", "type": "sonnet"})
	now := time.Now()
	// the records from before T-721: on G-01 and G-02 (their lanes' types), G-03 (nothing names one) and the pool lane
	dispatchRecord(t, p.Root, "T-01", event.StepDispatched, dispatchData{Lane: "G-01"}, now.Add(-2*time.Hour))
	dispatchRecord(t, p.Root, "T-02", event.StepDispatched, dispatchData{Lane: "G-02", Level: "simple"},
		now.Add(-3*time.Hour))
	dispatchRecord(t, p.Root, "T-03", event.StepDispatched, dispatchData{Lane: "G-03", Level: "simple"},
		now.Add(-4*time.Hour))
	dispatchRecord(t, p.Root, "T-04", event.StepDispatched, dispatchData{Lane: "L-01"}, now.Add(-5*time.Hour))
	// preconditions: the config names only the pool lane, G-02's row names sonnet, G-03 has no row at all
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if ls := cfg.Lanes(); len(ls) != 1 || ls[0].Name != "L-01" {
		t.Fatalf("precondition: one configured lane, L-01: %+v", ls)
	}
	if row, ok := p.Row("G-02"); !ok || row.Type != "sonnet" {
		t.Fatalf("precondition: G-02's lane row names sonnet: %+v (%v)", row, ok)
	}
	if _, ok := p.Row("G-03"); ok {
		t.Fatal("precondition: G-03 has no lane row")
	}
	s, err := Detail{Root: p.Root, Project: Build(Input{Lanes: checks.NewLaneTable(cfg), Now: now})}.Setup()
	if err != nil {
		t.Fatal(err)
	}
	want := []DispatchCount{
		{Level: "medium", Type: "opus", Recent: 1, All: 1},   // its own dispatch record
		{Level: "simple", Type: "", Recent: 1, All: 1},       // G-03: no record names a type: unknown
		{Level: "simple", Type: "sonnet", Recent: 1, All: 1}, // G-02: its lane row
		{Level: "", Type: "", Recent: 1, All: 1},             // the configured L-01: unknown
		{Level: "", Type: "opus", Recent: 1, All: 1},         // G-01: the type a record of its lane names
	}
	if !slices.Equal(s.Distribution, want) {
		t.Errorf("distribution %+v\nwant %+v", s.Distribution, want)
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
	if s.Distribution == nil || len(s.Distribution) != 0 || s.Lanes == nil || s.Difficulty == nil || s.Types == nil {
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
