// plugin_test.go: obor status's no-plugin warning (T-307) for an opencode lane, and when it waits for the plugin to
// check in (T-783): its marker is written on the agent session's first event, which comes after the agent started.
package view

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// pluginRig is a project at a temp root whose store holds lane L-01's row on T-1 with type ds (a model of kind
// opencode) dispatched at dispatchedAt, and whose plugin marker for the lane says at (0: none). The lane table's clock
// is now, and the lane's row is s.
func pluginRig(t *testing.T, dispatchedAt, at int64, now time.Time) (*checks.LaneTable, port.Lane) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgFile := filepath.Join(root, ".obor", "config.json")
	if err := os.WriteFile(cfgFile, []byte(`{"name":"pj","prefix":"pj","lanes":1,`+
		`"models":{"ds":{"kind":"opencode","model":"opencode-go/deepseek"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteLanes(db, []store.LaneRow{{Lane: "L-01", Task: "T-1", Agent: "pj-l-01", Branch: "task/T-1",
		DispatchedAt: dispatchedAt, Type: "ds"}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if at != 0 {
		marker := project.PluginMarkerFile(root, "L-01")
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte(`{"kind":"opencode","at":`+strconv.FormatInt(at, 10)+"}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	lp := checks.NewLaneTable(context.Background(), cfg)
	lp.Now = now.Unix()
	return lp, port.Lane{Name: "L-01", Type: "ds", Dir: filepath.Join(root, "..", "pj-wt", "L-01"),
		State: "working", Task: "T-1"}
}

// T-783: obor status reads the start serve last tried (T-893: serve's TaskStatus.Started, its fold of the agent.start
// records) into the warning: a lane whose agent was restarted and resumed 30 s ago has no no-plugin warning yet, though
// its marker is a dispatch old; the same lane ten minutes after that start, its hook still not run, is warned.
func TestBuildNoPluginWaitsForTheLastStart(t *testing.T) {
	for _, c := range []struct {
		name    string
		started int64 // serve's last start of the lane's agent, seconds ago
		want    string
	}{
		{"the resumed start 30 s ago", 30, ""},
		{"the start 10 min ago, no hook since", 600, "no-plugin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := testkit.New(t)
			o1 := p.Lane("L-01", "task/T-01")
			p.Config(testkit.Set("models.ds", `{"kind":"opencode","model":"opencode-go/deepseek"}`))
			p.Commit(o1)
			p.Dispatched("L-01")
			p.State("L-01", map[string]any{"lane": "L-01", "task": "T-01", "branch": "task/T-01", "agent": "pj-l-01",
				"type": "ds", "dispatched_at": time.Now().Add(-20 * time.Minute).Unix()})
			marker := project.PluginMarkerFile(p.Root, "L-01")
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			// The marker of the session before the dispatch: what the restarted agent's plugin has not replaced yet.
			at := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
			if err := os.WriteFile(marker, []byte(`{"kind":"opencode","at":`+at+"}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			started := time.Now().Add(-time.Duration(c.started) * time.Second).Unix()
			ask := func(context.Context, string) (wire.Status, error) {
				return wire.Status{Connected: true, Project: &wire.ProjectStatus{Name: "pj", Root: p.Root,
					Tasks: []wire.TaskStatus{{Task: "T-01", Lane: "L-01", Attempt: 1, State: "dispatched",
						Phase: wire.PhaseWorking, Started: started}}}}, nil
			}
			v, err := reading{ask: ask}.load(context.Background(), p.Root, time.Now(), 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(v.Lanes) != 1 || v.Lanes[0].State != "working" {
				t.Fatalf("precondition: lanes %+v, want L-01 working", v.Lanes)
			}
			got := v.Lanes[0].Warning
			if got != c.want {
				t.Errorf("warning %q, want %q", got, c.want)
			}
		})
	}
}

// T-783: a lane whose agent was (re)started after its dispatch — obor restart, then obor resume retrying the start —
// has a marker older than the dispatch until the agent's session's first event writes a new one, so the warning waits
// pluginSettle after that start; a start that never checks in is the missing plugin.
func TestPluginMissingWaitsForTheStartToCheckIn(t *testing.T) {
	now := time.Date(2026, 11, 4, 7, 25, 0, 0, time.UTC)
	disp := now.Add(-20 * time.Minute).Unix()
	for _, c := range []struct {
		name, why string
		at        int64 // the marker's at
		start     int64 // the last agent.start record (0: none)
		want      bool
	}{
		{"a start 30 s ago may still check in", "the marker is one dispatch behind, the restart's session has not begun",
			disp - 60, now.Add(-30 * time.Second).Unix(), false},
		{"a start 10 min ago that never checked in", "no hook ran for the start", disp - 60,
			now.Add(-10 * time.Minute).Unix(), true},
		{"no start record since the dispatch", "the dispatch is the only attempt", disp - 60, 0, true},
		{"a marker newer than the dispatch", "", now.Add(-time.Minute).Unix(), now.Add(-10 * time.Minute).Unix(), false},
		{"no marker, a start 10 min ago", "the plugin never loaded", 0, now.Add(-10 * time.Minute).Unix(), true},
		{"no marker, a start 20 s ago", "the plugin may still load", 0, now.Add(-20 * time.Second).Unix(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			lp, s := pluginRig(t, disp, c.at, now)
			got := pluginMissing(lp, s, map[string]int64{"T-1": c.start})
			if got != c.want {
				t.Errorf("pluginMissing with %s = %v, want %v", c.why, got, c.want)
			}
		})
	}
}
