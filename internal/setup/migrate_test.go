package setup

import (
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/testkit"
)

// oldShapeConfig is a config.json of the shape before T-765: lane types, lanes O-01 (typed) and O-02 (pooled), role
// objects.
const oldShapeConfig = `{
  "version": 1,
  "name": "proj",
  "prefix": "pj",
  "lanes_dir": "../proj-wt",
  "lane_types": {
    "opus": {"kind": "claude", "args": ["--model", "opus", "--effort", "high", "--permission-mode", "auto"], "max": 2}
  },
  "lanes": {"O-01": "opus", "O-02": ""},
  "roles": {"integrator": {"type": "opus"}, "lead": {"type": "opus", "handoff_tokens": 400000}}
}
`

// migrateFx is a project with the old-shape config committed on main, lane worktrees O-01 and O-02 detached at main
// (free), and a pane labelled with O-01's agent.
func migrateFx(t *testing.T) *fx {
	t.Helper()
	x := bare(t)
	x.Lane("O-01", "")
	x.Lane("O-02", "")
	x.Write(".obor/config.json", oldShapeConfig)
	x.Git(x.Root, "add", ".obor/config.json")
	x.Git(x.Root, "commit", "-q", "-m", "old config", "--", ".obor/config.json")
	x.h.Pane("w1:p1").Label = "pj-o-01"
	return x
}

// T-765: obor setup migrate rewrites the old shape once: lanes not named L-NN are moved to L-01 … (git worktree move),
// their panes relabelled, the new config written (it loads), printed beside the old and committed.
func TestSetupMigrateMovesLanesAndCommits(t *testing.T) {
	t.Parallel()
	x := migrateFx(t)
	if _, err := config.Load(x.Root); err == nil {
		t.Fatal("precondition: the old shape loads")
	}
	head := x.Git(x.Root, "rev-parse", "HEAD")
	out := x.ok(t, "migrate")
	for _, want := range []string{"old .obor/config.json:\n" + strings.TrimRight(oldShapeConfig, "\n"),
		"new .obor/config.json:", "lane O-01 → L-01 (git worktree move)", "lane O-02 → L-02 (git worktree move)",
		"agent pj-o-01 → pj-l-01", "pane w1:p1 pj-o-01 → pj-l-01", "dropped: lane_types.opus.max 2: dropped (no caps)",
		"OK migrate: committed .obor/config.json"} {
		has(t, out, want, "output")
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatalf("the migrated config does not load: %v", err)
	}
	if cfg.Data.Lanes != 2 || strings.Join(cfg.DifficultyList("hard"), ",") != "opus:high" {
		t.Errorf("lanes %d, hard %q", cfg.Data.Lanes, cfg.DifficultyList("hard"))
	}
	if rc, _ := cfg.Role("lead"); rc.Type != "opus" || rc.Effort != "high" || cfg.Data.HandoffTokens == nil || *cfg.Data.HandoffTokens != 400000 {
		t.Errorf("lead %+v, handoff %v", rc, cfg.Data.HandoffTokens)
	}
	for _, l := range []string{"L-01", "L-02"} {
		if !x.Exists(x.LaneDir(l) + "/.git") {
			t.Errorf("no worktree %s", l)
		}
	}
	for _, l := range []string{"O-01", "O-02", "O-01.migrating"} {
		if x.Exists(x.LaneDir(l)) {
			t.Errorf("worktree %s left", l)
		}
	}
	if !strings.Contains(x.Git(x.Root, "worktree", "list"), x.LaneDir("L-01")) {
		t.Errorf("git does not know L-01:\n%s", x.Git(x.Root, "worktree", "list"))
	}
	if got := x.h.Pane("w1:p1").Label; got != "pj-l-01" {
		t.Errorf("pane label %q, want pj-l-01", got)
	}
	if x.Git(x.Root, "rev-parse", "HEAD^") != head || x.Git(x.Root, "status", "--porcelain", "--", ".obor/config.json") != "" {
		t.Errorf("config.json not committed on top of %s", head)
	}
	has(t, x.reqs()[len(x.reqs())-1], "pane.rename label=pj-l-01 pane_id=w1:p1", "relabelled through herdr")
	// once is enough: a rerun on the new shape says so and changes nothing.
	again := x.Git(x.Root, "rev-parse", "HEAD")
	if out := x.ok(t, "migrate"); last(out) != "OK migrate: .obor/config.json has the new shape (models, a lanes count) (already)" {
		t.Errorf("rerun: %q", last(out))
	}
	if x.Git(x.Root, "rev-parse", "HEAD") != again {
		t.Error("the rerun committed")
	}
}

// T-765: lanes to rename while one is busy (uncommitted changes, or an agent running in it): refused, nothing moved,
// written or committed.
func TestSetupMigrateRefusesABusyLane(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		busy func(x *fx)
		why  string
	}{
		{"uncommitted changes", func(x *fx) { x.Write(x.LaneDir("O-01")+"/wip.txt", "wip\n") }, "O-01 (uncommitted changes)"},
		{"an agent runs in it", func(x *fx) { x.running([2]string{"pj-o-02", x.LaneDir("O-02")}) }, "O-02 (agent pj-o-02 runs in it)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := migrateFx(t)
			c.busy(x)
			head := x.Git(x.Root, "rev-parse", "HEAD")
			out, rc := x.run(t, "migrate")
			if rc != 1 || !strings.Contains(last(out), "every lane must be free: "+c.why) {
				t.Fatalf("migrate: exit %d, %q; want refused naming %s", rc, last(out), c.why)
			}
			if x.Read(".obor/config.json") != oldShapeConfig || x.Git(x.Root, "rev-parse", "HEAD") != head {
				t.Error("the refused migrate wrote or committed the config")
			}
			if !x.Exists(x.LaneDir("O-01")) || !x.Exists(x.LaneDir("O-02")) || x.Exists(x.LaneDir("L-01")) {
				t.Error("the refused migrate moved a lane")
			}
		})
	}
}

// T-765: a config of the new shape needs no migration: OK (already), nothing touched.
func TestSetupMigrateNewShapeIsAlready(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lanes", 1))
	before, head := x.Read(".obor/config.json"), x.Git(x.Root, "rev-parse", "HEAD")
	if out := x.ok(t, "migrate"); last(out) != "OK migrate: .obor/config.json has the new shape (models, a lanes count) (already)" {
		t.Errorf("migrate: %q", last(out))
	}
	if x.Read(".obor/config.json") != before || x.Git(x.Root, "rev-parse", "HEAD") != head {
		t.Error("migrate changed a new-shape config")
	}
	x.failing(t, "FAIL "+Usage(), "migrate", "--force")
}

// T-765: only local.json has the old shape (a machine's roles.advisor object, as obor-kit's own after its config.json
// was migrated): local.json is rewritten, config.json stays byte for byte and nothing is committed.
func TestSetupMigrateLocalOnly(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lanes", 1), testkit.Set("roles.integrator", "opus"))
	x.Write(".obor/local.json", `{"roles": {"advisor": {"type": "fable", "agent": "pj-fable"}}}`+"\n")
	if _, err := config.Load(x.Root); err == nil {
		t.Fatal("precondition: the old local.json does not load")
	}
	before, head := x.Read(".obor/config.json"), x.Git(x.Root, "rev-parse", "HEAD")
	out := x.ok(t, "migrate")
	if last(out) != "OK migrate: .obor/local.json rewritten (not committed): obor config validate" {
		t.Errorf("migrate: %q", last(out))
	}
	if x.Read(".obor/config.json") != before || x.Git(x.Root, "rev-parse", "HEAD") != head {
		t.Error("migrate rewrote or committed config.json")
	}
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatalf("the migrated local.json does not load: %v", err)
	}
	if rc, _ := cfg.Role("advisor"); rc.Type != "fable" || rc.Agent != "pj-advisor" {
		t.Errorf("advisor = %+v, want fable as pj-advisor", rc)
	}
	has(t, out, "agent pj-fable → pj-advisor", "the renamed agent said")
}
