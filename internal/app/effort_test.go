package app_test

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-695: effort beside the model. A model ref `opus:high` (the built-in hard level, a spec's Model: line, --model):
// the dispatch records and replies the effort, the worker's start request carries it, and status shows it.

// effortRig is a dispatch rig whose lane L-01 (agent pj-l-01) is free.
func effortRig(t *testing.T, edits ...func(map[string]any)) *loopRig {
	t.Helper()
	r := newLoopRig(t, dispatching(), withConfig(edits...))
	r.setLane(func(l *port.Lane) { l.Agent = "pj-l-01" })
	return r
}

// launchOf is the first Agents.Launch of agent (ok false: none yet).
func (r *loopRig) launchOf(agent string) (port.Launch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range r.launches {
		if l.Agent == agent {
			return l, true
		}
	}
	return port.Launch{}, false
}

func TestEffortHardLevelStartsAnOpusLaneAtHigh(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("hard") // the built-in hard level is opus:high
	reply, err := r.dispatchLevel("d1")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued) as opus effort high") {
		t.Fatalf("hard dispatch: %q, %v; want L-01 at effort high", reply, err)
	}
	if d := r.factData("T-1", event.StepDispatched); d["effort"] != "high" || d["lane"] != "L-01" || d["type"] != "opus:high" {
		t.Errorf("dispatch fact %v, want effort high and type opus:high on L-01 (T-721)", d)
	}
	r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
	if row, ok := r.p.Row("L-01"); !ok || row.Task != "T-1" || row.Type != "opus:high" {
		t.Errorf("lane row %+v (%v), want T-1 with type opus:high (T-721)", row, ok)
	}
	if l, _ := r.launchOf("pj-l-01"); l.Effort != "high" || !l.Fresh || l.Type != "opus" {
		t.Errorf("worker start %+v, want a fresh start of the dispatch's type opus at effort high (T-722)", l)
	}
	if st := r.taskStatus("T-1"); st.Effort != "high" {
		t.Errorf("status %+v, want effort high", st)
	}
}

// T-695: a spec's Model: line or --model names the effort; a plain type runs the kind's default (no effort).
func TestEffortFromTheSpecAndTheFlag(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, spec, typ, want, effort string
		flag, exact                   bool
	}{
		{"the spec's Model: line", "# T-1 — Typed\n\nDifficulty: medium\nModel: opus:max\n", "", "DISPATCHED T-1 → L-01 (queued) as opus effort max", "max", false, false},
		{"--model with an effort", "", "opus:low", "DISPATCHED T-1 → L-01 (queued) as opus effort low", "low", true, false},
		{"a plain type", "", "opus", "DISPATCHED T-1 → L-01 (queued) as opus", "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := effortRig(t)
			r.start()
			if c.spec != "" {
				r.setSpec("s1", c.spec)
			} else {
				r.specLevel("medium")
			}
			var reply string
			var err error
			if c.flag {
				reply, err = r.dispatchLevel("d1", c.typ)
			} else {
				reply, err = r.dispatchLevel("d1")
			}
			if err != nil || (c.exact && reply != c.want) || (!c.exact && !strings.HasPrefix(reply, c.want)) {
				t.Fatalf("dispatch: %q, %v; want %q", reply, err, c.want)
			}
			d := r.factData("T-1", event.StepDispatched)
			wantType := "opus"
			if c.effort != "" {
				wantType += ":" + c.effort
			}
			if d["type"] != wantType || (c.effort == "" && d["effort"] != nil) || (c.effort != "" && d["effort"] != c.effort) {
				t.Errorf("dispatch fact %v, want type %s with effort %q", d, wantType, c.effort)
			}
			r.waitFor("the worker's start", func() bool { _, ok := r.launchOf("pj-l-01"); return ok })
			if l, _ := r.launchOf("pj-l-01"); l.Effort != c.effort || l.Type != "opus" {
				t.Errorf("worker start %+v, want type opus and effort %q", l, c.effort)
			}
		})
	}
}

func TestEffortRefusals(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	if _, err := r.dispatchLevel("d1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
		t.Errorf("--types opus:turbo: %v; want a refusal naming claude's efforts", err)
	}
	if n := r.taskStep("T-1", "fact dispatch"); n != 0 {
		t.Errorf("a refused dispatch recorded %d dispatch facts", n)
	}
	set := func(callID, typ string) error {
		_, err := wire.Verdict(context.Background(), r.sock, wire.MethodSpecSet, wire.Call{Root: r.p.Root, Role: "lead",
			CallID: callID, Task: "T-2", Spec: "# T-2 — A\n\nDifficulty: medium\nModel: " + typ + "\n"})
		return err
	}
	if err := set("s1", "opus:turbo"); err == nil || !strings.Contains(err.Error(), `effort "turbo" is not one of claude's`) {
		t.Errorf("spec set Model: opus:turbo: %v; want a refusal", err)
	}
	if err := set("s2", "ghost:high"); err == nil || !strings.Contains(err.Error(), `"ghost" is not a model`) {
		t.Errorf("spec set Model: ghost:high: %v; want the unknown model refused", err)
	}
	if err := set("s3", "opus:high"); err != nil {
		t.Errorf("spec set Model: opus:high: %v", err)
	}
}

// T-705: dispatch's reply warns when a Claude lane's worktree has no enabled obor plugin (its worker would have no
// obor MCP tools), and still dispatches; a lane with the plugin enabled gets no warning (TestEffortPlainTypeNamesNone).
func TestDispatchWarnsAClaudeLaneWithoutThePlugin(t *testing.T) {
	r := effortRig(t)
	r.start()
	r.specLevel("medium")
	settings := filepath.Join(r.p.LaneDir("L-01"), ".claude", "settings.json")
	if err := os.Remove(settings); err != nil {
		t.Fatalf("precondition: the lane had the plugin enabled: %v", err)
	}
	reply, err := r.dispatchLevel("d1", "opus")
	if err != nil || !strings.HasPrefix(reply, "DISPATCHED T-1 → L-01 (queued)") ||
		!strings.Contains(reply, "WARN lane L-01 has no enabled obor plugin") {
		t.Fatalf("dispatch without the plugin: %q, %v; want DISPATCHED … and the WARN", reply, err)
	}
}
