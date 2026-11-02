package setup

import (
	"strings"
	"testing"

	"obor/internal/testkit"
)

// T-703: the env obor setup hands herdr for a new OpenCode pane carries the start's model and effort for obor's plugin
// (OBOR_OPENCODE_MODEL provider/model#effort), and nothing of the T-696 workaround (no XDG_STATE_HOME, no
// OPENCODE_CONFIG_CONTENT). A type without a model hands the variable empty, so a value the pane would inherit from
// setup's own env (an OpenCode agent running setup) is none to the plugin. A new pane's shell is never typed at.
func TestSetupOpencodePaneEnvCarriesTheModel(t *testing.T) {
	t.Parallel()
	const types = `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"bare":{"kind":"opencode","args":["--auto"]}}`
	for _, c := range []struct {
		name, role string // role "": the lane O-01 of type bare, else the reviewer of that type reference
		want       string // in the split's env
	}{
		{"a role's model and effort: model#effort", "oc:high", "OBOR_OPENCODE_MODEL=go/ds#high,"},
		{"a role's model: the model", "oc", "OBOR_OPENCODE_MODEL=go/ds,"},
		{"a lane without a model: empty, over an inherited one", "", "OBOR_OPENCODE_MODEL=,"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, testkit.Set("lane_types", types), testkit.Set("roles", `{"reviewer":{"type":"`+c.role+`"}}`))
			x.env = append(x.env, "OBOR_OPENCODE_MODEL=inherited/m#max", "XDG_STATE_HOME=/mine/state")
			args := []string{"role", "reviewer", "--tab", "w1:t1"}
			if c.role == "" {
				x.ok(t, "lane", "O-01", "bare")
				args = []string{"pane", "O-01", "--tab", "w1:t1"}
			}
			x.reset()
			if out := x.ok(t, args...); !strings.HasPrefix(last(out), "OK ") {
				t.Fatalf("last line %q", last(out))
			}
			calls := strings.Join(x.reqs(), "\n")
			has(t, calls, "pane.split", "precondition: a new pane")
			has(t, calls, "kind=opencode", "the start")
			has(t, calls, c.want, "the plugin's model")
			hasNot(t, calls, "inherited/m", "the inherited model")
			hasNot(t, calls, "XDG_STATE_HOME", "no state dir, the user's own untouched")
			hasNot(t, calls, "OPENCODE_CONFIG_CONTENT", "no config content")
			if len(x.envLines()) != 0 {
				t.Errorf("a new pane's shell is typed at: %q", x.envLines())
			}
		})
	}
}
