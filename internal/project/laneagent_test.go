package project

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"obor/internal/config"
	"obor/internal/store"
)

// writeRows replaces root's lanes table with one row per lane naming agent (what serve's projection writes).
func writeRows(t *testing.T, root string, agents map[string]string) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows []store.LaneRow
	for lane, agent := range agents {
		rows = append(rows, store.LaneRow{Lane: lane, Task: "T-01", Agent: agent})
	}
	if err := store.WriteLanes(db, rows); err != nil {
		t.Fatal(err)
	}
}

// T-892: LaneModel is the one answer to "which model does this lane run now": the row's Type (effort kept) when the
// row holds a task, also on a free lane whose row keeps its last task; no row or no task names none. RunningKind is
// that model's kind, else agents.Default; an agent with no lane uses its own type.
func TestLaneModelAndRunningKind(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := `{"prefix":"pj","lanes_dir":"../wt","lanes":2,` +
		`"models":{"ds":{"kind":"opencode","model":"go/deepseek"}}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteLanes(db, []store.LaneRow{
		{Lane: "L-01", Task: "T-01", Type: "opus"},                 // an open dispatch
		{Lane: "L-02", Task: "T-02", Type: "ds:high", Finished: 9}, // a free lane keeping its last task
		{Lane: "L-08", Task: "T-08"},                               // a dispatch recorded before T-721 names no type
		{Lane: "L-09", Type: "ds"},                                 // a row with no task is no open dispatch
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for _, c := range []struct {
		name, lane, typ string // the agent's lane and its own model (no lane: a role's)
		ref             string // LaneModel's answer
		ok              bool   // whether it names one
		kind            string // RunningKind's answer
	}{
		{"a row with a task", "L-01", "", "opus", true, "claude"},
		{"a free lane keeping its last task", "L-02", "", "ds:high", true, "opencode"},
		{"no row", "L-03", "", "", false, "claude"},
		{"no row, the agent's own type", "", "ds", "", false, "opencode"},
		{"a row with a task and no type", "L-08", "", "", false, "claude"},
		{"a row with no task", "L-09", "", "", false, "claude"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if ref, ok := LaneModel(cfg, c.lane); ref != c.ref || ok != c.ok {
				t.Errorf("LaneModel(%q) = %q, %v; want %q, %v", c.lane, ref, ok, c.ref, c.ok)
			}
			if got := RunningKind(cfg, config.AgentInfo{Lane: c.lane, Type: c.typ}); got != c.kind {
				t.Errorf("RunningKind(lane %q, type %q) = %q; want %q", c.lane, c.typ, got, c.kind)
			}
		})
	}
}

// T-302: a lane's agent is found by its real names only, never by the bare lowercase lane name (another project's
// agent may be named so); a row naming the bare name makes it a real name. The lane row's agent is its first alias and
// known to AgentLookup; a row written later is seen at once.
func TestAgentLookupTakesNoBareLaneName(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"prefix":"pj","lanes_dir":"../wt","lanes":2}`), 0o644)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// No database yet: the derived name alone; the row's agent is not known until the row names it.
	if got := cfg.LaneAgentAliases("L-01", LaneAgent(root, "L-01")); !reflect.DeepEqual(got, []string{"pj-l-01"}) {
		t.Errorf("no database: %q", got)
	}
	if _, ok := AgentLookup(cfg, "l-01", ""); ok {
		t.Error("l-01 known before the row names it")
	}
	writeRows(t, root, map[string]string{"L-01": "l-01", "L-02": "pj-l-02"})
	if got := cfg.LaneAgentAliases("L-01", LaneAgent(root, "L-01")); !reflect.DeepEqual(got, []string{"l-01", "pj-l-01"}) {
		t.Errorf("row: %q, want its agent then the derived name", got)
	}
	if got, ok := AgentLookup(cfg, "pj-l-02", ""); !ok || got.Lane != "L-02" {
		t.Errorf("AgentLookup(pj-l-02) = %+v, %v; want lane L-02", got, ok)
	}
	if got, ok := AgentLookup(cfg, "l-02", ""); ok {
		t.Errorf("AgentLookup(l-02) = %+v; want unknown (bare lane name)", got)
	}
	if names := cfg.LaneAgentAliases("L-02", LaneAgent(root, "L-02")); slices.Contains(names, "l-02") {
		t.Errorf("LaneAgentAliases(L-02) = %q has the bare lane name", names)
	}
	if got, ok := AgentLookup(cfg, "l-01", ""); !ok || got.Lane != "L-01" {
		t.Errorf("AgentLookup(l-01) = %+v, %v; want lane L-01 (its row's agent)", got, ok)
	}
}
