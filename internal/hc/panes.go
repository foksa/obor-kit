package hc

import (
	"context"
	"strconv"

	"obor/internal/agents"
	"obor/internal/app/port"
)

// Record is hc's terminal record (the schema's Terminal) as far as obor reads it: the one struct of that part of the
// protocol, so the client decodes and hctest's core sends the same fields. A null hc sends reads as the zero value.
type Record struct {
	Address string            `json:"address"` // t7: this terminal forever on this core
	State   string            `json:"state"`   // live, finished
	Reason  string            `json:"reason,omitempty"`
	PID     int               `json:"pid,omitempty"` // the terminal's own program: the shell of a shell terminal
	Cwd     string            `json:"cwd"`           // where it started (after a lost holder: its last cwd)
	Labels  map[string]string `json:"labels"`
	// Reported: the program ever sent an OSC 7501 report (it stays true after the program exits).
	Reported   bool        `json:"reported"`
	Status     *Status     `json:"status"` // nil: the program never reported, or it exited
	Foreground *Foreground `json:"foreground,omitempty"`
}

// Status is one OSC 7501 record: the program's own word (idle, working, done, blocked, error), the blocked kind
// (permission, question, auth), its message and the app that reported it (claude-code, codex, opencode).
type Status struct {
	State string `json:"state"`
	Kind  string `json:"kind,omitempty"`
	Msg   string `json:"msg,omitempty"`
	App   string `json:"app,omitempty"`
}

// Foreground is what runs in the terminal now: the foreground process group's leader and its cwd (it follows a cd).
type Foreground struct {
	PID  int      `json:"pid"`
	Name string   `json:"name"`
	Argv []string `json:"argv"`
	Cwd  string   `json:"cwd"`
}

// pane is r as the core sees it. Group and Label are the obor.project and obor.agent labels; Cwd the foreground's
// (it follows a cd), else the record's; Run the terminal's own program's pid (a fresh shell after a lost holder or a
// stop and start has another). The agent is what the program reported: its kind the one whose agents report
// status.app (kindOf; never foreground.name, which is Claude's version), its status hc's word, with `error`
// as idle (the turn ended, the agent takes a prompt again). With no status, a program other than the terminal's own
// in the foreground is unknown; the terminal's own program (its shell) is no agent.
func (r Record) pane() port.Pane {
	p := port.Pane{ID: r.Address, Terminal: r.Address, Group: r.Labels[LabelProject], Label: r.Labels[LabelAgent], Cwd: r.Cwd}
	if r.PID != 0 {
		p.Run = strconv.Itoa(r.PID)
	}
	if f := r.Foreground; f != nil && f.Cwd != "" {
		p.Cwd = f.Cwd
	}
	switch {
	case r.Status != nil:
		p.Agent = kindOf(r.Status.App)
		p.Status = statusWord(r.Status.State)
	case r.Foreground != nil && r.Foreground.PID != r.PID:
		p.Status = "unknown"
	}
	return p
}

// kindOf is the kind whose agents report app as their name in a status (agents.Caps.App, else the kind's name); ""
// when no kind does.
func kindOf(app string) string {
	if app == "" {
		return ""
	}
	for _, n := range agents.Names() {
		k, ok := agents.Lookup(n)
		if !ok {
			continue
		}
		if a := k.Caps().App; a == app || a == "" && n == app {
			return n
		}
	}
	return ""
}

// statusWord is hc's status word as the port's: idle, working, blocked and done as they are, error as idle, any
// other word unknown.
func statusWord(s string) string {
	switch s {
	case "idle", "working", "blocked", "done":
		return s
	case "error":
		return "idle"
	}
	return "unknown"
}

// Panes is port.Panes over the core: every live terminal of it is a pane, its Group its obor.project label.
type Panes struct {
	C *Client
}

var _ port.Panes = Panes{}

// serverStatus is server.status as far as obor reads it: the core's permanent id.
type serverStatus struct {
	Core struct {
		ID string `json:"id"`
	} `json:"core"`
}

// snapshot is the port's snapshot of recs (finished terminals left out), with the core's version, its id as the
// instance (a restart, an update and a stop keep it) and note. Each pane's Run is its program's pid (PaneRuns): what
// tells serve's restore which panes lost their program.
func snapshot(h hello, st serverStatus, recs []Record) port.Snapshot {
	out := port.Snapshot{Version: h.Version, Instance: st.Core.ID, Note: note(h), PaneRuns: true}
	for _, r := range recs {
		if r.State == "live" {
			out.Panes = append(out.Panes, r.pane())
		}
	}
	return out
}

// Snapshot is the core now: server.status and terminal.list on one connection.
func (p Panes) Snapshot(ctx context.Context) (port.Snapshot, error) {
	var out port.Snapshot
	err := p.C.do(ctx, "terminal.list", func(ctx context.Context, s *session) error {
		var st serverStatus
		if err := s.call(ctx, "server.status", nil, &st); err != nil {
			return err
		}
		var l struct {
			Terminals []Record `json:"terminals"`
		}
		if err := s.call(ctx, "terminal.list", nil, &l); err != nil {
			return err
		}
		out = snapshot(s.hello, st, l.Terminals)
		return nil
	})
	return out, err
}

// Notify: hc shows no notifications (HD6), so every toast is not shown.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	return &port.NotShown{Reason: "hc has no notifications"}
}
