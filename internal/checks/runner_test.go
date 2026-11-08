package checks

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/testkit"
)

// plainFx is the fixture's config (fxConfig plus edits) in a plain project (testkit.NewPlain): no git, for a runner test
// whose commands run in the root and read no repository (T-1018: the unit tier runs no git).
func plainFx(t *testing.T, edits ...func(map[string]any)) *fx {
	t.Helper()
	p := testkit.NewPlain(t)
	p.Config(fxConfig(edits...)...)
	return &fx{Project: p, t: t, scratch: filepath.Join(p.Base, "proj-wt", "merge"), slots: filepath.Join(p.Base, "slots"),
		sig: make(chan int, 1)}
}

// commandCalls is a runner over dir whose onCommand trail the test reads: the command each start names and "" each end
// reports.
func commandCalls(t *testing.T, opts Options, dir string) (*runner, *[]string) {
	t.Helper()
	r := newRunner(context.Background(), &opts, dir, "T-5", "serve-changed-files")
	got := &[]string{}
	r.onCommand = func(name string) { *got = append(*got, name) }
	return &r, got
}

// T-1003 (RV-139 #1): a lane in two classes runs the guard once, then each class's checks once, in config order; a
// command both classes name runs once. Only neutral files still run the guard alone.
func TestRunChecksMixedClass(t *testing.T) {
	x := plainFx(t,
		testkit.Set("merge.guard", "guard"),
		testkit.Set("merge.classes", `[
			{"name": "go", "paths": ["internal/**"], "checks": ["check_affected", "check_shared"]},
			{"name": "release", "paths": ["Makefile"], "checks": ["check_release", "check_shared"]}]`),
		cmd("guard", "echo 1 pass"), cmd("check_affected", "echo 1 pass"),
		cmd("check_release", "echo 1 pass"), cmd("check_shared", "echo 1 pass"))
	o := x.opts()
	for _, c := range []struct {
		name, files, mode, checks string
		want                      []string
	}{
		{"two classes", "Makefile internal/app/loop.go", "partial:go+release",
			"guard 1 pass, check_affected 1 pass, check_shared 1 pass, check_release 1 pass",
			[]string{"guard", "", "check_affected", "", "check_shared", "", "check_release", ""}},
		{"neutral", "docs/a.md", "partial:neutral", "guard 1 pass", []string{"guard", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ran := commandCalls(t, o, x.Root)
			r.prio = "worker"
			defer r.releaseSlot()
			r.class = Classify(o.Cfg.Data.Merge, strings.Fields(c.files), false)
			r.mode = r.class.Mode()
			if !r.class.Partial || r.mode != c.mode {
				t.Fatalf("precondition: class %s, want %s", r.mode, c.mode)
			}
			if h := r.runChecks(); h != nil {
				t.Fatalf("runChecks: %+v\noutput:\n%s", h, x.out.String())
			}
			if !slices.Equal(*ran, c.want) {
				t.Errorf("ran %q, want %q", *ran, c.want)
			}
			if r.check != c.checks {
				t.Errorf("checks %q, want %q", r.check, c.checks)
			}
		})
	}
}

// T-1007 (RV-139 #1): a full lane (here an unmatched go.mod beside a class's file) runs check, then each matched
// class marked with_full's checks, config order; a class without with_full adds nothing.
func TestRunChecksFullWithFull(t *testing.T) {
	x := plainFx(t,
		testkit.Set("merge.guard", "guard"),
		testkit.Set("merge.classes", `[
			{"name": "go", "paths": ["internal/**"], "checks": ["check_affected"]},
			{"name": "release", "paths": ["Makefile"], "checks": ["check_release"], "with_full": true},
			{"name": "web", "paths": ["web/src/**"], "checks": ["check_web"], "with_full": true}]`),
		cmd("check", "echo 1 pass"), cmd("check_affected", "echo 1 pass"),
		cmd("check_release", "echo 1 pass"), cmd("check_web", "echo 1 pass"))
	o := x.opts()
	for _, c := range []struct {
		name, files, mode, checks string
		want                      []string
	}{
		{"release and an unmatched path", "Makefile go.mod", "full:unmatched go.mod", "1 pass, check_release 1 pass",
			[]string{"check", "", "check_release", ""}},
		{"web and an unmatched path", "web/src/x.ts go.mod", "full:unmatched go.mod", "1 pass, check_web 1 pass",
			[]string{"check", "", "check_web", ""}},
		{"go and an unmatched path (go has no with_full)", "internal/app/x.go go.mod", "full:unmatched go.mod", "1 pass",
			[]string{"check", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ran := commandCalls(t, o, x.Root)
			r.prio = "worker"
			defer r.releaseSlot()
			r.class = Classify(o.Cfg.Data.Merge, strings.Fields(c.files), false)
			r.mode = r.class.Mode()
			if r.class.Partial || r.mode != c.mode {
				t.Fatalf("precondition: class %s, want %s", r.mode, c.mode)
			}
			if h := r.runChecks(); h != nil {
				t.Fatalf("runChecks: %+v\noutput:\n%s", h, x.out.String())
			}
			if !slices.Equal(*ran, c.want) {
				t.Errorf("ran %q, want %q", *ran, c.want)
			}
			if r.check != c.checks {
				t.Errorf("checks %q, want %q", r.check, c.checks)
			}
		})
	}
}

// T-1008 (RV-139 #2): OBOR_FULL=1 forces the merge full, but the scan still names the classes it matched, so release
// and web files keep their with_full checks (before: the forced early return dropped them).
func TestRunChecksForcedKeepsWithFull(t *testing.T) {
	x := plainFx(t,
		testkit.Set("merge.guard", "guard"),
		testkit.Set("merge.classes", `[
			{"name": "release", "paths": ["Makefile"], "checks": ["check_release"], "with_full": true},
			{"name": "web", "paths": ["web/src/**"], "checks": ["check_web"], "with_full": true}]`),
		cmd("check", "echo 1 pass"), cmd("check_release", "echo 1 pass"), cmd("check_web", "echo 1 pass"))
	x.env = append(x.env, "OBOR_FULL=1")
	o := x.opts()
	if o.Env.Get("OBOR_FULL") != "1" {
		t.Fatal("precondition: the fixture hands OBOR_FULL=1 to the runner")
	}
	for _, c := range []struct {
		name, files, checks string
		want                []string
	}{
		{"a release file", "Makefile go.mod", "1 pass, check_release 1 pass", []string{"check", "", "check_release", ""}},
		{"a web file", "web/src/x.ts go.mod", "1 pass, check_web 1 pass", []string{"check", "", "check_web", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ran := commandCalls(t, o, x.Root)
			r.prio = "worker"
			defer r.releaseSlot()
			r.class = Classify(o.Cfg.Data.Merge, strings.Fields(c.files), o.Env.Get("OBOR_FULL") == "1")
			r.mode = r.class.Mode()
			if r.class.Partial || r.mode != "full:forced" {
				t.Fatalf("precondition: class %s, want full:forced", r.mode)
			}
			if h := r.runChecks(); h != nil {
				t.Fatalf("runChecks: %+v\noutput:\n%s", h, x.out.String())
			}
			if !slices.Equal(*ran, c.want) {
				t.Errorf("ran %q, want %q", *ran, c.want)
			}
			if r.check != c.checks {
				t.Errorf("checks %q, want %q", r.check, c.checks)
			}
		})
	}
}

// T-673 (RV-75 #4): the runner tells serve's gate each command as it starts and "" as it ends, so obor status shows
// the gate with no command between two — a finished command is never shown as running.
func TestRunnerCommandEnds(t *testing.T) {
	x := plainFx(t, cmd("check", "true"))
	o := x.opts()
	r, got := commandCalls(t, o, x.Root)
	if _, ok, h := r.exec("check"); !ok || h != nil {
		t.Fatalf("precondition: check ran, got ok %v, halt %+v", ok, h)
	}
	if want := []string{"check", ""}; !slices.Equal(*got, want) {
		t.Fatalf("one command: told %q; want %q", *got, want)
	}
	// Between two commands (here the second exec: in a gate the slot wait or git work) nothing runs.
	r.exec("check")
	if want := []string{"check", "", "check", ""}; !slices.Equal(*got, want) {
		t.Errorf("two commands: told %q; want %q", *got, want)
	}
}

// T-673: a command that times out, and one a signal cuts, end like any other: the runner reports no command running.
func TestRunnerCommandEndsOnTimeoutAndInterrupt(t *testing.T) {
	testkit.Slow(t) // 1 s: the command timeout under test
	x := plainFx(t, cmd("check", map[string]any{"cmd": "sleep 1", "timeout": 1}))
	o := x.opts()
	r, got := commandCalls(t, o, x.Root)
	out, ok, h := r.exec("check")
	if !ok || h != nil || out.rc != 124 {
		t.Fatalf("precondition: check timed out (rc 124), got ok %v, rc %d, halt %+v", ok, out.rc, h)
	}
	if want := []string{"check", ""}; !slices.Equal(*got, want) {
		t.Errorf("timed out: told %q; want %q", *got, want)
	}

	x2 := plainFx(t, cmd("check", "sleep 1"))
	o2 := x2.opts()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r2 := newRunner(ctx, &o2, x2.Root, "T-5", "serve-changed-files")
	var calls []string
	started := make(chan struct{})
	r2.onCommand = func(name string) {
		if name != "" {
			close(started)
		}
		calls = append(calls, name)
	}
	go func() {
		<-started
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, ok, h := r2.exec("check"); !ok || h == nil || !h.signal {
		t.Fatalf("precondition: check was cut by the signal, got ok %v, halt %+v", ok, h)
	}
	if want := []string{"check", ""}; !slices.Equal(calls, want) {
		t.Errorf("interrupted: told %q; want %q", calls, want)
	}
}
