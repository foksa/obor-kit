package main

// obor monitor (T-658, T-668): the project's live view in the "system" tab of its herdr workspace. It renders the
// project's view (view.Build, as obor status does; the look is monitor_render.go) — a header, needs-you, the lanes and
// the role agents, the queue (T-664), the store's recent events — fitted to the terminal. It draws on the alternate
// screen with the cursor hidden (nothing it draws reaches the scrollback), redraws in place by cursor-home and a
// clear-to-end-of-line per row, only when the frame's text changed, and refits at once on a resize. It looks at the
// store for a new record every poll and redraws at least every 2 s (elapsed times), survives a serve restart ("serve
// restarting…" until the socket answers again), and ends on Ctrl-C (no other keys). NO_COLOR or a pipe: plain frames.
// Read-only, any role; no TUI library.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"obor/internal/app/event"
	"obor/internal/app/wire"
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
	warn   string       // config.LoadReader's warning ("": none)
	cfgAt  [2]time.Time // mtimes of .obor/config.json and .obor/local.json when cfg was read (T-713)
	height func() int   // the terminal's rows
	width  func() int   // the terminal's columns
	now    func() time.Time
	color  bool // paint the frame (a tty and no NO_COLOR)

	db    *sql.DB   // the store, read-only, open while it reads
	seq   int64     // the newest record drawn
	drawn time.Time // when the last frame was drawn
	size  [2]int    // the rows and columns it was drawn for
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
	cfg, warn, rc := loadReader()
	if rc != 0 {
		return rc
	}
	rows, _ := terminalSize()
	tty := rows > 0
	m := &monitor{root: cfg.Root, cfg: cfg, warn: warn, cfgAt: configMtimes(cfg.Root), height: terminalRows, width: terminalCols, now: time.Now,
		color: tty && os.Getenv("NO_COLOR") == ""}
	defer m.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	s := &screen{w: out, tty: tty}
	s.enter()
	defer s.leave()
	m.run(ctx, s, winch)
	return 0
}

// run draws the view until ctx ends (Ctrl-C), a frame at once and then one for each new record, and at least every
// monitorEvery; a resize (winch) refits it at once.
func (m *monitor) run(ctx context.Context, s *screen, winch <-chan os.Signal) {
	for {
		if text, drawn := m.tick(); drawn {
			s.show(text, m.rows(), m.cols())
		}
		select {
		case <-ctx.Done():
			return
		case <-winch:
			m.drawn = time.Time{}
		case <-time.After(monitorPoll):
		}
	}
}

// tick builds the view when the store has a new record, monitorEvery passed since the last frame or the terminal's
// size changed: drawn says whether text is a frame to show (the screen writes it only when it differs from the last).
func (m *monitor) tick() (text string, drawn bool) {
	now := m.now()
	seq := m.newest()
	size := [2]int{m.rows(), m.cols()}
	if seq == m.seq && size == m.size && !m.drawn.IsZero() && now.Sub(m.drawn) < monitorEvery {
		return "", false
	}
	m.seq, m.drawn, m.size = seq, now, size
	return m.frame(now), true
}

// frame is the view at now: serve's answer (or the restarting line after one stopped answering), the lanes as git and
// the store say, the queue and the recent events, fitted to the terminal's rows and cut to its columns, with no newline
// after the last row.
func (m *monitor) frame(now time.Time) string {
	ctx, cancel := context.WithTimeout(context.Background(), monitorAsk)
	ss, err := wire.Ask(ctx, wire.SocketPath(m.root))
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
	m.reloadConfig()
	table := checks.NewLaneTable(m.cfg)
	table.Now = now.Unix()
	st := view.Build(view.Input{Status: ss, Err: err, Lanes: table, DB: m.db, Now: now, Events: monitorEvents,
		Notes: view.Notes(m.root, m.warn)})
	if !restarting {
		st.Serve.PID, st.Serve.Launchd = serveProcess(ctx, m.cfg, st.Serve.Up)
	}
	cancel()
	lines := fitFrame(st, renderOpts{name: projectName(m.root), now: now, restarting: restarting, color: m.color},
		m.rows(), m.cols())
	return strings.Join(lines, "\n")
}

// configMtimes is the modification times of the config files (zero: missing).
func configMtimes(root string) (at [2]time.Time) {
	for i, name := range []string{"config.json", "local.json"} {
		if fi, err := os.Stat(filepath.Join(root, ".obor", name)); err == nil {
			at[i] = fi.ModTime()
		}
	}
	return at
}

// reloadConfig reads the config again when .obor/config.json or .obor/local.json changed since it was read (T-713: a
// role added while the monitor runs shows up). It reads as the readers do (an unknown field warns); a read that fails
// keeps the last good config and says so in the frame's notes, until a read succeeds.
func (m *monitor) reloadConfig() {
	at := configMtimes(m.root)
	if at == m.cfgAt {
		return
	}
	m.cfgAt = at
	cfg, warn, err := config.LoadReader(m.root)
	if err != nil {
		m.warn = "WARN config not reloaded, keeping the last good one: " + strings.TrimPrefix(err.Error(), "obor: ")
		return
	}
	m.cfg, m.warn = cfg, warn
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
	recs, err := event.RecentEvents(m.db, 1)
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

// The terminal control the screen writes (a tty only).
const (
	altOn  = "\033[?1049h\033[?25l" // the alternate screen, the cursor hidden
	altOff = "\033[?25h\033[?1049l" // the cursor back, the main screen back
	home   = "\033[H"
	toEOL  = "\033[K"
	below  = "\033[J"
)

// screen is where frames land. On a tty: the alternate screen (nothing drawn reaches the scrollback), a frame written
// from the home position, each row followed by clear-to-end-of-line and the last by clear-below — no full clear, no
// newline after the last row — and only when it differs from the last one. Not a tty: the plain text of each changed
// frame, a newline after it, no escape at all.
type screen struct {
	w    frameWriter
	tty  bool
	last string
	size [2]int
	any  bool // a frame was shown
}

// enter switches a tty to the alternate screen and hides the cursor.
func (s *screen) enter() {
	if s.tty {
		fmt.Fprint(s.w, altOn)
		_ = s.w.Flush()
	}
}

// leave restores the cursor and the main screen.
func (s *screen) leave() {
	if s.tty {
		fmt.Fprint(s.w, altOff)
		_ = s.w.Flush()
	}
}

// show draws text (rows joined by newlines) for a terminal of rows by cols; the same text at the same size writes
// nothing.
func (s *screen) show(text string, rows, cols int) {
	size := [2]int{rows, cols}
	if s.any && text == s.last && size == s.size {
		return
	}
	s.last, s.size, s.any = text, size, true
	if !s.tty {
		fmt.Fprintln(s.w, text)
		_ = s.w.Flush()
		return
	}
	var b strings.Builder
	b.WriteString(home)
	for i, l := range strings.Split(text, "\n") {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(l)
		b.WriteString(toEOL)
	}
	b.WriteString(below)
	_, _ = s.w.Write([]byte(b.String()))
	_ = s.w.Flush()
}
