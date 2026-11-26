package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/review"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/testkit"
)

// The reviewer's stop after its review closes empties its probe dir: the rule stops it through launch.Stop, the one
// path obor restart <agent> --stop takes, which empties project.ProbeDir. A failed removal is said and the stop goes
// on. TMPDIR, os.TempDir's seam, points at a temp dir of the test's own.
func TestReviewerStopClearsTheProbeDir(t *testing.T) {
	for _, tc := range []struct {
		name        string
		removeFails bool
	}{
		{"the dir is emptied", false},
		{"a removal error is said and the stop goes on", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("TMPDIR", base)
			bubble(t, func(t *testing.T) {
				r := newLoopRig(t, noCheck(), withConfig(testkit.Set("agents.roles.reviewer", "opus")))
				for _, p := range []port.Pane{
					agentPane("w1:p5", "pj-lead", r.p.Root),
					agentPane("w1:p6", "pj-rev", r.p.Root),
				} {
					r.addPane(p)
				}
				// The review serve started is closed before serve starts: the rule's condition on its first pass.
				rec := func(step string, d review.Data) event.Record {
					raw, err := json.Marshal(d)
					if err != nil {
						t.Fatal(err)
					}
					return event.Record{Task: event.ReviewTask, Attempt: 1, Step: step, Kind: event.KindFact, Data: raw}
				}
				if err := app.WriteLog(r.p.Root, r.clock.Now(),
					rec(event.StepReviewStarted, review.Data{ID: "RV-1", Head: "abcdef0"}),
					rec(event.StepReviewClosed, review.Data{ID: "RV-1", Reply: review.ClosedReply + "RV-1 closed · abcdef0 · approved · T-a"})); err != nil {
					t.Fatal(err)
				}
				cfg, err := config.Load(r.p.Root)
				if err != nil {
					t.Fatal(err)
				}
				dir := project.ProbeDir(cfg, "pj-rev")
				if dir == "" {
					t.Fatal("precondition: the reviewer's probe dir has no path")
				}
				if err := os.MkdirAll(filepath.Join(dir, "overlay"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "overlay", "probe.json"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				if tc.removeFails {
					if err := os.Chmod(dir, 0o555); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // its cleanup removes it again
				}
				out := &syncBuf{}
				kit, err := filepath.Abs("../..")
				if err != nil {
					t.Fatal(err)
				}
				r.startWithAgents(&launch.Launcher{Cfg: cfg, Env: r.p.Env(), Kit: kit, H: r.client(), Out: out,
					Wait: time.Second, Poll: time.Millisecond, PS: func(...string) ([]byte, error) { return nil, nil },
					Where: func(context.Context, wire.WhereReq) (wire.WhereReply, error) {
						return wire.WhereReply{State: wire.WhereNone, Why: "no proof"}, nil
					}})
				// The stop ran once the dir is gone (or its removal was said): the rule stopped it, and serve's
				// snapshot is told the pane is at its shell (the rig's terminal edits send no event).
				r.waitFor("the reviewer's stop", func() bool {
					if tc.removeFails {
						return strings.Contains(out.String(), "cannot empty the probe dir of pj-rev")
					}
					_, err := os.Stat(dir)
					return os.IsNotExist(err)
				})
				r.push()
				r.waitFor("pj-rev stopped after its review closed", func() bool {
					return slices.Contains(r.app.Status().Project.Stopped, "pj-rev")
				})
				if tc.removeFails {
					if _, err := os.Stat(dir); err != nil {
						t.Errorf("the probe dir %s is gone despite the removal error: %v", dir, err)
					}
					if !strings.Contains(out.String(), "cannot empty the probe dir of pj-rev") {
						t.Errorf("the removal error is not said: %q", out.String())
					}
					return
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Errorf("the probe dir %s is still there after the stop (%v)", dir, err)
				}
				if strings.Contains(out.String(), "cannot empty the probe dir") {
					t.Errorf("a removal warning with the dir gone: %q", out.String())
				}
			})
		})
	}
}

// A stopped integrator with no open job is the normal state, across a serve restart too: no timer, no job.lost, no
// needs-you, and obor status shows it as stopped.
func TestRoleStopIntegratorAcrossRestart(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r := newLoopRig(t)
		r.work("feature.txt", "feature\n")
		r.start()
		r.submit()
		job := r.waitJob("pj-int", "T-1/1/review")
		r.accept("c1", r.patch(job))
		r.merged(1)
		r.waitFor("pj-int stopped after its verdict", func() bool {
			return slices.Contains(r.app.Status().Project.Stopped, "pj-int")
		})
		starts := r.starts.Load()

		r.stop()
		r.start()
		for i := 0; i < 3; i++ {
			r.later(2 * time.Minute) // past Gone (1 min) each time
		}
		st := r.app.Status().Project
		if !slices.Equal(st.Stopped, []string{"pj-int"}) {
			t.Errorf("status stopped %q after the restart, want [pj-int]", st.Stopped)
		}
		for _, f := range st.Facts {
			t.Errorf("fact in force after the restart: %s %s", f.Task, f)
		}
		for _, s := range r.steps() {
			if strings.Contains(s, event.StepLost) || strings.Contains(s, event.StepNeedsYou) {
				t.Errorf("record %q after the stop", s)
			}
		}
		if n := r.starts.Load(); n != starts {
			t.Errorf("integrator starts %d after the restart, want %d (no job: nothing starts it)", n, starts)
		}
		ints := 0
		for _, s := range r.stopped() {
			if strings.HasPrefix(s, "pj-int ") {
				ints++
			}
		}
		if ints != 1 {
			t.Errorf("stops of pj-int %d, want 1 (the replay stops it no second time): %q", ints, r.stopped())
		}
	})
}
