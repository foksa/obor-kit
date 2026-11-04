package launch_test

import (
	"strings"
	"testing"

	"obor/internal/launch"
)

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
