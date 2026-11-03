package roles

import (
	"reflect"
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
		{"worker", "worker", "pj-o-01", CwdLane, WriteSpec, true, true},
		{"reviewer", "reviewer", "pj-rev", CwdProject, WriteReviewsOnly, false, false},
		{"deep_dive", "deep_dive", "pj-deep-dive", CwdProject, WriteReviewsOnly, false, false},
		{"triage", "", "pj-triage", CwdProject, WriteNone, false, false},
		{"advisor", "advisor", "pj-advisor", CwdProject, WriteNone, false, false},
		{"researcher", "researcher", "pj-researcher", CwdProject, WriteResearchOnly, false, false},
		{"driver", "driver", "pj-drive", CwdLane, WriteSpec, false, false},
	}
	for _, check := range checks {
		r, _ := Lookup(check.name)
		if r.EnvValue != check.env || r.AgentName("pj", "O-01", "") != check.agent ||
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

func TestRoleAgentAndWorktreeOverrides(t *testing.T) {
	worker, _ := Lookup("worker")
	if got := worker.AgentName("pj", "O-01", "custom-worker"); got != "custom-worker" {
		t.Errorf("worker override = %q", got)
	}
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("unknown role found")
	}
	if _, ok := LookupEnv(""); ok {
		t.Fatal("empty env value found a role")
	}
}

func TestLaneAgentAliases(t *testing.T) {
	tests := []struct {
		name, lane, state, configured string
		want                          []string
	}{
		{"all aliases", "O-01", "state-lane", "custom-lane", []string{"state-lane", "custom-lane", "pj-o-01", "pj-o1"}},
		{"no optional aliases", "S-01", "", "", []string{"pj-s-01", "pj-s1"}},
		{"deduplicates aliases", "o1", "pj-o1", "pj-o1", []string{"pj-o1"}},
		{"bare lane name only as the state agent", "O-01", "o-01", "", []string{"o-01", "pj-o-01", "pj-o1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := LaneAgentAliases(test.lane, "pj", test.state, test.configured); !reflect.DeepEqual(got, test.want) {
				t.Errorf("LaneAgentAliases = %#v, want %#v", got, test.want)
			}
		})
	}
}

// T-751: the drive agent has a name of its own beside the lane's worker, and it is lane-bound like the worker.
func TestDriverNameAndLaneBound(t *testing.T) {
	if got := DriveAgent("pj-o-01"); got != "pj-o-01-drive" {
		t.Errorf("DriveAgent = %q, want pj-o-01-drive", got)
	}
	for role, want := range map[string]bool{"worker": true, Driver: true, "lead": false, "integrator": false, "": false} {
		if got := LaneBound(role); got != want {
			t.Errorf("LaneBound(%q) = %v, want %v", role, got, want)
		}
	}
}
