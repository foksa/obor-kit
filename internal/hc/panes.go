package hc

import (
	"cmp"
	"context"
	"strconv"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/osnotify"
	"obor/internal/progstatus"
)

// Record is hc's terminal record (the schema's Terminal) as far as obor reads it: the one struct of that part of the
// protocol, so the client decodes and hctest's core sends the same fields. A null hc sends reads as the zero value.
type Record struct {
	Address string            `json:"address"` // t7: this terminal forever on this core
	State   string            `json:"state"`   // live, finished
	Reason  string            `json:"reason,omitempty"`
	PID     int               `json:"pid,omitempty"` // the terminal's own program: the shell of a shell terminal
	Cwd     string            `json:"cwd"`           // where it started (after a lost holder: its last cwd)
	Keep    bool              `json:"keep"`          // restarts as a shell after a loss
	Labels  map[string]string `json:"labels"`
	// Reported: the program ever sent an OSC 7501 report (it stays true after the program exits).
	Reported   bool        `json:"reported"`
	Status     *Status     `json:"status"` // nil: the program never reported, or it exited
	Foreground *Foreground `json:"foreground,omitempty"`
}

// Status is one OSC 7501 record: the program's own word (idle, working, done, blocked, error), the blocked kind
// (permission, question, auth), its message and the app that reported it (claude-code, codex, opencode,
// antigravity).
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
// status.app (agents.ByApp, of kinds: the project's; never foreground.name, which is Claude's version), else the
// obor.kind label obor's start set,
// its status and note what the report makes of them (progstatus.Report: its word, with `error` as idle, and its
// note). With no status, a program other than the terminal's own in the foreground is unknown; the terminal's own
// program (its shell) is no agent.
func (r Record) pane(kinds agents.Kinds) port.Pane {
	p := port.Pane{ID: r.Address, Terminal: r.Address, Group: r.Labels[LabelProject], Label: r.Labels[LabelAgent], Cwd: r.Cwd}
	if r.PID != 0 {
		p.Run = strconv.Itoa(r.PID)
	}
	if f := r.Foreground; f != nil && f.Cwd != "" {
		p.Cwd = f.Cwd
	}
	switch {
	case r.Status != nil:
		rep := r.Status.report()
		p.Agent = cmp.Or(agents.ByApp(kinds, rep.App), r.Labels[LabelKind])
		p.Status, p.Note = rep.Word(), rep.Note()
	case r.programRuns():
		p.Status = "unknown"
	}
	return p
}

// programRuns: a program other than the terminal's own (a shell terminal's shell) holds the foreground.
func (r Record) programRuns() bool {
	return r.Foreground != nil && r.Foreground.PID != r.PID
}

// report is s decoded into the terminal-neutral report: what obor makes of a program's status (progstatus).
func (s *Status) report() progstatus.Report {
	return progstatus.Report{State: s.State, Kind: s.Kind, Msg: s.Msg, App: s.App}
}

// Panes is port.Panes over the core: every live terminal of it is a pane, its Group its obor.project label.
type Panes struct {
	C *Client
	// Core, when set, is serve's hold on the core (env.go): each Subscribe first starts a core that is down and refuses
	// one whose environment holds another terminal's identity. nil: a subscribe only dials.
	Core *Core
	// Notifier shows Notify's notification (osnotify: on macOS, osascript).
	Notifier osnotify.Notifier
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
func snapshot(kinds agents.Kinds, h hello, st serverStatus, recs []Record) port.Snapshot {
	out := port.Snapshot{Version: h.Version, Instance: st.Core.ID, Note: note(h), PaneRuns: true}
	for _, r := range recs {
		if r.State == "live" {
			out.Panes = append(out.Panes, r.pane(kinds))
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
		out = snapshot(p.C.kinds(), s.hello, st, l.Terminals)
		return nil
	})
	return out, err
}

// Note is the adapter's note on a core that lacks a call obor makes (Snapshot's Note), for obor setup show: one
// snapshot.
func (p Panes) Note(ctx context.Context) (string, error) {
	snap, err := p.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return snap.Note, nil
}

// Notify: hc has no notifications of its own (HD6), so the toast is the OS's notification with the same title and body
// (osnotify), bounded by ctx; when none shows, it is a port.NotShown with the reason.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	if err := p.Notifier.Show(ctx, title, body); err != nil {
		return &port.NotShown{Reason: "hc: " + err.Error()}
	}
	return nil
}
