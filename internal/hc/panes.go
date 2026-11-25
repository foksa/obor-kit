package hc

import (
	"cmp"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"unicode"

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
	Keep    bool              `json:"keep"`          // restarts as a shell after a loss
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
// status.app (kindOf, of kinds: the project's; never foreground.name, which is Claude's version), else the obor.kind
// label obor's start set,
// its status hc's word, with `error` as idle (the turn ended, the agent takes a prompt again; its msg is logged where
// a wait reads it), and its Note what the status says (Status.note). With no status, a program other than the
// terminal's own in the foreground is unknown; the terminal's own program (its shell) is no agent.
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
		p.Agent = cmp.Or(kindOf(kinds, r.Status.App), r.Labels[LabelKind])
		p.Status, p.Note = statusWord(r.Status.State), r.Status.note()
	case r.programRuns():
		p.Status = "unknown"
	}
	return p
}

// programRuns: a program other than the terminal's own (a shell terminal's shell) holds the foreground.
func (r Record) programRuns() bool {
	return r.Foreground != nil && r.Foreground.PID != r.PID
}

// kindOf is the kind of kinds whose agents report app as their name in a status (agents.Caps.App, else the kind's
// name); "" when no kind does.
func kindOf(kinds agents.Kinds, app string) string {
	if app == "" {
		return ""
	}
	for _, n := range kinds.Names() {
		k, ok := kinds.Lookup(n)
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

// note is s as port.Pane.Note: blocked its kind and msg ("permission: approve Bash: ls", either alone when the other
// is empty), error "error: <msg>" (or "error"), any other word its msg ("" when none); each part and the whole shown
// (sanitized). The word itself decides (statusWord); the note is shown only.
func (s *Status) note() string {
	kind, msg := sanitized(s.Kind), sanitized(s.Msg)
	switch s.State {
	case "blocked":
		return sanitized(joined(kind, msg))
	case "error":
		return sanitized(joined("error", msg))
	}
	return msg
}

// noteMax is the most runes a note shows: a longer one is cut to it, its last rune "…".
const noteMax = 160

// sanitized is s, text an agent wrote, as obor shows it on one line: control characters (C0, DEL, C1) dropped,
// whitespace runs one space, trimmed, at most noteMax runes ("…" ending a cut one). No markup is read or written:
// a page escapes it as any text.
func sanitized(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r): // C0, DEL and C1
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > noteMax {
		s = strings.TrimRight(string(rs[:noteMax-1]), " ") + "…"
	}
	return s
}

// Panes is port.Panes over the core: every live terminal of it is a pane, its Group its obor.project label.
type Panes struct {
	C *Client
	// Core, when set, is serve's hold on the core (env.go): each Subscribe first starts a core that is down and refuses
	// one whose environment holds a HERDR_ or OBOR_ name. nil: a subscribe only dials.
	Core *Core
	// Notifier shows a macOS notification (the seam of Notify): nil is osascript. GOOS names the platform Notify acts
	// for: "" is this one.
	Notifier func(ctx context.Context, title, body string) error
	GOOS     string
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

// Notify: hc has no notifications of its own (HD6), so on darwin the toast is a macOS notification with the same title
// and body, bounded by ctx; on another platform, or when the notification fails, it is a port.NotShown with the reason.
func (p Panes) Notify(ctx context.Context, title, body string) error {
	if cmp.Or(p.GOOS, runtime.GOOS) != "darwin" {
		return &port.NotShown{Reason: "hc has no notifications outside macOS"}
	}
	notify := p.Notifier
	if notify == nil {
		notify = osascript
	}
	if err := notify(ctx, title, body); err != nil {
		return &port.NotShown{Reason: "osascript: " + err.Error()}
	}
	return nil
}

// notifyScript is the script osascript runs: its title and body are its arguments, never part of its source.
var notifyScript = []string{
	"on run argv",
	"display notification (item 2 of argv) with title (item 1 of argv)",
	"end run",
}

// osascript posts the notification: the title and body follow the script as its arguments (after `--`, so a title
// that starts with a dash is no option). A failure carries osascript's own message.
func osascript(ctx context.Context, title, body string) error {
	args := make([]string, 0, 2*len(notifyScript)+3)
	for _, l := range notifyScript {
		args = append(args, "-e", l)
	}
	args = append(args, "--", title, body)
	out, err := exec.CommandContext(ctx, "osascript", args...).CombinedOutput()
	if err != nil {
		if m := strings.TrimSpace(string(out)); m != "" {
			return errors.New(m)
		}
		return err
	}
	return nil
}
