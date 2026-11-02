package wire

import (
	"testing"
	"time"
)

// T-685: OBOR_CALL_DEADLINE, in seconds (fractions allowed), is every method's deadline when set; unset or no number,
// the table's own.
func TestCallDeadlineEnv(t *testing.T) {
	for _, c := range []struct {
		env, method string
		want        time.Duration
	}{
		{"", MethodAccept, verdictDeadline},
		{"", MethodTurn, turnDeadline},
		{"", MethodStopped, stopFailureDeadline},
		{"0.2", MethodAccept, 200 * time.Millisecond},
		{"0.2", MethodTurn, 200 * time.Millisecond},
		{"0.2", MethodStopped, 200 * time.Millisecond},
		{"bogus", MethodAccept, verdictDeadline},
	} {
		t.Setenv("OBOR_CALL_DEADLINE", c.env)
		if got := callDeadline(c.method); got != c.want {
			t.Errorf("OBOR_CALL_DEADLINE=%q: callDeadline(%s) = %v, want %v", c.env, c.method, got, c.want)
		}
	}
}
