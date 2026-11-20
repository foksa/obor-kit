package project

import (
	"errors"
	"testing"

	"obor/internal/config"
)

// Pane is the one resolver of an agent's pane: OBOR_PANE (every start obor makes exports it), else the pane variable of
// the project's terminal kind (herdr HERDR_PANE_ID, hc HC_TERMINAL_ID; another kind's variable is never read), an empty
// value read as unset, else ErrNoPane naming what it read.
func TestPane(t *testing.T) {
	t.Parallel()
	herdr := (*config.Config)(nil) // no config: the default kind
	hc := &config.Config{Data: config.Data{Project: config.Project{Terminal: config.TerminalHC}}}
	if herdr.TerminalKind() != config.DefaultTerminal || hc.TerminalKind() != config.TerminalHC {
		t.Fatalf("precondition: kinds %q and %q, want herdr and hc", herdr.TerminalKind(), hc.TerminalKind())
	}
	for _, c := range []struct {
		name    string
		cfg     *config.Config
		env     map[string]string
		want    string
		wantErr string // the error when want is ""
	}{
		{"herdr: OBOR_PANE only", herdr, map[string]string{"OBOR_PANE": "w1:p2"}, "w1:p2", ""},
		{"herdr: HERDR_PANE_ID only: the fallback", herdr, map[string]string{"HERDR_PANE_ID": "w1:p3"}, "w1:p3", ""},
		{"herdr: both: OBOR_PANE wins", herdr, map[string]string{"OBOR_PANE": "w1:p2", "HERDR_PANE_ID": "w1:p3"}, "w1:p2", ""},
		{"herdr: OBOR_PANE empty (a pane being made): the fallback", herdr,
			map[string]string{"OBOR_PANE": "", "HERDR_PANE_ID": "w1:p3"}, "w1:p3", ""},
		{"herdr: HC_TERMINAL_ID is hc's, never read", herdr, map[string]string{"HC_TERMINAL_ID": "t7"}, "",
			"no pane: neither OBOR_PANE nor HERDR_PANE_ID is set"},
		{"herdr: neither", herdr, map[string]string{"OBOR_LANE": "L-01"}, "", "no pane: neither OBOR_PANE nor HERDR_PANE_ID is set"},
		{"hc: HC_TERMINAL_ID only: the fallback", hc, map[string]string{"HC_TERMINAL_ID": "t7"}, "t7", ""},
		{"hc: OBOR_PANE wins", hc, map[string]string{"OBOR_PANE": "t2", "HC_TERMINAL_ID": "t7"}, "t2", ""},
		{"hc: an inherited HERDR_PANE_ID is never taken for the pane", hc,
			map[string]string{"HERDR_PANE_ID": "w1:p3", "HC_TERMINAL_ID": "t7"}, "t7", ""},
		{"hc: HERDR_PANE_ID alone names no pane", hc, map[string]string{"HERDR_PANE_ID": "w1:p3"}, "",
			"no pane: neither OBOR_PANE nor HC_TERMINAL_ID is set"},
		{"hc: an empty OBOR_PANE (set empty, never unset) reads as unset", hc,
			map[string]string{"OBOR_PANE": "", "HC_TERMINAL_ID": "t7"}, "t7", ""},
		{"hc: empty names read as unset", hc, map[string]string{"OBOR_PANE": "", "HC_TERMINAL_ID": ""}, "",
			"no pane: neither OBOR_PANE nor HC_TERMINAL_ID is set"},
		{"a kind with no pane variable: OBOR_PANE only", &config.Config{Data: config.Data{Project: config.Project{Terminal: "rex"}}},
			map[string]string{"HERDR_PANE_ID": "w1:p3"}, "", "no pane: OBOR_PANE is not set (terminal rex has no pane variable)"},
	} {
		env := Env(func(k string) (string, bool) { v, ok := c.env[k]; return v, ok })
		got, err := PaneOf(c.cfg, env)
		if got != c.want {
			t.Errorf("%s: pane %q, want %q", c.name, got, c.want)
		}
		if c.want != "" && err != nil {
			t.Errorf("%s: error %v, want none", c.name, err)
		}
		if c.want == "" && (!errors.Is(err, ErrNoPane) || err.Error() != c.wantErr) {
			t.Errorf("%s: error %v, want ErrNoPane %q", c.name, err, c.wantErr)
		}
	}
}

// PaneVars, the names a process that is no agent's drops, hold every kind's pane variable and OBOR_PANE.
func TestPaneVarsEveryKind(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"OBOR_PANE", "HERDR_PANE_ID", "HC_TERMINAL_ID"} {
		found := false
		for _, p := range PaneVars {
			found = found || p == v
		}
		if !found {
			t.Errorf("PaneVars %q lacks %s", PaneVars, v)
		}
	}
}
