package checks

import (
	"slices"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// serve's gate tells GateInput.Command each command as it starts and "" as it ends (its
// lane_setup and compile), and the lane test tells TestInput.Command its own: a partial merge its guard then its class's
// check (check_affected), never check; a full merge check; each runs on with no command between two.
func TestGateReportsItsCommands(t *testing.T) {
	t.Parallel()
	output := testkit.Set("checks.output", map[string]any{"summary": "[0-9]+ pass"})
	byPath := testkit.Set("merge.by_path", map[string]any{"guard": "guard",
		"rules": []any{map[string]any{"name": "any", "paths": []any{"**"}, "run": []any{"check_affected"}}}})
	for _, tc := range []struct {
		name  string
		edits []func(map[string]any)
		want  []string
	}{
		{"partial", []func(map[string]any){output, byPath, cmd("guard", "echo 1 pass"), cmd("check_affected", "echo 2 pass"),
			cmd("check", "echo 3 pass")}, []string{"guard", "", "check_affected", ""}},
		{"full", []func(map[string]any){cmd("check", "echo 3 pass")}, []string{"check", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			k := newFk(t, append(tc.edits, cmd("compile", "echo 1 pass"), cmd("lane_setup", "true"))...)
			in := lane(k.submit())
			var got []string
			in.Command = func(name string) { got = append(got, name) }
			c := k.green(in)
			if want := []string{"lane_setup", "", "compile", ""}; !slices.Equal(got, want) {
				t.Errorf("the gate told %q, want %q", got, want)
			}
			k.g.answers["Changed "+c.Target+" "+c.SHA] = "feature.txt"
			got = nil
			ti := port.TestInput{Task: "T-05", Lane: "L-01", Branch: "task/T-05", SHA: c.SHA, Target: c.Target,
				Command: func(name string) { got = append(got, name) }}
			if o, err := k.s.Test(ctx, ti); err != nil || o.Kind != port.OutcomeGreen {
				t.Fatalf("test: %+v %v\noutput:\n%s", o, err, k.out.String())
			}
			if want := append([]string{"lane_setup", ""}, tc.want...); !slices.Equal(got, want) {
				t.Errorf("the lane test told %q, want %q", got, want)
			}
		})
	}
}
