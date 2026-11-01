package main

// obor monitor (T-658): the project's live view in the "system" tab of its herdr workspace. It renders the project's
// view (view.Build, as obor status does) redrawn in place — serve's line with what it does without an agent, needs-you,
// the lanes and the role agents, the queue (T-664) — plus the store's recent events, fitted to the terminal's height. It redraws on every new record in the store and at least
// every 2 s (elapsed times), survives a serve restart ("serve: restarting…" until the socket answers again), and ends
// on Ctrl-C. Read-only, any role; no TUI library: clear the screen, write the lines.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/view"
)

// The monitor's cadence and bounds.
const (
	monitorPoll   = 250 * time.Millisecond // how often the store is asked for a new record
	monitorEvery  = 2 * time.Second        // a redraw at least this often: elapsed times
	monitorAsk    = 2 * time.Second        // one status request (serve answers at once, or is not there)
	monitorEvents = 60                     // worded events in the view at one redraw (more than a terminal's rows)
	monitorHeight = 24                     // rows drawn when the terminal's height is unknown
	monitorWidth  = 120                    // columns drawn when the terminal's width is unknown
	// monitorRestart is how long a serve that stopped answering reads as restarting, not as down (launchd starts the
	// new build at once; a serve that is gone for good shows its error then).
	monitorRestart = time.Minute
)

// frameWriter is where the monitor draws: a writer it can flush after each frame.
type frameWriter interface {
	Write(p []byte) (int, error)
	Flush() error
}

// monitor is one project's live view: its config, the store it polls and the last frame's state.
type monitor struct {
	root   string
	cfg    *config.Config
	height func() int // the terminal's rows
	width  func() int // the terminal's columns
	now    func() time.Time

	db    *sql.DB   // the store, read-only, open while it reads
	seq   int64     // the newest record drawn
	drawn time.Time // when the last frame was drawn
	// seen: serve answered at least once (then a failure is a restart); lost: when it stopped answering, so a serve that
	// is gone for good reads as down again after monitorRestart.
	seen bool
	lost time.Time
}

// monitorCmd is `obor monitor`.
func monitorCmd(args []string) int {
	if len(args) != 0 {
		errf("usage: obor monitor")
		return 2
	}
	_, cfg, rc := load()
	if rc != 0 {
		return rc
	}
	m := &monitor{root: cfg.Root, cfg: cfg, height: terminalRows, width: terminalCols, now: time.Now}
	defer m.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	m.run(ctx, out)
	return 0
}

// run draws the view until ctx ends (Ctrl-C), a frame at once and then one for each new record, and at least every
// monitorEvery.
func (m *monitor) run(ctx context.Context, w frameWriter) {
	for {
		if text, drawn := m.tick(); drawn {
			drawFrame(w, text)
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(w)
			_ = w.Flush()
			return
		case <-time.After(monitorPoll):
		}
	}
}

// tick redraws the view when the store has a new record or monitorEvery passed since the last frame: drawn says
// whether text is a frame to draw.
func (m *monitor) tick() (text string, drawn bool) {
	now := m.now()
	seq := m.newest()
	if seq == m.seq && !m.drawn.IsZero() && now.Sub(m.drawn) < monitorEvery {
		return "", false
	}
	m.seq, m.drawn = seq, now
	return m.frame(now), true
}

// frame is the view at now: serve's answer (or the restarting line after one stopped answering), the lanes as git and
// the store say, the queue and the recent events, fitted to the terminal's rows and cut to its columns, with no newline
// after the last row.
func (m *monitor) frame(now time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), monitorAsk)
	ss, err := app.Ask(ctx, app.SocketPath(m.root))
	restarting := false
	switch {
	case err == nil:
		m.seen, m.lost = true, time.Time{}
	case m.seen:
		if m.lost.IsZero() {
			m.lost = now
		}
		restarting = now.Sub(m.lost) < monitorRestart
	}
	if m.db == nil {
		m.db = openStore(m.root) // nil until the store is written: no specs, no events
	}
	table := checks.NewLaneTable(m.cfg)
	table.Now = now.Unix()
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: table, DB: m.db, Now: now, Events: monitorEvents})
	if !restarting {
		st.Serve.PID, st.Serve.Launchd = serveProcess(ctx, m.cfg, st.Serve.Up)
	}
	cancel()
	lines := fitSections(monitorSections(st, now, restarting), m.rows())
	width := m.cols()
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(clip(l, width))
	}
	return b.String()
}

// newest is the seq of the store's newest record (0: no store yet, or no records). A read that fails closes the store,
// so the next tick opens it again (a replaced or upgraded file); a project whose store is not written yet is checked
// for it each tick. The store stays open for the frame's view.
func (m *monitor) newest() int64 {
	if m.db == nil {
		if m.db = openStore(m.root); m.db == nil {
			return 0
		}
	}
	recs, err := app.RecentEvents(m.db, 1)
	if err != nil {
		m.closeDB()
		return 0
	}
	if len(recs) > 0 {
		return recs[0].Seq
	}
	return 0
}

// closeDB closes the store once its read failed; close closes it for good.
func (m *monitor) closeDB() {
	if m.db != nil {
		m.db.Close()
		m.db = nil
	}
}

func (m *monitor) close() { m.closeDB() }

// rows is the terminal's height in rows (monitorHeight when unknown).
func (m *monitor) rows() int {
	if n := m.height(); n > 0 {
		return n
	}
	return monitorHeight
}

// cols is the terminal's width in columns (monitorWidth when unknown).
func (m *monitor) cols() int {
	if n := m.width(); n > 0 {
		return n
	}
	return monitorWidth
}

// drawFrame clears the terminal and writes frame.
func drawFrame(w frameWriter, frame string) {
	fmt.Fprintf(w, "\033[H\033[2J%s", frame)
	_ = w.Flush()
}

// terminalSize is the size of the terminal obor monitor draws on (stdout) in rows and columns: 0, 0 when it is not one.
func terminalSize() (rows, cols int) {
	var ws struct{ rows, cols, xpix, ypix uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0, 0
	}
	return int(ws.rows), int(ws.cols)
}

// terminalRows is the height of the terminal obor monitor draws on (stdout), 0 when it is not one.
func terminalRows() int {
	rows, _ := terminalSize()
	return rows
}

// terminalCols is the width of the terminal obor monitor draws on (stdout), 0 when it is not one.
func terminalCols() int {
	_, cols := terminalSize()
	return cols
}

// monitorSections is the monitor's view as sections, top to bottom (T-658): serve's line and the running line; the
// needs-you, in full, with their commands; one line per lane and one for the role agents; the queue (T-664: the merge
// loop, the waiting dispatches, the review, watches, lines and briefs, the pause, the nightly check); the recent events,
// newest first.
func monitorSections(st view.Project, now time.Time, restarting bool) [][]string {
	serve := serveSection(st, now)
	if restarting {
		serve = []string{"serve: restarting…"}
	}
	return [][]string{serve, needsYouSection(st), monitorLaneSection(st, now), queueSection(st, now),
		eventSection(st.Events)}
}

// monitorLaneSection is section 3: one line per lane — its state column (T-657: working, gating, merging, reviewing,
// held, free …), the task, how long the lane has been in that state, and its agent idle or working — then the role
// agents on one line (none while serve is down: their state is not known).
func monitorLaneSection(st view.Project, now time.Time) []string {
	out := make([]string, 0, len(st.Lanes)+1)
	for _, l := range st.Lanes {
		since := "-"
		if l.Since != nil {
			since = app.Age(now.Sub(time.Unix(*l.Since, 0)))
		}
		agent := "-"
		if l.Agent != "" {
			agent = l.Agent
			if l.AgentStatus != "" {
				agent += " " + l.AgentStatus
			}
		}
		out = append(out, fmt.Sprintf("%-6s  %-9s  %-8s  %7s  %s", l.Lane, l.Column, orDash(l.Task), since, agent))
	}
	if len(st.Roles) > 0 {
		items := make([]string, 0, len(st.Roles))
		for _, r := range st.Roles {
			item := r.Agent + " no pane"
			if r.Status != "" {
				item = r.Agent + " " + r.Status + " " + app.Age(time.Duration(r.Age)*time.Second)
			}
			if b := r.Brief; b != nil {
				item += ", briefed " + app.Age(max(now.Sub(time.Unix(b.Since, 0)), 0)) + " ago"
			}
			items = append(items, item)
		}
		out = append(out, "roles   "+strings.Join(items, " · "))
	}
	return out
}

// eventSection is section 5: the recent events, newest first, one line each with its local time.
func eventSection(ev []view.Event) []string {
	out := make([]string, 0, len(ev))
	for _, e := range ev {
		out = append(out, e.At.Local().Format("15:04:05")+"  "+e.Line)
	}
	return out
}

// fitSections is sections in order, cut to height rows: the later ones are dropped first — the section that does not
// fit keeps its first lines and one "…" replaces the last line when something was left out.
func fitSections(sections [][]string, height int) []string {
	if height <= 0 {
		return nil
	}
	var out []string
	for _, s := range sections {
		room := height - len(out)
		if len(s) <= room {
			out = append(out, s...)
			continue
		}
		out = append(out, s[:room]...)
		if len(out) == height && height > 1 {
			out[len(out)-1] = "…"
		}
		return out
	}
	return out
}

// clip is line cut to width runes with an ellipsis at the cut: a line longer than the terminal is cut, never wrapped.
func clip(line string, width int) string {
	if width <= 0 {
		return line
	}
	r := []rune(line)
	if len(r) <= width {
		return line
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
