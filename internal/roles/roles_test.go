package roles

import (
	"reflect"
	"slices"
	"testing"
)

func TestRoleTableDefaultsAndProfiles(t *testing.T) {
	// T-801: advisor, researcher and triage are the kit's default custom rows, not table roles.
	want := []string{"lead", "integrator", "worker", "reviewer", "deep_dive", "driver"}
	all := All()
	if len(all) != len(want) {
		t.Fatalf("role count = %d, want %d", len(all), len(want))
	}
	for i, name := range want {
		if all[i].Name != name {
			t.Errorf("role %d = %q, want %q", i, all[i].Name, name)
		}
	}
	for _, role := range all {
		if got, ok := Lookup(role.Name); !ok || !reflect.DeepEqual(got, role) {
			t.Errorf("Lookup(%q) = %+v, %v", role.Name, got, ok)
		}
		if role.EnvValue != "" {
			if got, ok := LookupEnv(role.EnvValue); !ok || !reflect.DeepEqual(got, role) {
				t.Errorf("LookupEnv(%q) = %+v, %v", role.EnvValue, got, ok)
			}
		}
	}
	checks := []struct {
		name         string
		env          string
		agent        string
		cwd          CwdRule
		write        WriteProfile
		stoppable    bool
		dispatchable bool
		optional     bool
	}{
		{"lead", "lead", "pj-lead", CwdProject, WriteAll, false, false, false},
		{"integrator", "integrator", "pj-int", CwdRoleWorktree, WriteNone, false, false, false}, // T-853
		{"worker", "worker", "pj-l-01", CwdLane, WriteSpec, true, true, false},
		{"reviewer", "reviewer", "pj-rev", CwdRoleWorktree, WriteReviewsOnly, false, false, false}, // T-853
		{"deep_dive", "deep_dive", "pj-deep-dive", CwdRoleWorktree, WriteReviewsOnly, false, false, true},
		// Optional table roles count only when configured (T-751): the driver is never configured, obor drive starts it.
		{"driver", "driver", "pj-drive", CwdLane, WriteSpec, false, false, true},
	}
	for _, check := range checks {
		r, _ := Lookup(check.name)
		if r.EnvValue != check.env || r.AgentName("pj", "L-01") != check.agent ||
			r.Cwd != check.cwd || r.Write != check.write || r.Stoppable != check.stoppable ||
			r.Dispatchable != check.dispatchable || r.Optional != check.optional {
			t.Errorf("%s = %+v", check.name, r)
		}
	}
}

func TestRoleLookupAndDefaults(t *testing.T) {
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("unknown role found")
	}
	if _, ok := LookupEnv(""); ok {
		t.Fatal("empty env value found a role")
	}
	worker, _ := Lookup("worker")
	if got := worker.AgentName("pj", "L-01"); got != "pj-l-01" {
		t.Errorf("worker agent = %q, want the derived pj-l-01", got)
	}
	if got := worker.AgentName("", "L-01"); got != "" {
		t.Errorf("no prefix: agent = %q, want none", got)
	}
}

// T-777, T-1031: a lane's worker has one name, the derived <prefix>-<lane lower> (no lane row records one). The
// compact alias (pj-l1), a configured name and the bare lane name are no names.
func TestLaneAgentAliases(t *testing.T) {
	for _, test := range []struct {
		name, lane string
		want       []string
	}{
		{"the derived name", "L-01", []string{"pj-l-01"}},
		{"another lane", "L-12", []string{"pj-l-12"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := LaneAgentAliases(test.lane, "pj"); !reflect.DeepEqual(got, test.want) {
				t.Errorf("LaneAgentAliases = %#v, want %#v", got, test.want)
			}
		})
	}
	if names := LaneAgentAliases("L-01", "pj"); slices.Contains(names, "pj-l1") {
		t.Errorf("LaneAgentAliases(%q) = %q has the removed compact alias", "L-01", names)
	}
}

// T-751: the drive agent has a name of its own beside the lane's worker, and it is lane-bound like the worker.
func TestDriverNameAndLaneBound(t *testing.T) {
	if got := DriveAgent("pj-l-01"); got != "pj-l-01-drive" {
		t.Errorf("DriveAgent = %q, want pj-l-01-drive", got)
	}
	for role, want := range map[string]bool{"worker": true, Driver: true, "lead": false, "integrator": false, "": false} {
		if got := LaneBound(role); got != want {
			t.Errorf("LaneBound(%q) = %v, want %v", role, got, want)
		}
	}
}

// T-801: a custom role (a config row, or one of the kit's default rows) is an advisor-like role: main checkout, git
// reads only, optional, never dispatchable or stoppable by serve, writes only its list; its agent suffix is its name
// with _ → -, its doc <name>.md.
func TestCustomRoles(t *testing.T) {
	r := Custom("ux_buddy", []string{"docs/ux/", "NOTES.md"})
	want := Role{Name: "ux_buddy", EnvValue: "ux_buddy", AgentSuffix: "ux-buddy", Cwd: CwdProject, Write: WriteList,
		WritePaths: []string{"docs/ux/", "NOTES.md"}, Optional: true, GitReadOnly: true, Custom: true}
	if !reflect.DeepEqual(r, want) {
		t.Errorf("Custom = %+v, want %+v", r, want)
	}
	if got := r.AgentName("pj", ""); got != "pj-ux-buddy" {
		t.Errorf("agent = %q, want pj-ux-buddy", got)
	}
	if r.DocFile() != "ux_buddy.md" {
		t.Errorf("custom doc file = %q, want ux_buddy.md", r.DocFile())
	}
	if dd, _ := Lookup("deep_dive"); dd.DocFile() != "deep-dive.md" {
		t.Errorf("table doc file = %q, want deep-dive.md", dd.DocFile())
	}
	if LaneBound("ux_buddy") || r.Dispatchable || r.Stoppable || r.StartPrompt("claude") != "" {
		t.Errorf("a custom role is lane-bound, dispatchable, stoppable or has a first prompt: %+v", r)
	}
	if _, ok := Lookup("ux_buddy"); ok {
		t.Error("a custom role is in the built-in table")
	}
	defaults := map[string][]string{}
	for _, d := range DefaultRows() {
		if !d.Custom || d.Write != WriteList || !d.GitReadOnly || d.Cwd != CwdProject {
			t.Errorf("default row %s is not a custom role: %+v", d.Name, d)
		}
		if _, inTable := Lookup(d.Name); inTable {
			t.Errorf("default row %s is in the table too", d.Name)
		}
		defaults[d.Name] = d.WritePaths
	}
	if want := map[string][]string{"advisor": {"docs/research/", "docs/plan/"}, "researcher": {"docs/research/"},
		"triage": {"docs/BUGS.md"}}; !reflect.DeepEqual(defaults, want) {
		t.Errorf("default rows' writes = %v, want %v", defaults, want)
	}
	if tr, _ := DefaultRow("triage"); tr.Seeds["docs/BUGS.md"] != "BUGS.md" {
		t.Errorf("triage seeds = %v, want docs/BUGS.md from templates/BUGS.md", tr.Seeds)
	}
	if _, ok := DefaultRow("lead"); ok {
		t.Error("lead is a default row")
	}
}

// T-801: a custom role's name matches [a-z][a-z0-9_]{0,23} and is no table role's name or agent suffix.
func TestCheckCustomName(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"buddy", true}, {"ux_buddy", true}, {"a", true}, {"designer2", true}, {"abcdefghijklmnopqrstuvwx", true},
		{"abcdefghijklmnopqrstuvwxy", false}, {"Buddy", false}, {"ux-buddy", false}, {"2nd", false}, {"", false},
		{"_x", false}, {"lead", false}, {"worker", false}, {"reviewer", false}, {"deep_dive", false},
		{"driver", false}, {"int", false}, {"rev", false}, {"drive", false}, {"none", false}, {"leads", false},
	} {
		if err := CheckCustomName(c.name); (err == nil) != c.ok {
			t.Errorf("CheckCustomName(%q) = %v, want ok %v", c.name, err, c.ok)
		}
	}
}
