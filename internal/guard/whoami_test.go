package guard

import (
	"testing"

	"obor/internal/project"
	"obor/internal/testkit"
)

// whoamiFor is Whoami of dir with OBOR_ROLE role.
func whoamiFor(t *testing.T, dir, role string) *Who {
	t.Helper()
	repo, cfg, _, err := loadAt(dir)
	if repo == nil || err != nil {
		t.Fatalf("precondition: loadAt(%s): %v", dir, err)
	}
	w, err := Whoami(repo, cfg, func(k string) (string, bool) {
		if k == "OBOR_ROLE" && role != "-" {
			return role, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// A lead's Whoami names how its lines reach it (agents.LeadLines), from its kind's caps and its inbox record: a kind
// with an inbox (Claude, OpenCode) whose record names a socket that is there, its inbox; with none recorded, its hooks
// (both act on the answers), as for the test kind (no inbox, the answers); agy, whose record an obor command writes
// and the kind sends with: its inbox while no send with it was refused, else its hooks; Codex: only obor status. A
// worker's names none.
func TestWhoamiLeadLines(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, ref string
		live      bool
		sends     string // an agy record: "live", "refused" (a send with it was refused), "" none
		want      string
	}{
		{"claude, a live inbox", "opus", true, "", LinesInbox},
		{"claude, no inbox recorded", "opus", false, "", LinesHooks},
		{"opencode, a live inbox", "deepseek", true, "", LinesInbox},
		{"opencode, no inbox recorded", "deepseek", false, "", LinesHooks},
		{"agy, a record its command wrote", "gem", false, "live", LinesInbox},
		{"agy, its last send refused", "gem", false, "refused", LinesHooks},
		{"agy, no inbox recorded", "gem", false, "", LinesHooks},
		{"hook-answers kind", "hooky", true, "", LinesHooks},
		{"codex", "terra", true, "", LinesStatus},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := fixture(t)
			f.Config(testkit.HookModel(), testkit.Set("agents.models.gem", map[string]any{"kind": "agy", "id": "gemini-3-flash"}),
				testkit.Set("agents.roles.lead", c.ref))
			if c.live {
				liveInbox(t, f.Project)
			}
			if c.sends != "" {
				in := project.Inbox{Sends: true, Record: `{"ANTIGRAVITY_CSRF_TOKEN":"t"}`}
				if c.sends == "refused" {
					in.Refused = "agentapi send-message: exit 1: invalid CSRF token"
				}
				if err := project.WriteInbox(f.Cfg(), "lead", in); err != nil {
					t.Fatal(err)
				}
			}
			w := whoamiFor(t, f.P, "lead")
			if w.Role.Role != "lead" {
				t.Fatalf("precondition: role %q, want lead", w.Role.Role)
			}
			if w.Lines != c.want {
				t.Errorf("lead on %s: lines %q, want %q", c.ref, w.Lines, c.want)
			}
		})
	}
	f := fixture(t)
	f.Config(testkit.HookModel(), testkit.Set("agents.roles.lead", "opus"))
	if w := whoamiFor(t, f.L, "-"); w.Role.Role != "worker" || w.Lines != "" {
		t.Errorf("worker: role %q, lines %q; want a worker with no lines", w.Role.Role, w.Lines)
	}
}
