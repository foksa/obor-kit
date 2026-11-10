package project

import (
	"errors"
	"testing"
)

// Pane is the one resolver of an agent's pane: OBOR_PANE (every start obor makes exports it), else the
// terminal's HERDR_PANE_ID (a pane obor did not start, or one started before OBOR_PANE), else ErrNoPane naming both.
func TestPane(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"OBOR_PANE only", map[string]string{"OBOR_PANE": "w1:p2"}, "w1:p2"},
		{"HERDR_PANE_ID only: the fallback", map[string]string{"HERDR_PANE_ID": "w1:p3"}, "w1:p3"},
		{"both: OBOR_PANE wins", map[string]string{"OBOR_PANE": "w1:p2", "HERDR_PANE_ID": "w1:p3"}, "w1:p2"},
		{"OBOR_PANE empty (a pane being made): the fallback", map[string]string{"OBOR_PANE": "", "HERDR_PANE_ID": "w1:p3"}, "w1:p3"},
		{"neither", map[string]string{"OBOR_LANE": "L-01"}, ""},
	} {
		env := Env(func(k string) (string, bool) { v, ok := c.env[k]; return v, ok })
		got, err := Pane(env)
		if got != c.want {
			t.Errorf("%s: pane %q, want %q", c.name, got, c.want)
		}
		if c.want != "" && err != nil {
			t.Errorf("%s: error %v, want none", c.name, err)
		}
		if c.want == "" && (!errors.Is(err, ErrNoPane) ||
			err.Error() != "no pane: neither OBOR_PANE nor HERDR_PANE_ID is set") {
			t.Errorf("%s: error %v, want ErrNoPane naming both variables", c.name, err)
		}
	}
}
