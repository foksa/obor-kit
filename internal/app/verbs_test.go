package app

import (
	"testing"

	"obor/internal/app/wire"
)

// leadOrOwner admits the owner (no role) and the lead alone: a built-in role and a custom one are refused in the one
// line every lead-or-owner method shares. A custom role's grant (roles.<name>.verbs) is leadOr's, after this check.
func TestLeadOrOwner(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, agent, role string
		allowed           bool
	}{
		{"owner", "", "", true},
		{"lead", "pj-lead", "lead", true},
		{"a role", "pj-l-01", "worker", false},
		{"a custom role", "pj-tracker", "tracker", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := leadOrOwner(wire.MethodPause, "pauses", wire.Envelope{Agent: c.agent, Role: c.role})
			if c.allowed {
				if err != nil {
					t.Fatalf("allowed: %v; want nil", err)
				}
				return
			}
			want := "pause: the lead or the owner pauses, not " + Dash(c.agent) + " (role " + c.role + ")"
			if err == nil || err.Error() != want {
				t.Fatalf("refused: %v; want %q", err, want)
			}
		})
	}
}
