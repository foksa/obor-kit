package launch

import "testing"

// T-544: the env line is done once the shell echoed its mark and drew its next prompt; the typed line (wrapped at any
// column), the echo with no prompt yet, or another restart's mark is not.
func TestEnvMarkDone(t *testing.T) {
	t.Parallel()
	m := mark{typed: `obor-env-"abc1"`, echoed: "obor-env-abc1"}
	typed := `me@host proj % export OBOR_AGENT=pj-o-01; echo ` + m.typed
	for _, c := range []struct {
		name, screen string
		want         bool
	}{
		{"typed only", typed + "\n", false},
		{"typed, wrapped before the quote", "me@host proj % export OBOR_AGENT=pj-o-01; echo obor-env-\n\"abc1\"\n", false},
		{"typed, wrapped after the word", "me@host proj % export OBOR_AGENT=pj-o-01; echo obor-env-\"abc1\n\"\n", false},
		{"echoed, no prompt yet", typed + "\nobor-env-abc1\n\n", false},
		{"echoed, then the prompt", typed + "\nobor-env-abc1\nme@host proj %\n", true},
		{"another restart's mark, then a prompt", "obor-env-zzz9\nme@host proj %\n" + typed + "\n", false},
	} {
		if got := m.done(c.screen); got != c.want {
			t.Errorf("%s: done = %v, want %v (screen %q)", c.name, got, c.want, c.screen)
		}
	}
	if a, b := envMark(), envMark(); a.echoed == b.echoed || a.typed == a.echoed {
		t.Errorf("marks %+v, %+v: want each its own word, typed quoted", a, b)
	}
}
