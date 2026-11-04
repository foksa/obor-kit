package roles

import (
	"reflect"
	"slices"
	"testing"
)

func TestRoleTableDefaultsAndProfiles(t *testing.T) {
	want := []string{"lead", "integrator", "worker", "reviewer", "deep_dive", "triage", "advisor", "researcher", "driver"}
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
		if got, ok := Lookup(role.Name); !ok || got != role {
			t.Errorf("Lookup(%q) = %+v, %v", role.Name, got, ok)
		}
		if role.EnvValue != "" {
			if got, ok := LookupEnv(role.EnvValue); !ok || got != role {
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
	}{
		{"lead", "lead", "pj-lead", CwdProject, WriteAll, false, false},
		{"integrator", "integrator", "pj-int", CwdProject, WriteNone, false, false},
		{"worker", "worker", "pj-l-01", CwdLane, WriteSpec, true, true},
		{"reviewer", "reviewer", "pj-rev", CwdProject, WriteReviewsOnly, false, false},
		{"deep_dive", "deep_dive", "pj-deep-dive", CwdProject, WriteReviewsOnly, false, false},
		{"triage", "triage", "pj-triage", CwdProject, WriteNone, false, false},
		{"advisor", "advisor", "pj-advisor", CwdProject, WriteNone, false, false},
		{"researcher", "researcher", "pj-researcher", CwdProject, WriteResearchOnly, false, false},
		{"driver", "driver", "pj-drive", CwdLane, WriteSpec, false, false},
	}
	for _, check := range checks {
		r, _ := Lookup(check.name)
		if r.EnvValue != check.env || r.AgentName("pj", "L-01") != check.agent ||
			r.Cwd != check.cwd || r.Write != check.write || r.Stoppable != check.stoppable || r.Dispatchable != check.dispatchable {
			t.Errorf("%s = %+v", check.name, r)
		}
	}
}

// Optional roles count only when configured: triage, deep_dive, advisor and researcher; the driver is never
// configured (obor drive starts it, T-751).
func TestOptionalRoles(t *testing.T) {
	var optional []string
	for _, role := range All() {
		if role.Optional {
			optional = append(optional, role.Name)
		}
	}
	if want := []string{"deep_dive", "triage", "advisor", "researcher", "driver"}; !reflect.DeepEqual(optional, want) {
		t.Errorf("optional roles = %q, want %q", optional, want)
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

// T-777: a lane's worker has two names: the one its lane row records and the derived <prefix>-<lane lower>. The
// compact alias (pj-l1) and a configured name are no names any more.
func TestLaneAgentAliases(t *testing.T) {
	for _, test := range []struct {
		name, lane, state string
		want              []string
	}{
		{"the row's agent then the derived name", "L-01", "bob", []string{"bob", "pj-l-01"}},
		{"no row: the derived name alone", "L-01", "", []string{"pj-l-01"}},
		{"deduplicates", "L-01", "pj-l-01", []string{"pj-l-01"}},
		{"the bare lane name only when the row names it", "L-01", "l-01", []string{"l-01", "pj-l-01"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := LaneAgentAliases(test.lane, "pj", test.state); !reflect.DeepEqual(got, test.want) {
				t.Errorf("LaneAgentAliases = %#v, want %#v", got, test.want)
			}
		})
	}
	if names := LaneAgentAliases("L-01", "pj", ""); slices.Contains(names, "pj-l1") {
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
