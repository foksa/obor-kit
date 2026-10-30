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

// The lane row's agent is its first alias and known to AgentLookup; a row written later is seen at once.
func TestLaneAgentReadsTheRow(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"prefix":"pj","lanes_dir":"../wt",
		"lane_types":{"opus":{"kind":"claude"}},"lanes":{"O-01":"opus"}}`), 0o644)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.LaneAgentAliases("O-01", LaneAgent(root, "O-01")); !reflect.DeepEqual(got, []string{"pj-o-01", "pj-o1"}) {
		t.Errorf("no database: %q", got)
	}
	if _, ok := AgentLookup(cfg, "bob", ""); ok {
		t.Error("bob known before the row names it")
	}
	writeRows(t, root, map[string]string{"O-01": "bob"})
	if got := cfg.LaneAgentAliases("O-01", LaneAgent(root, "O-01")); !reflect.DeepEqual(got, []string{"bob", "pj-o-01", "pj-o1"}) {
		t.Errorf("row: %q", got)
	}
	if got, ok := AgentLookup(cfg, "bob", ""); !ok || got.Role != "worker" || got.Lane != "O-01" {
		t.Errorf("AgentLookup(bob) = %+v, %v", got, ok)
	}
}

// T-302: a lane's agent is found by its real names only, never by the bare lowercase lane name (another project's
// agent may be named so); a row naming the bare name makes it a real name.
func TestAgentLookupTakesNoBareLaneName(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(`{"prefix":"pj","lanes_dir":"../wt",
		"lane_types":{"opus":{"kind":"claude"}},"lanes":{"O-01":"opus","O-02":"opus"}}`), 0o644)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	writeRows(t, root, map[string]string{"O-01": "o-01", "O-02": "pj-o-02"})
	if got, ok := AgentLookup(cfg, "pj-o-02", ""); !ok || got.Lane != "O-02" {
		t.Errorf("AgentLookup(pj-o-02) = %+v, %v; want lane O-02", got, ok)
	}
	if got, ok := AgentLookup(cfg, "o-02", ""); ok {
		t.Errorf("AgentLookup(o-02) = %+v; want unknown (bare lane name)", got)
	}
	if names := cfg.LaneAgentAliases("O-02", LaneAgent(root, "O-02")); slices.Contains(names, "o-02") {
		t.Errorf("LaneAgentAliases(O-02) = %q has the bare lane name", names)
	}
	if got, ok := AgentLookup(cfg, "o-01", ""); !ok || got.Lane != "O-01" {
		t.Errorf("AgentLookup(o-01) = %+v, %v; want lane O-01 (its row's agent)", got, ok)
	}
}
