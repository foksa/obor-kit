package app

import (
	"context"
	"encoding/json"
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
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, &Loop{}), nil }
	h.snap.Panes = append(h.snap.Panes,
		port.Pane{ID: "w1:p3", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-advisor", Status: "idle"},
		port.Pane{ID: "w1:p4", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-lead", Status: "idle"},
		port.Pane{ID: "w1:p5", Group: "pj", Cwd: h.root, Agent: "claude", Label: "pj-int", Status: "idle"})
	h.panes.last = h.now // their transcripts' last write: the done stretches start at t0
	h.connect()
	t.Cleanup(func() { h.proj().log.Close(); h.a.closeLog() })
	return h
}

// freshServe is serve restarted: a new App over the same project's log, its observations empty.
func freshServe(t *testing.T, h *harness) {
	t.Helper()
	h.a.closeLog()
	h.a = newTestApp(Options{Root: h.root, Now: func() time.Time { return h.now }}, h.panes, h.lanes)
	h.a.opt.Loop = func(root string) (*Loop, error) { return withCfg(root, &Loop{}), nil }
	h.connect()
}

// deliverOnce is one of serve's line passes over pj: the lead's lines posted to a fake inbox.
func deliverOnce(t *testing.T, h *harness) {
	t.Helper()
	h.a.deliver(context.Background(), h.proj(), withAppCfg(h.a, &Loop{Inbox: &kindSpy{}, Prompter: &linerSpy{}}))
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
		if _, ok := h.a.recordBrief(h.proj(), loadCfg(t, h.root), "pj-advisor", "pj-lead", "answer the question", "", "", false, nil); !ok {
			t.Fatal("precondition: the advisor's brief was not recorded")
		}
	}
	work := func(_ *testing.T, h *harness) {
		h.panes.last = h.now // the transcript's last write: a new stretch starts here
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

// doneLines is pj's done reminder lines' agents in the log's order, each checked for the stop command and the note.
func doneLines(t *testing.T, h *harness) []string {
	t.Helper()
	var out []string
	for _, line := range leadLines(t, h) {
		if !strings.Contains(line, ", no open work — obor restart ") {
			continue
		}
		agent, _, _ := strings.Cut(strings.TrimPrefix(line, "[obor] "), " done ")
		if !strings.HasSuffix(line, "obor restart "+agent+" --stop (the next obor brief starts it fresh)") {
			t.Errorf("line %q, want the stop command for %s and the brief note", line, agent)
		}
		out = append(out, agent)
	}
	return out
}

// hintsOf is obor status' hint by agent in pj now.
func hintsOf(t *testing.T, h *harness) map[string]string {
	t.Helper()
	st := h.a.Status()
	if st.Project == nil {
		t.Fatal("serve holds no project")
	}
	out := map[string]string{}
	for _, as := range st.Project.Agents {
		out[as.Name] = as.Hint
	}
	return out
}

// The done stretch is the agent's own last activity, so it outlives serve: a fresh process (a new App over the same
// log, empty observations) dates the same stretch the same. A reminder sent for it is not sent again, frequent restarts
// do not postpone the first, a written transcript starts a new stretch, and unknown activity is no stretch at all.
func TestRoleDoneStretchOutlivesServe(t *testing.T) {
	t.Parallel()
	t.Run("reminded once, transcript unchanged: no second line", func(t *testing.T) {
		t.Parallel()
		h := doneRig(t)
		h.now = h.now.Add(roleDoneAfter + time.Minute)
		deliverOnce(t, h)
		if got := doneLines(t, h); !slices.Equal(got, []string{"pj-advisor", "pj-int"}) {
			t.Fatalf("the first pass's lines %q, want one per role agent", got)
		}
		freshServe(t, h)
		h.now = h.now.Add(roleDoneAfter + time.Minute)
		deliverOnce(t, h)
		if got := doneLines(t, h); !slices.Equal(got, []string{"pj-advisor", "pj-int"}) {
			t.Errorf("after a fresh serve and another hour: lines %q, want no second line for the same stretch", got)
		}
	})
	t.Run("restarted every 30 min for 2h: the first line still at t0+1h", func(t *testing.T) {
		t.Parallel()
		h := doneRig(t)
		t0 := h.now
		for i := 1; i <= 4; i++ {
			h.now = t0.Add(time.Duration(i) * 30 * time.Minute)
			freshServe(t, h)
			deliverOnce(t, h)
			if i == 1 {
				if got := doneLines(t, h); len(got) != 0 {
					t.Fatalf("after 30 min and a restart: lines %q, want none", got)
				}
			}
		}
		if got := doneLines(t, h); !slices.Equal(got, []string{"pj-advisor", "pj-int"}) {
			t.Errorf("lines after restarts every 30 min: %q, want the first at t0+1h, once", got)
		}
		if got := hintsOf(t, h); got["pj-advisor"] != "obor restart pj-advisor --stop" {
			t.Errorf("status hint after a fresh serve %q, want the stop command", got["pj-advisor"])
		}
	})
	t.Run("works, then idle another hour: a second line", func(t *testing.T) {
		t.Parallel()
		h := doneRig(t)
		h.now = h.now.Add(roleDoneAfter + time.Minute)
		deliverOnce(t, h)
		h.panes.last = h.now // the transcript is written: a new stretch starts here
		h.work("w1:p3", true)
		h.work("w1:p5", true)
		h.work("w1:p3", false)
		h.work("w1:p5", false)
		freshServe(t, h)
		h.now = h.now.Add(roleDoneAfter + time.Minute)
		deliverOnce(t, h)
		want := []string{"pj-advisor", "pj-int", "pj-advisor", "pj-int"}
		if got := doneLines(t, h); !slices.Equal(got, want) {
			t.Errorf("lines %q, want a second per role agent: %q", got, want)
		}
	})
	t.Run("unknown activity: no line and no hint", func(t *testing.T) {
		t.Parallel()
		h := doneRig(t)
		h.panes.last = time.Time{} // a kind whose transcripts obor cannot read
		h.now = h.now.Add(roleDoneAfter + time.Minute)
		freshServe(t, h)
		deliverOnce(t, h)
		if got := doneLines(t, h); len(got) != 0 {
			t.Errorf("lines %q, want none for unknown activity", got)
		}
		if got := hintsOf(t, h); got["pj-advisor"] != "" || got["pj-int"] != "" {
			t.Errorf("hints %q, want none for unknown activity", got)
		}
	})
}

// While the terminal is gone, obor status offers no stop hint (its text and JSON both print the field) and serve sends
// no line from the panes it kept; a reconnect decides from the agents' activity afresh, with no stale timer carried
// over.
func TestRoleDoneDisconnected(t *testing.T) {
	t.Parallel()
	h := doneRig(t)
	h.now = h.now.Add(roleDoneAfter + time.Minute) // connected, this would be a hint and a line
	h.a.mu.Lock()
	h.a.connected = false
	h.a.mu.Unlock()
	deliverOnce(t, h)
	if got := doneLines(t, h); len(got) != 0 {
		t.Errorf("lines while disconnected %q, want none", got)
	}
	st := h.a.Status()
	if st.Connected {
		t.Fatal("precondition: status says connected while the terminal is gone")
	}
	for _, as := range st.Project.Agents {
		if as.Hint != "" {
			t.Errorf("%s's hint while disconnected %q, want none", as.Name, as.Hint)
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"hint"`) {
		t.Errorf("status JSON while disconnected carries a hint: %s", raw)
	}

	// The agent writes its transcript while serve is away: the reconnect dates the stretch from it, not from the pane
	// serve saw last.
	h.panes.last = h.now
	h.connect()
	if got := hintsOf(t, h); got["pj-advisor"] != "" || got["pj-int"] != "" {
		t.Errorf("hints right after the reconnect %q, want none: the stretch starts at the new activity", got)
	}
	deliverOnce(t, h)
	if got := doneLines(t, h); len(got) != 0 {
		t.Errorf("lines right after the reconnect %q, want none", got)
	}
	h.now = h.now.Add(roleDoneAfter + time.Minute)
	if got := hintsOf(t, h); got["pj-advisor"] != "obor restart pj-advisor --stop" {
		t.Errorf("hint an hour after the reconnect %q, want the stop command", got["pj-advisor"])
	}
	deliverOnce(t, h)
	if got := doneLines(t, h); !slices.Equal(got, []string{"pj-advisor", "pj-int"}) {
		t.Errorf("lines an hour after the reconnect %q, want one per role agent", got)
	}
}
