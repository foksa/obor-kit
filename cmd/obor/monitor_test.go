package main

// obor monitor (T-658): the sections at a fixed size, truncation at a small height, one event line per kind of record,
// and the reconnect after serve's socket goes away and comes back.

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"obor/internal/app"
	"obor/internal/config"
	"obor/internal/store"
	"obor/internal/testkit"
)

// statusServe is a fake app socket at path answering every status request with ss (changing ss between calls is the
// next answer).
type statusServe struct {
	ss   app.Status
	ln   net.Listener
	path string
}

// serveStatus answers on path (created): the answer is s.ss at the time of the request.
func serveStatus(t *testing.T, path string, ss app.Status) *statusServe {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := &statusServe{ss: ss, ln: ln, path: path}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = bufio.NewReader(c).ReadBytes('\n')
				out, _ := json.Marshal(map[string]any{"status": s.ss})
				_, _ = c.Write(append(out, '\n'))
			}()
		}
	}()
	t.Cleanup(s.stop)
	return s
}

// stop ends the fake socket and removes its file: a dial then fails as after a serve restart.
func (s *statusServe) stop() {
	s.ln.Close()
	os.Remove(s.path)
}

// addEvent appends one record to the project's store at at (attempt 1).
func addEvent(t *testing.T, root string, at time.Time, kind, task, step string, data any) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	b, _ := json.Marshal(data)
	if _, err := db.Exec(`INSERT INTO events (seq, ts, task, attempt, step, kind, class, data)
		VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM events), ?, ?, 1, ?, ?, '', ?)`,
		at.UTC().Format(time.RFC3339), task, step, kind, string(b)); err != nil {
		t.Fatal(err)
	}
}

// readEvents is the project's newest records, newest first.
func readEvents(t *testing.T, root string, n int) []app.Record {
	t.Helper()
	db, err := store.OpenRO(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recs, err := app.RecentEvents(db, n)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// monitorOf is a monitor of p's project at the fixed clock and the fixed height, in the unknown-width default columns.
func monitorOf(t *testing.T, p *testkit.Project, now time.Time, height int) *monitor {
	t.Helper()
	cfg, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	return &monitor{root: cfg.Root, cfg: cfg, now: func() time.Time { return now }, height: func() int { return height },
		width: func() int { return monitorWidth }}
}

// frameLines is m's frame at now as lines.
func frameLines(t *testing.T, m *monitor, now time.Time) []string {
	t.Helper()
	text := m.frame(now, readEvents(t, m.root, monitorRecent))
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// monitorFx is a project with one committed lane, a served status (one needs-you with its command, one waiting task,
// an open review, a watch, nothing running) and one record of each kind the event list shows.
func monitorFx(t *testing.T, now time.Time) (*testkit.Project, *statusServe) {
	t.Helper()
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	// The lane's task dispatched 4 minutes ago: working, its elapsed time counted from the row.
	p.State("O-01", map[string]any{"lane": "O-01", "task": "T-01", "branch": "task/T-01",
		"dispatched_at": now.Add(-4 * time.Minute).Unix()})
	addEvent(t, p.Root, now.Add(-9*time.Minute), app.KindFact, "T-01", app.StepDispatched,
		map[string]any{"lane": "O-01"})
	addEvent(t, p.Root, now.Add(-8*time.Minute), app.KindFact, "T-01", app.StepSubmitted,
		map[string]any{"lane": "O-01", "report": "Summary: x"})
	addEvent(t, p.Root, now.Add(-7*time.Minute), app.KindResult, "T-01", app.StepGate,
		map[string]any{"outcome": "green", "line": "GREEN T-01"})
	addEvent(t, p.Root, now.Add(-6*time.Minute), app.KindResult, "T-01", "gate.2",
		map[string]any{"outcome": "red", "line": "RED T-01: e2e failed"})
	addEvent(t, p.Root, now.Add(-5*time.Minute), app.KindResult, "T-01", "accept.c1",
		map[string]any{"job": "T-01/1/review", "note": "ok"})
	addEvent(t, p.Root, now.Add(-4*time.Minute), app.KindFact, "T-01", app.StepSendback,
		map[string]any{"note": "fix the test"})
	addEvent(t, p.Root, now.Add(-3*time.Minute), app.KindFact, "T-01", app.StepNeedsYou,
		map[string]any{"lane": "O-01", "reason": "held: decide", "cause": "held"})
	addEvent(t, p.Root, now.Add(-2*time.Minute), app.KindFact, "T-01", app.StepMerged,
		map[string]any{"head": "abcdef0123456789"})
	addEvent(t, p.Root, now.Add(-100*time.Second), app.KindFact, "review", app.StepReviewRequest,
		map[string]any{"after": []string{"T-01"}})
	addEvent(t, p.Root, now.Add(-90*time.Second), app.KindFact, "review", app.StepReviewStarted,
		map[string]any{"id": "RV-3", "head": "abcdef0123456789"})
	addEvent(t, p.Root, now.Add(-80*time.Second), app.KindFact, app.MsgTask, app.StepMessage,
		map[string]any{"to": "pj-lead", "line": "[obor other] hi"})
	ss := app.Status{Since: now.Add(-time.Hour).UTC().Format(time.RFC3339), Connected: true, Project: &app.ProjectStatus{
		Name: "pj", Root: p.Root, Merging: true,
		Tasks: []app.TaskStatus{{Task: "T-02", State: "waiting", After: []string{"T-01"},
			Why: "dispatch T-02: no free lane of the types"}},
		Facts: []app.Record{
			{Seq: 1, TS: now.Add(-4 * time.Minute).UTC().Format(time.RFC3339), Task: "T-09", Step: app.StepNeedsYou,
				Kind: app.KindFact, Data: json.RawMessage(`{"lane":"O-09","reason":"asks: may I?"}`)},
		},
		Watches: []app.WatchStatus{{Task: "T-02", By: "pj-lead", Since: now.Add(-3 * time.Minute).Unix()}},
		Answers: map[int64]string{1: `obor answer T-09 "<text>"`},
		Paused:  &app.Pause{Why: "quota", Caller: "pj-lead"},
		Review:  "review request since 3m (now)",
	}}
	return p, serveStatus(t, app.SocketPath(p.Root), ss)
}

// TestMonitorSections: at 40 rows the sections read top to bottom — serve's line and `running: nothing`, needs-you in
// full with its command, one line per lane (state, task, elapsed, agent), the review and the watch and the queued
// --after dispatch, then the events newest first with one line for each kind of record.
func TestMonitorSections(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p, _ := monitorFx(t, now)
	m := monitorOf(t, p, now, 40)
	text := m.frame(now, readEvents(t, p.Root, monitorRecent))
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 12 {
		t.Fatalf("precondition: a full view, got %d lines:\n%s", len(lines), text)
	}
	if !strings.HasPrefix(lines[0], "serve: up since ") || lines[1] != "running: nothing" {
		t.Errorf("the first two lines %q %q, want serve's and `running: nothing`", lines[0], lines[1])
	}
	for _, want := range []string{
		`needs-you T-09 (O-09): asks: may I? → obor answer T-09 "<text>"`,
		"O-01", "review request since 3m (now)", "watch T-02 since 3m (by pj-lead)",
		"queued T-02: waiting (after T-01): dispatch T-02: no free lane of the types",
		"paused: quota, until cleared (obor pause --clear ends it; no dispatch meanwhile)",
		"dispatched T-01 (O-01)", "submitted T-01", "gate T-01 pass (GREEN T-01)",
		"gate T-01 fail (RED T-01: e2e failed)", "accepted T-01", "sendback T-01: fix the test",
		"held T-01: decide", "merged T-01 abcdef0", "review requested (after T-01)", "review started RV-3",
		"line to pj-lead: [obor other] hi", "serve restarted",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no line %q in\n%s", want, text)
		}
	}
	lane := ""
	for _, l := range lines {
		if strings.HasPrefix(l, "O-01 ") {
			lane = l
		}
	}
	if f := strings.Fields(lane); len(f) < 4 || f[1] != "working" || f[2] != "T-01" || f[3] != "4m" {
		t.Errorf("the lane line %q, want working, its task T-01 and 4m in that state", lane)
	}
	if !strings.HasSuffix(lane, "-") || strings.Contains(lane, "agent") {
		t.Errorf("the lane line %q: no agent runs in the lane, want its place held by -", lane)
	}
	// Section order: serve's lines, needs-you, the lanes, the review and watches and queue, the events.
	at := func(sub string) int { return strings.Index(text, sub) }
	for _, pair := range [][2]string{{"running: nothing", "needs-you T-09"}, {"asks: may I?", "O-01 "},
		{"O-01 ", "review request since"}, {"review request since", "watch T-02 since"},
		{"queued T-02:", "line to pj-lead"}} {
		if a, b := at(pair[0]), at(pair[1]); a < 0 || b < 0 || a > b {
			t.Errorf("section order: %q before %q: %d, %d\n%s", pair[0], pair[1], a, b, text)
		}
	}
	// Newest first, the oldest is serve's own start (an hour ago here).
	if !strings.HasSuffix(lines[len(lines)-1], "serve restarted") {
		t.Errorf("the last event line is %q, want the oldest (serve's start)", lines[len(lines)-1])
	}
	if a, b := at("line to pj-lead"), at("review started RV-3"); a < 0 || b < 0 || a > b {
		t.Errorf("events not newest first: the line at %d, the review start at %d", a, b)
	}
	if a, b := at("review started RV-3"), at("review requested (after T-01)"); a < 0 || b < 0 || a > b {
		t.Errorf("events not newest first: the start at %d, the request at %d", a, b)
	}
}

// TestMonitorPendingLine (T-662): the monitor draws the pending line under the running one — the same projection as
// obor status: a failed step with the age of its failure and its reason, a queued one as queued.
func TestMonitorPendingLine(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	st := statusModel{Serve: serveJSON{Up: true, Herdr: true, Project: true, Merging: true},
		Pending: []app.PendingStep{
			{Step: app.StepAfterMerge, Task: "T-1", Since: now.Add(-time.Hour).Unix(),
				Error: "git I/O: index.lock exists", FailedAt: now.Add(-4 * time.Minute).Unix()},
			{Step: app.StepGate, Task: "T-2", Since: now.Add(-time.Minute).Unix()},
		}}
	sec := monitorSections(st, nil, now, false)[0]
	if len(sec) < 3 || sec[1] != "running: nothing" ||
		sec[2] != "pending: after_merge T-1 (failed 4m ago: git I/O: index.lock exists) · gate T-2 (queued)" {
		t.Errorf("the monitor's serve section %q, want serve's line, `running: nothing` and the pending line", sec)
	}
}

// TestMonitorTruncatesLaterSections: at 6 rows the later sections go first — the events are gone, the last line says
// the view was cut, and serve's line and the needs-you stay.
func TestMonitorTruncatesLaterSections(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p, _ := monitorFx(t, now)
	m := monitorOf(t, p, now, 6)
	lines := frameLines(t, m, now)
	if len(lines) != 6 {
		t.Fatalf("%d lines at height 6:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[0], "serve: up since ") || lines[1] != "running: nothing" {
		t.Errorf("the first two lines %q %q, want serve's and `running: nothing`", lines[0], lines[1])
	}
	if !strings.Contains(strings.Join(lines, "\n"), "needs-you T-09") {
		t.Errorf("the needs-you is gone before the events:\n%s", strings.Join(lines, "\n"))
	}
	if lines[len(lines)-1] != "…" {
		t.Errorf("the last line %q, want the cut marker", lines[len(lines)-1])
	}
	for _, gone := range []string{"dispatched", "submitted", "merged", "line to"} {
		if strings.Contains(strings.Join(lines, "\n"), gone) {
			t.Errorf("an event line (%q) at height 6:\n%s", gone, strings.Join(lines, "\n"))
		}
	}
}

// TestMonitorFitsTerminal: at 80x10 a frame is exactly 10 rows with serve's line first, no row longer than 80 runes (a
// long line is cut with an ellipsis, not wrapped), and no newline after the last row (which would scroll the view one).
func TestMonitorFitsTerminal(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	long := "line to pj-lead: " + strings.Repeat("x", 100)
	if n := utf8.RuneCountInString(long); n <= 80 {
		t.Fatalf("precondition: the event line is %d runes, not longer than the terminal's 80", n)
	}
	p, _ := monitorFx(t, now)
	addEvent(t, p.Root, now.Add(-70*time.Second), app.KindFact, app.MsgTask, app.StepMessage,
		map[string]any{"to": "pj-lead", "line": strings.Repeat("x", 100)})
	m := monitorOf(t, p, now, 10)
	m.width = func() int { return 80 }
	text := m.frame(now, readEvents(t, p.Root, monitorRecent))
	if strings.HasSuffix(text, "\n") {
		t.Errorf("the frame ends in a newline, which scrolls the view one row:\n%s", text)
	}
	lines := strings.Split(text, "\n")
	if len(lines) != 10 {
		t.Fatalf("%d rows at 80x10, want exactly 10:\n%s", len(lines), text)
	}
	if !strings.HasPrefix(lines[0], "serve: up since ") {
		t.Errorf("row 0 is %q, want serve's line first", lines[0])
	}
	cut := ""
	for i, l := range lines {
		if n := utf8.RuneCountInString(l); n > 80 {
			t.Errorf("row %d is %d runes, want at most 80: %q", i, n, l)
		}
		if strings.Contains(l, "line to pj-lead: xxx") {
			cut = l
		}
	}
	if n := utf8.RuneCountInString(cut); cut == "" || n != 80 || !strings.HasSuffix(cut, "…") {
		t.Errorf("the long event row %q: %d runes, want 80 cut with an ellipsis", cut, n)
	}
}

// TestFitSections: sections in order, the later dropped first, the cut one keeping its first lines and the marker.
func TestFitSections(t *testing.T) {
	t.Parallel()
	secs := [][]string{{"a"}, {"b", "c"}, {"d"}, {"e", "f"}}
	if got := fitSections(secs, 4); strings.Join(got, ",") != "a,b,c,…" {
		t.Errorf("height 4 = %q, want a,b,c,…", got)
	}
	if got := fitSections(secs, 6); strings.Join(got, ",") != "a,b,c,d,e,f" {
		t.Errorf("height 6 = %q, want every line", got)
	}
	if got := fitSections(secs, 1); len(got) != 1 || got[0] != "a" {
		t.Errorf("height 1 = %q, want the first line only", got)
	}
}

// TestMonitorReconnects: serve answers, its socket goes away (a restart: "serve: restarting…", not "down"), and comes
// back (serve's line again).
func TestMonitorReconnects(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	ss := app.Status{Since: now.UTC().Format(time.RFC3339), Connected: true,
		Project: &app.ProjectStatus{Name: "pj", Root: p.Root, Merging: true}}
	srv := serveStatus(t, app.SocketPath(p.Root), ss)
	clock := now
	m := monitorOf(t, p, now, 24)
	m.now = func() time.Time { return clock }
	text, drawn := m.tick()
	if !drawn || !strings.HasPrefix(text, "serve: up since ") {
		t.Fatalf("first frame: drawn %v, %q", drawn, text)
	}
	srv.stop()
	clock = clock.Add(3 * time.Second)
	text, drawn = m.tick()
	if !drawn || !strings.Contains(text, "serve: restarting…") {
		t.Fatalf("socket gone: drawn %v\n%s", drawn, text)
	}
	if strings.Contains(text, "serve: down") {
		t.Errorf("a restart reads as a serve that is down:\n%s", text)
	}
	// Gone for good (a minute without an answer): the down line and its error, not a restarting line forever.
	clock = clock.Add(monitorRestart)
	text, _ = m.tick()
	if !strings.Contains(text, "serve: down (") || strings.Contains(text, "restarting") {
		t.Errorf("after %s without an answer:\n%s", monitorRestart, text)
	}
	serveStatus(t, srv.path, ss)
	clock = clock.Add(3 * time.Second)
	text, drawn = m.tick()
	if !drawn || !strings.HasPrefix(text, "serve: up since ") {
		t.Fatalf("socket back: drawn %v\n%s", drawn, text)
	}
	if strings.Contains(text, "restarting") {
		t.Errorf("the restarting line stays after serve answered:\n%s", text)
	}
}

// TestMonitorRedrawsOnRecordsAndTime: no redraw while nothing changed; one on a new record, and one once 2 s passed.
func TestMonitorRedrawsOnRecordsAndTime(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p := testkit.New(t)
	o1 := p.Lane("O-01", "task/T-01")
	p.Config()
	p.Commit(o1)
	serveStatus(t, app.SocketPath(p.Root), app.Status{Since: now.UTC().Format(time.RFC3339), Connected: true,
		Project: &app.ProjectStatus{Name: "pj", Root: p.Root}})
	clock := now
	m := monitorOf(t, p, now, 24)
	m.now = func() time.Time { return clock }
	if _, drawn := m.tick(); !drawn {
		t.Fatal("precondition: the first frame is drawn")
	}
	clock = clock.Add(monitorPoll)
	if _, drawn := m.tick(); drawn {
		t.Error("a frame drawn though nothing changed")
	}
	addEvent(t, p.Root, clock, app.KindFact, "T-01", app.StepSubmitted, map[string]any{"lane": "O-01"})
	if _, drawn := m.tick(); !drawn {
		t.Error("no frame on a new record")
	}
	clock = clock.Add(monitorPoll)
	if _, drawn := m.tick(); drawn {
		t.Error("a frame drawn though nothing changed")
	}
	clock = clock.Add(monitorEvery)
	if _, drawn := m.tick(); !drawn {
		t.Error("no frame once 2 s passed (elapsed times)")
	}
}

// TestMonitorUsage: obor monitor takes no arguments.
func TestMonitorUsage(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	rc, _, stderr := run(t, p.Root, nil, "monitor", "--json")
	if rc != 2 || !strings.Contains(stderr, "usage: obor monitor") {
		t.Errorf("obor monitor --json: exit %d, %q", rc, stderr)
	}
}
