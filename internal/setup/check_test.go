package setup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// obor setup check runs the terminal's own check: its line after OK; its refusal (hc's env guard) is the FAIL line,
// exit 1; a terminal with none (herdr) says so. It is read-only and takes no argument.
func TestSetupCheck(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("project.terminal", "hc"))
	before := x.Read(".obor/config.json")
	run := func(check func(context.Context) (string, error), args ...string) (string, int) {
		s, out := x.newSetup(t)
		s.Dir, s.Check = x.Root, check
		rc := s.Run(context.Background(), append([]string{"check"}, args...))
		return out.String(), rc
	}
	calls := 0
	clean := func(context.Context) (string, error) {
		calls++
		return "hc's core environment holds no HERDR_ or OBOR_ name", nil
	}
	if out, rc := run(clean); rc != 0 || out != "OK check: terminal hc: hc's core environment holds no HERDR_ or OBOR_ name\n" || calls != 1 {
		t.Errorf("a clean core: exit %d, %d checks, out %q", rc, calls, out)
	}
	leak := "hc core env: hc's core was started from an obor pane: run `hc server restart --fresh` from a shell outside any pane " +
		"(its environment holds OBOR_ROLE)"
	out, rc := run(func(context.Context) (string, error) { return "", errors.New(leak) })
	if rc != 1 || last(out) != "FAIL terminal hc: "+leak {
		t.Errorf("a leaked core: exit %d, last line %q", rc, last(out))
	}
	if out, rc := run(nil); rc != 0 || out != "OK check: terminal hc: no check of its own\n" {
		t.Errorf("a terminal with no check: exit %d, out %q", rc, out)
	}
	if out, rc := run(clean, "extra"); rc != 1 || !strings.HasPrefix(last(out), "FAIL usage:") || calls != 1 {
		t.Errorf("an argument: exit %d, %d checks, last line %q, want the usage and no check", rc, calls, last(out))
	}
	if x.Read(".obor/config.json") != before {
		t.Error("obor setup check changed the config (it is read-only)")
	}
}
