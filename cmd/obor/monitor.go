package main

// obor monitor (T-658): the project's live view in the "system" tab of its herdr workspace. It is obor status redrawn
// in place — serve's line with what it does without an agent, needs-you, the lanes, the open review and watches — plus
// the store's recent events, fitted to the terminal's height. It redraws on every new record in the store and at least
// every 2 s (elapsed times), survives a serve restart ("serve: restarting…" until the socket answers again), and ends
// on Ctrl-C. Read-only, any role; no TUI library: clear the screen, write the lines.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/store"
)

// The monitor's cadence and bounds.
const (
	monitorPoll   = 250 * time.Millisecond // how often the store is asked for a new record
	monitorEvery  = 2 * time.Second        // a redraw at least this often: elapsed times
	monitorAsk    = 2 * time.Second        // one status request (serve answers at once, or is not there)
	monitorRecent = 200                    // records read for the event list at one redraw
	monitorHeight = 24                     // rows drawn when the terminal's size is unknown
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
	height func() int
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
	m := &monitor{root: cfg.Root, cfg: cfg, height: terminalHeight, now: time.Now}
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
	recs, seq := m.recent()
	if seq == m.seq && !m.drawn.IsZero() && now.Sub(m.drawn) < monitorEvery {
		return "", false
	}
	m.seq, m.drawn = seq, now
	return m.frame(now, recs), true
}

// frame is the view at now: serve's answer (or the restarting line after one stopped answering), the lanes as git and
// the store say, and the recent events, fitted to the terminal's height.
func (m *monitor) frame(now time.Time, recs []app.Record) string {
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
	table := checks.NewLaneTable(m.cfg)
	table.Now = now.Unix()
	st := buildStatus(table, ss, err)
	if !restarting {
		st.Serve.PID, st.Serve.Launchd = serveProcess(ctx, m.cfg, st.Serve.Up)
	}
	cancel()
	ev := eventsOf(recs, st)
	var b strings.Builder
	for _, l := range fitSections(monitorSections(st, ev, now, restarting), m.rows()) {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}

// recent is the store's newest records, newest first, and the newest one's seq (0: no store yet, or no records). A read
// that fails closes the store, so the next tick opens it again (a replaced or upgraded file); a project whose store is
// not written yet is checked for it each tick.
func (m *monitor) recent() (recs []app.Record, seq int64) {
	if m.db == nil {
		if _, err := os.Stat(store.File(m.root)); err != nil {
			return nil, 0
		}
		db, err := store.OpenRO(store.File(m.root))
		if err != nil {
			return nil, 0
		}
		m.db = db
	}
	recs, err := app.RecentEvents(m.db, monitorRecent)
	if err != nil {
		m.closeDB()
		return nil, 0
	}
	if len(recs) > 0 {
		return recs, recs[0].Seq
	}
	return recs, 0
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

// drawFrame clears the terminal and writes frame.
func drawFrame(w frameWriter, frame string) {
	fmt.Fprintf(w, "\033[H\033[2J%s", frame)
	_ = w.Flush()
}

// terminalHeight is the height of the terminal obor monitor draws on (stdout), 0 when it is not one.
func terminalHeight() int {
	var ws struct{ rows, cols, xpix, ypix uint16 }
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)))
	if errno != 0 {
		return 0
	}
	return int(ws.rows)
}

// monitorSections is the monitor's view as sections, top to bottom (T-658): serve's line and the running line; the
// needs-you, in full, with their commands; one line per lane; the open review, the watches and the dispatches queued
// on --after tasks; the recent events, newest first.
func monitorSections(st statusModel, ev []monitorEvent, now time.Time, restarting bool) [][]string {
	serve := serveSection(st, now)
	if restarting {
		serve = []string{"serve: restarting…"}
	}
	return [][]string{serve, needsYouSection(st), monitorLaneSection(st, now), attentionSection(st, now), eventSection(ev)}
}

// monitorLaneSection is section 3: one line per lane — its state column (T-657: working, gating, merging, reviewing,
// held, free …), the task, how long the lane has been in that state, and its agent idle or working.
func monitorLaneSection(st statusModel, now time.Time) []string {
	out := make([]string, 0, len(st.Lanes))
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
		out = append(out, fmt.Sprintf("%-6s  %-9s  %-8s  %7s  %s", l.Lane, laneState(l, st), orDash(l.Task), since, agent))
	}
	return out
}

// attentionSection is section 4: the open review request (or the "review due" line), the lead's open watches, the
// dispatches queued behind their --after tasks, and the pause in force (a project that reads normal while it dispatches
// nothing would mislead the owner).
func attentionSection(st statusModel, now time.Time) []string {
	var out []string
	if st.Review != "" {
		out = append(out, st.Review)
	}
	for _, w := range st.Watches {
		line := "watch " + w.Task + " since " + app.Age(now.Sub(time.Unix(w.Since, 0)))
		if w.By != "" {
			line += " (by " + w.By + ")"
		}
		out = append(out, line)
	}
	for _, t := range st.Tasks {
		if t.State == "waiting" && len(t.After) > 0 {
			out = append(out, "queued "+t.Task+": waiting (after "+strings.Join(t.After, ", ")+")"+why(t))
		}
	}
	if st.Paused != nil {
		out = append(out, strings.TrimSuffix(pauseLine(st.Paused), "\n"))
	}
	return out
}

// monitorEvent is one line of the event list: its time and its text.
type monitorEvent struct {
	At   time.Time
	Line string
}

// eventSection is section 5: the recent events, newest first, one line each with its local time.
func eventSection(ev []monitorEvent) []string {
	out := make([]string, 0, len(ev))
	for _, e := range ev {
		out = append(out, e.At.Local().Format("15:04:05")+"  "+e.Line)
	}
	return out
}

// eventsOf is what the monitor lists of the log: the facts and results of a dispatch, a submission, a gate, a
// verdict, a hold, a merge, a review and a line posted, then serve itself while it is up (its restart is the store's
// oldest, newest-state fact). Newest first.
func eventsOf(recs []app.Record, st statusModel) []monitorEvent {
	ev := make([]monitorEvent, 0, len(recs)+1)
	for _, r := range recs {
		if line := eventLine(r); line != "" {
			ev = append(ev, monitorEvent{At: recordTS(r), Line: line})
		}
	}
	if st.Serve.Up && !st.Serve.sinceTime.IsZero() {
		ev = append(ev, monitorEvent{At: st.Serve.sinceTime, Line: "serve restarted"})
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].At.After(ev[j].At) })
	return ev
}

// eventData is what the monitor reads of a record's data: each kind of record fills the fields it has.
type eventData struct {
	Lane    string   `json:"lane"`
	Reason  string   `json:"reason"`
	Cause   string   `json:"cause"`
	Outcome string   `json:"outcome"`
	Line    string   `json:"line"`
	Note    string   `json:"note"`
	Head    string   `json:"head"`
	After   []string `json:"after"`
	ID      string   `json:"id"`
	To      string   `json:"to"`
}

// eventLine is r as the event list words it ("" for a record the monitor does not list): dispatched, submitted, a
// gate's pass or fail with its summary, accepted, a sendback, a hold, a merge with its sha, a review's request and
// start, or a line posted to an agent.
func eventLine(r app.Record) string {
	var d eventData
	if len(r.Data) > 0 {
		_ = json.Unmarshal(r.Data, &d)
	}
	if r.Kind == app.KindResult {
		if baseStep(r.Step) == app.StepGate {
			if d.Outcome == "green" {
				return "gate " + r.Task + " pass (" + d.Line + ")"
			}
			return "gate " + r.Task + " fail (" + d.Line + ")"
		}
		if method, _, _ := strings.Cut(r.Step, "."); method == app.MethodAccept {
			return "accepted " + r.Task
		} else if method == app.MethodSendback {
			return "sendback " + r.Task + noteIn(d.Note)
		}
		return ""
	}
	if r.Kind != app.KindFact {
		return ""
	}
	switch r.Step {
	case app.StepDispatched:
		return "dispatched " + r.Task + laneIn(d.Lane)
	case app.StepSubmitted:
		return "submitted " + r.Task
	case app.StepSendback:
		return "sendback " + r.Task + noteIn(d.Note)
	case app.StepNeedsYou:
		if d.Cause == app.CauseHeld {
			return "held " + r.Task + noteIn(strings.TrimPrefix(d.Reason, "held: "))
		}
	case app.StepMerged:
		if d.Head == "" {
			return "merged " + r.Task + " (outside obor serve)"
		}
		return "merged " + r.Task + " " + d.Head[:min(7, len(d.Head))]
	case app.StepReviewRequest:
		if len(d.After) > 0 {
			return "review requested (after " + strings.Join(d.After, ",") + ")"
		}
		return "review requested"
	case app.StepReviewStarted:
		return "review started " + d.ID
	case app.StepReviewRm:
		return "review request ended"
	case app.StepMessage:
		return "line to " + d.To + ": " + d.Line
	}
	return ""
}

// laneIn is " (O-01)" for a dispatch's lane, "" without one.
func laneIn(lane string) string {
	if lane == "" {
		return ""
	}
	return " (" + lane + ")"
}

// noteIn is ": note" for a sendback's or hold's why, "" without one.
func noteIn(note string) string {
	if note == "" {
		return ""
	}
	return ": " + note
}

// baseStep is a numbered step's step (gate.2 → gate; a verdict's accept.<call id> stays whole).
func baseStep(step string) string {
	if i := strings.LastIndexByte(step, '.'); i > 0 {
		if _, err := strconv.Atoi(step[i+1:]); err == nil {
			return step[:i]
		}
	}
	return step
}

// recordTS is r's time (zero when unreadable).
func recordTS(r app.Record) time.Time {
	t, _ := time.Parse(time.RFC3339, r.TS)
	return t
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
