package launch_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/project"
	"obor/internal/testkit"
)

// A restart --stop empties the agent's probe dir (project.ProbeDir: the reviewer's and the deep dive's probes), the
// dir the next start probes in again. The test points TMPDIR, os.TempDir's seam, at a temp dir of its own.
func TestStopClearsTheProbeDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	x := setup(t, testkit.Set("agents.roles.deep_dive", "opus"))
	x.agent("w1:p3", "pj-deep-dive", "claude", "idle")
	dir := project.ProbeDir(x.p.Cfg(), "pj-deep-dive")
	if dir == "" || !strings.HasPrefix(dir, base+string(filepath.Separator)) {
		t.Fatalf("precondition: ProbeDir = %q, want a dir under %s", dir, base)
	}
	if err := os.MkdirAll(filepath.Join(dir, "overlay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "overlay", "probe.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, ok := x.stop("pj-deep-dive", "", false)
	if !ok || !strings.HasPrefix(last(out), "STOPPED pj-deep-dive") {
		t.Fatalf("the stop of the deep dive: %q", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the probe dir %s is still there after the stop (%v)", dir, err)
	}
}

// A stop of an agent with no labelled pane is a plain FAIL, not port.NotFound: a
// label is not the identity, so a missing one never says the agent is gone (serve's identity decides that, stopAgent);
// herdr's pane list failing is a plain FAIL too.
func TestStopNoPanePlainFail(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		prep func(x *fx)
		want string
		gone bool
	}{{
		name: "no pane labelled the agent",
		prep: func(x *fx) {},
		want: "no pane labelled pj-l-01 (pass --pane)",
	}, {
		name: "the terminal pane list failed",
		prep: func(x *fx) { x.f.Fail("Panes", termError("panes", "", "boom")) },
		want: "terminal pane list failed (is the terminal running?)",
	}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			c.prep(x)
			labelled := false // precondition: pj-l-01 is the config's lane agent, and no pane is labelled it
			x.f.Locked(func() {
				for _, p := range x.f.All {
					labelled = labelled || p.Label == "pj-l-01"
				}
			})
			if labelled {
				t.Fatal("precondition: a pane is labelled pj-l-01")
			}
			var out bytes.Buffer
			_, err := x.launcher(&out).Stop(context.Background(), "pj-l-01", "", false)
			if err == nil || err.Error() != c.want {
				t.Fatalf("stop of pj-l-01: %v, want %q", err, c.want)
			}
			if got := port.NotFound(err); got != c.gone {
				t.Errorf("port.NotFound(%v) = %v, want %v", err, got, c.gone)
			}
		})
	}
}
