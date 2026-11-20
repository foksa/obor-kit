package herdrtest_test

import (
	"context"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
)

// Agent on an id that already has a shell changes that pane in place: one pane object per id, the agent's fields on it,
// the pane's own label kept.
func TestAgentTakesOverAnExistingShellPane(t *testing.T) {
	f := herdrtest.New()
	shell := f.Shell("w1:p1", "pj-l-01", "/p")
	a := f.Agent("w1:p1", "pj-l-01", "idle", "/p")
	if len(f.Panes) != 1 {
		t.Fatalf("panes after the agent started at the shell's id: %d, want 1", len(f.Panes))
	}
	if a != shell {
		t.Fatalf("Agent returned %p, another object than the shell pane %p", a, shell)
	}
	if a.Kind != "claude" || a.Name != "pj-l-01" || a.Status != "idle" || a.Cwd != "/p" {
		t.Errorf("the pane = %+v, want the agent's fields: kind claude, name pj-l-01, idle in /p", a.Wire)
	}
	if a.Label != "pj-l-01" {
		t.Errorf("the pane's label = %q, want the shell's pj-l-01: the label is the pane's", a.Label)
	}
}

// A setter runs before the pane is visible to the server: its fields are in the first answer that names the pane.
func TestSetterFieldsInTheFirstPaneAnswer(t *testing.T) {
	f := herdrtest.New()
	f.Agent("w1:p1", "pj-int", "idle", "/p", func(p *herdrtest.Pane) {
		p.Label, p.Session = "pj-int", "int-1"
	})
	p, err := herdrtest.Serve(t, f).Client().Pane(context.Background(), "w1:p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Label != "pj-int" || p.Session != "int-1" {
		t.Errorf("the first pane.get answered %+v, want the setter's label pj-int and session int-1", p)
	}
}

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
			_, err := cl.Start(context.Background(), "pj-l-01", "claude", "w1:p1", nil, "--model", "opus", c.arg)
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
