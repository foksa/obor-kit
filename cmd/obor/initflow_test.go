package main

import (
	"bytes"
	"context"
	"testing"
)

// Init ok: the lead starts once; init FAIL: its rc, no lead.
func TestInitFlowStartsTheLeadOnlyWhenInitSucceeds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		initRC, wantRC int
		wantLeads      int
	}{{"init ok", 0, 0, 1}, {"init fails", 1, 1, 0}} {
		initRuns, leads := 0, 0
		f := initFlow{
			init: func(args []string) int { initRuns++; return tc.initRC },
			lead: func(context.Context) int { leads++; return 0 },
		}
		if rc := f.run(context.Background(), nil); rc != tc.wantRC || initRuns != 1 || leads != tc.wantLeads {
			t.Errorf("%s: exit %d, init runs %d, leads %d", tc.name, rc, initRuns, leads)
		}
	}
}

// The lead's rc is init's: a lead start that fails fails obor init.
func TestInitFlowReturnsTheLeadsRC(t *testing.T) {
	t.Parallel()
	f := initFlow{init: func([]string) int { return 0 }, lead: func(context.Context) int { return 3 }}
	if rc := f.run(context.Background(), nil); rc != 3 {
		t.Errorf("exit %d, want the lead's 3", rc)
	}
}

// A lead already running (a rerun of init): OK runs, nothing touched. The lead start is obor lead's (leadRun).
func TestInitFlowWithTheLeadRunningSaysOKRuns(t *testing.T) {
	t.Parallel()
	x := newLeadFx(t)
	x.f.Agent("w1:p1", "pj-lead", "working", x.p.Root).Label = "pj-lead"
	before := len(x.srv.Requests())
	var out bytes.Buffer
	f := initFlow{
		init: func([]string) int { return 0 },
		lead: func(ctx context.Context) int {
			r := leadRun{dir: x.p.Root, kit: x.p.Base, env: x.p.Env(), t: fakeTerminal(x.srv.Client(), x.p.Env()), out: &out, tune: x.launcher}
			return r.run(ctx, nil)
		},
	}
	if rc := f.run(context.Background(), nil); rc != 0 || lastLine(out.String()) != "OK pj-lead runs (pane w1:p1)" {
		t.Fatalf("exit %d\n%s", rc, out.String())
	}
	for _, r := range x.srv.Requests()[before:] {
		if r.Method == "tab.create" || r.Method == "agent.start" {
			t.Errorf("a running lead was touched: %s", r.Method)
		}
	}
}
