package view

import (
	"context"
	"encoding/json"
	"fmt"
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

// T-921 (RV-124 #1): a status from an older serve names no free lanes: every lane reads as locked, so the page warns
// before a lanes-count edit drops one; a free list naming a lane frees it and leaves the others locked.
func TestSetupReadsAnOlderServesLanesAsLocked(t *testing.T) {
	p := testkit.New(t)
	p.Lane("L-01", "task/T-01")
	p.Lane("L-02", "")
	p.Config()
	ask := func(body string) func(context.Context, string) (wire.Status, error) {
		var ss wire.Status
		if err := json.Unmarshal([]byte(body), &ss); err != nil {
			t.Fatal(err)
		}
		return func(context.Context, string) (wire.Status, error) { return ss, nil }
	}
	older, err := reading{ask: ask(fmt.Sprintf(`{"project":{"name":"proj","root":%q}}`, p.Root))}.
		setup(context.Background(), p.Root)
	if err != nil {
		t.Fatal(err)
	}
	want := []SetupLane{{Lane: "L-01", Agent: "pj-l-01", State: "free", Locked: true},
		{Lane: "L-02", Agent: "pj-l-02", State: "free", Locked: true}}
	if !reflect.DeepEqual(older.Lanes, want) {
		t.Errorf("no free field: lanes %+v, want %+v (every lane locked)", older.Lanes, want)
	}

	newer, err := reading{ask: ask(fmt.Sprintf(`{"project":{"name":"proj","root":%q,"free":["L-01"]}}`, p.Root))}.
		setup(context.Background(), p.Root)
	if err != nil {
		t.Fatal(err)
	}
	want = []SetupLane{{Lane: "L-01", Agent: "pj-l-01", State: "free"},
		{Lane: "L-02", Agent: "pj-l-02", State: "free", Locked: true}}
	if !reflect.DeepEqual(newer.Lanes, want) {
		t.Errorf("free L-01: lanes %+v, want %+v", newer.Lanes, want)
	}
}

// T-750, T-765, T-766: Setup is the project's steering — the difficulty levels in configured order with what runs each
// model ref (kind, model id, the ref's effort), then the roles' refs no level names, every model the config knows with
// its pause in force, every agent kind with its args and whether it has a prompt_extra, the lanes
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
		testkit.Set("kinds", map[string]any{"codex": map[string]any{"args": []any{"--search"}},
			"opencode": map[string]any{"prompt_extra": "quote summaries only"}}),
	)
	p.Commit(l1)
	p.State("L-01", map[string]any{"task": "T-01", "branch": "task/T-01"}) // its row holds T-01, not landed: working
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
			Agents:      []wire.AgentStatus{{Pane: "w1:p1", Name: "pj-rev", Agent: "claude", Status: "working", Age: 600}},
			Paused:      &event.Pause{Why: "lunch", Until: until, Caller: "owner"},
			Free:        []string{"L-02"},
			ModelPauses: map[string]*event.Pause{"sonnet": {Why: "quota", Caller: "pj-lead"}}}}
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Lanes()) != 2 || ss.Project == nil || ss.Project.Paused == nil {
		t.Fatalf("precondition: two lanes and the pause: %+v", cfg.Lanes())
	}
	s, err := reading{ask: func(context.Context, string) (wire.Status, error) { return ss, nil }}.
		setup(context.Background(), p.Root)
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
	// a model's pause in force (T-766), none on the others
	if m := byName["sonnet"]; m.Paused == nil || m.Paused.Reason != "quota" || m.Paused.By != "pj-lead" || m.Paused.Until != nil {
		t.Errorf("model sonnet's pause %+v, want quota by pj-lead until cleared", m.Paused)
	}
	if m := byName["opus"]; m.Paused != nil {
		t.Errorf("model opus paused %+v, want none", m.Paused)
	}
	// kinds (T-766): every agent kind, its args and whether a prompt_extra is set
	wantKinds := []SetupKind{{Name: "claude", Args: []string{}}, {Name: "codex", Args: []string{"--search"}},
		{Name: "opencode", Args: []string{}, PromptExtra: true}}
	if !reflect.DeepEqual(s.Kinds, wantKinds) {
		t.Errorf("kinds %+v, want %+v", s.Kinds, wantKinds)
	}
	// lanes: L-01 … L-NN, their agents and states now (no record names what runs: no model), and serve's locked flag
	wantLanes := []SetupLane{{Lane: "L-01", Agent: "pj-l-01", State: "working", Locked: true},
		{Lane: "L-02", Agent: "pj-l-02", State: "free"}}
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

// T-750: no store is no distribution and no config names none: Setup answers with empty lists, not nil, so the page
// has arrays to draw.
func TestSetupWithoutStore(t *testing.T) {
	p := testkit.New(t)
	p.Config()
	s, err := reading{}.setup(context.Background(), p.Root)
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

// T-801: a custom role and a kit default row the config names are role rows of the view (obor status, obor web) and of
// the Setup page with their model refs, like any other role; an unnamed default row is neither; a removed row drops
// out of both and leaves the others.
func TestCustomRolesListed(t *testing.T) {
	p := testkit.New(t)
	p.Write("docs/agents/designer.md", "# Role: designer\n")
	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.researcher", "sonnet:medium"),
		testkit.Set("roles.designer", map[string]any{"model": "opus:high", "write": []any{"docs/design/"}}))
	now := time.Now()
	ss := wire.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true,
		Project: &wire.ProjectStatus{Name: "proj", Root: p.Root,
			Agents: []wire.AgentStatus{{Pane: "w1:p4", Name: "pj-designer", Agent: "claude", Status: "idle", Age: 60}}}}
	rows := func() (map[string]Role, map[string]SetupRole) {
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		v := Build(Input{Status: ss, Lanes: checks.NewLaneTable(context.Background(), cfg), Now: now})
		s, err := reading{ask: func(context.Context, string) (wire.Status, error) { return ss, nil }}.
			setup(context.Background(), p.Root)
		if err != nil {
			t.Fatal(err)
		}
		view, setup := map[string]Role{}, map[string]SetupRole{}
		for _, r := range v.Roles {
			view[r.Role] = r
		}
		for _, r := range s.Roles {
			setup[r.Role] = r
		}
		return view, setup
	}
	view, setup := rows()
	if r, ok := view["designer"]; !ok || r.Agent != "pj-designer" || r.Status != "idle" {
		t.Errorf("view designer %+v, %v; want pj-designer idle", r, ok)
	}
	if r, ok := view["researcher"]; !ok || r.Agent != "pj-researcher" {
		t.Errorf("view researcher %+v, %v; want pj-researcher", r, ok)
	}
	for _, name := range []string{"advisor", "triage", "lead", "worker"} {
		if _, ok := view[name]; ok {
			t.Errorf("view lists %s (roles %+v)", name, view)
		}
	}
	if r, ok := setup["designer"]; !ok || r.Agent != "pj-designer" || r.Type != "opus:high" || r.Status != "idle" {
		t.Errorf("setup designer %+v, %v; want pj-designer on opus:high, idle", r, ok)
	}
	if r, ok := setup["researcher"]; !ok || r.Type != "sonnet:medium" {
		t.Errorf("setup researcher %+v, %v; want sonnet:medium", r, ok)
	}
	if _, ok := setup["advisor"]; ok {
		t.Errorf("setup lists the unnamed advisor: %+v", setup)
	}

	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.researcher", "sonnet:medium"))
	view, setup = rows()
	if _, ok := view["designer"]; ok {
		t.Errorf("view still lists the removed designer: %+v", view)
	}
	if _, ok := setup["designer"]; ok {
		t.Errorf("setup still lists the removed designer: %+v", setup)
	}
	if _, ok := view["researcher"]; !ok {
		t.Errorf("view lost the researcher with the designer's row: %+v", view)
	}
}
