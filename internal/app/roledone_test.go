package app

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
)

// doneRig is pj with a lead (pj-lead), an integrator (pj-int) and an advisor (pj-advisor), all idle in their panes
// besides lane L-01's worker pj-l-01, connected: the agents a done reminder may name, and the two it never names.
func doneRig(t *testing.T) *harness {
	t.Helper()
	h, _ := newIdentHarness(t)
	h.withLead()
	h.a.opt.Loop = func(string) (*Loop, error) { return &Loop{}, nil }
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:p3", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-advisor", Status: "idle"},
		port.Pane{ID: "w1:p4", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-lead", Status: "idle"},
		port.Pane{ID: "w1:p5", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-int", Status: "idle"})
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	return h
}

// deliverOnce is one of serve's line passes over pj: the lead's lines posted to a fake inbox.
func deliverOnce(t *testing.T, h *harness) {
	t.Helper()
	h.a.deliver(context.Background(), h.proj(), &Loop{Inbox: &kindSpy{}, Prompter: &linerSpy{}})
}

// The done reminder, one table: a role agent idle or done for roleDoneAfter with no open work gets one line to the
// lead, once per done stretch; a short stretch, open work, a stop and a restart send none; a lane's worker and the
// lead never.
func TestRoleDoneReminder(t *testing.T) {
	t.Parallel()
	pass := func(d time.Duration) lineStep {
		return func(t *testing.T, h *harness) {
			h.now = h.now.Add(d)
			deliverOnce(t, h)
		}
	}
	brief := func(t *testing.T, h *harness) {
		if _, ok := h.a.recordBrief(h.proj(), "pj-advisor", "pj-lead", "answer the question", "", "", false, nil); !ok {
			t.Fatal("precondition: the advisor's brief was not recorded")
		}
	}
	work := func(_ *testing.T, h *harness) {
		h.work("w1:p3", true)
		h.work("w1:p5", true)
	}
	back := func(_ *testing.T, h *harness) {
		h.work("w1:p3", false)
		h.work("w1:p5", false)
	}
	stopped := func(_ *testing.T, h *harness) {
		h.status("w1:p3", func(p *port.Pane) { p.Agent = "" }) // at its shell: obor restart pj-advisor --stop
	}
	flip := func(t *testing.T, h *harness) { // a viewed reply: done becomes idle (or the reverse), the same stretch
		h.now = h.now.Add(30 * time.Minute)
		for _, id := range []string{"w1:p3", "w1:p5"} {
			h.status(id, func(p *port.Pane) { p.Status = "done" })
		}
		deliverOnce(t, h)
	}
	cases := []struct {
		name  string
		steps []lineStep
		lines []string // the role agents' done lines, in order
	}{
		{name: "done 59 min: none", steps: []lineStep{pass(59 * time.Minute)}},
		{name: "done 61 min: one line, once", steps: []lineStep{pass(61 * time.Minute), pass(0), pass(time.Hour)},
			lines: []string{"pj-advisor", "pj-int"}},
		{name: "open work (an unfinished brief): none", steps: []lineStep{brief, pass(61 * time.Minute)},
			lines: []string{"pj-int"}},
		{name: "works again, then done another hour: a second line",
			steps: []lineStep{pass(61 * time.Minute), work, back, pass(61 * time.Minute)},
			lines: []string{"pj-advisor", "pj-int", "pj-advisor", "pj-int"}},
		{name: "stopped: none", steps: []lineStep{stopped, pass(61 * time.Minute)}, lines: []string{"pj-int"}},
		{name: "an idle/done flip is one stretch", steps: []lineStep{flip, pass(31 * time.Minute)},
			lines: []string{"pj-advisor", "pj-int"}},
		{name: "a restart sends none again", steps: []lineStep{pass(61 * time.Minute), restartRig, pass(0)},
			lines: []string{"pj-advisor", "pj-int"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := doneRig(t)
			for _, s := range c.steps {
				s(t, h)
			}
			var got []string
			for _, line := range leadLines(t, h) {
				if !strings.Contains(line, ", no open work — obor restart ") {
					continue
				}
				agent, _, _ := strings.Cut(strings.TrimPrefix(line, "[obor] "), " done ")
				got = append(got, agent)
				if !strings.Contains(line, "the next obor brief starts it fresh") || !strings.HasSuffix(line,
					"obor restart "+agent+" --stop (the next obor brief starts it fresh)") {
					t.Errorf("line %q, want the stop command for %s and the brief note", line, agent)
				}
			}
			if !slices.Equal(got, c.lines) {
				t.Errorf("done lines %q, want %q", got, c.lines)
			}
			if len(c.lines) > 0 {
				want := roleDoneLine(c.lines[0], roleDoneAfter+time.Minute)
				if first := firstDoneLine(t, h); first != want {
					t.Errorf("the first done line %q, want %q", first, want)
				}
			}
		})
	}
}

// firstDoneLine is pj's first done reminder in the log.
func firstDoneLine(t *testing.T, h *harness) string {
	t.Helper()
	for _, line := range leadLines(t, h) {
		if strings.Contains(line, ", no open work — obor restart ") {
			return line
		}
	}
	return ""
}

// obor status' hint ends a qualifying role agent's line with the command that stops it; no other agent's line has
// one, and before roleDoneAfter none does.
func TestStatusDoneRoleHint(t *testing.T) {
	t.Parallel()
	h := doneRig(t)
	stopped := func() map[string]string {
		out := map[string]string{}
		for _, as := range h.a.Status().Project.Agents {
			out[as.Name] = as.Hint
		}
		return out
	}
	if got := stopped(); got["pj-advisor"] != "" || got["pj-int"] != "" {
		t.Fatalf("hints before roleDoneAfter: %q, want none", got)
	}
	h.now = h.now.Add(roleDoneAfter + time.Minute)
	got := stopped()
	for _, agent := range []string{"pj-advisor", "pj-int"} {
		if want := "obor restart " + agent + " --stop"; got[agent] != want {
			t.Errorf("%s's hint %q, want %q", agent, got[agent], want)
		}
	}
	for _, agent := range []string{"pj-l-01", "pj-lead"} {
		if got[agent] != "" {
			t.Errorf("%s's hint %q, want none", agent, got[agent])
		}
	}
}
