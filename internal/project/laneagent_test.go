package project

import (
	"os"
	"path/filepath"
	"testing"

	"obor/internal/config"
	"obor/internal/store"
)

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
