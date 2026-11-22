package guard

import (
	"testing"

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

// A lead's Whoami names how its lines reach it, from its kind's caps (a Claude ref: its inbox; a kind with no inbox
// whose hooks act on the answers: its hooks; OpenCode, Codex: only obor status), and a worker's names none.
func TestWhoamiLeadLines(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, ref, want string
	}{
		{"claude", "opus", LinesInbox},
		{"hook-answers kind", "hooky", LinesHooks},
		{"opencode", "deepseek", LinesStatus},
		{"codex", "terra", LinesStatus},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := fixture(t)
			f.Config(testkit.HookModel(), testkit.Set("agents.roles.lead", c.ref))
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
