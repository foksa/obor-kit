package herdrtest_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
)

// herdr encodes an agent.start argument for the target shell and refuses one with a newline (herdr 0.9.3,
// invalid_agent_argument; live probe 2026-10-27), before anything runs; quotes, backticks, $, backslashes, braces, &
// and ; pass, as the probe showed.
func TestStartRefusesNewlineArgument(t *testing.T) {
	for _, c := range []struct {
		name    string
		arg     string
		refused bool
	}{
		{"a one-line prompt: started", "call the obor job_next tool", false},
		{"quotes, backticks, $, backslashes, braces, & and ; pass", "a \"b\" `c` $d e\\f {g} & h; i", false},
		{"a newline: refused", "line one\nline two", true},
		{"a carriage return: refused", "line one\rline two", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := herdrtest.New()
			f.Shell("w1:p1", "pj-l-01", "")
			cl := herdrtest.Serve(t, f).Client()
			_, err := cl.Start(context.Background(), "pj-l-01", "claude", "w1:p1", "--model", "opus", c.arg)
			p := f.Pane("w1:p1")
			if p == nil {
				t.Fatal("precondition: pane w1:p1 exists")
			}
			if !c.refused {
				if err != nil {
					t.Fatalf("start refused: %v", err)
				}
				if p.Kind != "claude" || p.Name != "pj-l-01" {
					t.Errorf("pane holds %q named %q, want the agent started", p.Kind, p.Name)
				}
				return
			}
			if err == nil {
				t.Fatal("an argument with a newline started")
			}
			for _, want := range []string{"invalid_agent_argument", "agent arguments cannot be encoded safely for the target shell"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %v does not name %q", err, want)
				}
			}
			if p.Kind != "" || p.Name != "" {
				t.Errorf("pane holds %q named %q after the refusal, want nothing ran", p.Kind, p.Name)
			}
		})
	}
}
