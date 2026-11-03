package launch_test

import (
	"strings"
	"testing"

	"obor/internal/launch"
)

// T-722, T-765: every lane is pooled; the model a dispatch chose (the launch's, else the lane row's) picks the kind and
// args of the lane agent's start, effort included; with neither, the default kind starts with no model.
func TestStartTakesTheDispatchType(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, row string // the lane row's type ("": no row)
		req       launch.Request
		has       []string // in agent.start
		lacks     []string
	}{
		{"no dispatch model: the default kind, no model", "", launch.Request{Agent: "pj-l-01", Fresh: true},
			[]string{"kind=claude"}, []string{"gpt-6-luna", "--model"}},
		{"the launch's type and effort", "", launch.Request{Agent: "pj-l-01", Fresh: true, Type: "luna", Effort: "xhigh"},
			[]string{"kind=codex", "-m gpt-6-luna", "model_reasoning_effort=xhigh"}, []string{"--model opus", "kind=claude"}},
		{"the launch's type before the row's", "opus:low", launch.Request{Agent: "pj-l-01", Fresh: true, Type: "luna"},
			[]string{"kind=codex", "-m gpt-6-luna"}, []string{"--model opus", "low"}},
		{"the lane row's type and effort", "luna:high", launch.Request{Agent: "pj-l-01", Fresh: true},
			[]string{"kind=codex", "-m gpt-6-luna", "model_reasoning_effort=high"}, []string{"--model opus", "kind=claude"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			if c.row != "" {
				x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"`+c.row+`"}`)
			}
			if row, ok := x.p.Row("L-01"); c.row != "" && (!ok || row.Type != c.row) {
				t.Fatalf("lane row %+v (%v), want type %s", row, ok, c.row)
			}
			x.shell("w1:p3", "pj-l-01", "")
			out, ok := x.restart(c.req)
			if !ok {
				t.Fatal(out)
			}
			start := x.startCall()
			if !strings.HasPrefix(start, "agent.start args=[") {
				t.Fatalf("no agent.start: %q", start)
			}
			for _, h := range c.has {
				if !strings.Contains(start, h) {
					t.Errorf("agent.start %q lacks %q", start, h)
				}
			}
			for _, l := range c.lacks {
				if strings.Contains(start, l) {
					t.Errorf("agent.start %q has %q", start, l)
				}
			}
		})
	}
}

// T-722: obor restart <worker> resumes the recorded session when its kind is the lane row type's kind, else starts a
// new session of the row type's kind (said), without --fresh.
func TestRestartResumesOnlyTheRowTypesKind(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, row string
		has       []string
		lacks     []string
		out       string
	}{
		{"same kind: resumed", "opus", []string{"kind=claude", "--model opus", "--resume sid-1"}, []string{"gpt-6-luna"},
			"resuming sid-1"},
		{"changed kind: a new session of the row's kind", "luna", []string{"kind=codex", "-m gpt-6-luna"},
			[]string{"sid-1", "kind=claude"}, "session sid-1 is claude's, the type luna is codex's: new session"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.sid("pj-l-01", "sid-1") // a claude session
			if x.recorded("pj-l-01") != "sid-1" {
				t.Fatal("precondition: sid-1 is recorded")
			}
			x.p.State("L-01", `{"task":"T-1","branch":"task/T-1","agent":"pj-l-01","type":"`+c.row+`"}`)
			x.shell("w1:p3", "pj-l-01", "")
			out, ok := x.restart(launch.Request{Agent: "pj-l-01"})
			if !ok {
				t.Fatal(out)
			}
			if !strings.Contains(out, c.out) {
				t.Errorf("output %q lacks %q", out, c.out)
			}
			start := x.startCall()
			if !strings.HasPrefix(start, "agent.start args=[") {
				t.Fatalf("no agent.start: %q", start)
			}
			for _, h := range c.has {
				if !strings.Contains(start, h) {
					t.Errorf("agent.start %q lacks %q", start, h)
				}
			}
			for _, l := range c.lacks {
				if strings.Contains(start, l) {
					t.Errorf("agent.start %q has %q", start, l)
				}
			}
		})
	}
}
