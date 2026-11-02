package main

// The monitor's look and its drawing (T-668): plain golden frames at 80x24 and 120x40, painted frames that fit and
// balance their escapes, no escape under NO_COLOR or a pipe, an unchanged frame writes nothing, nothing clears the
// whole screen or ends in a newline, and a resize redraws at the new size.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/view"
)

// goldenNow is the fixture's clock (UTC; the events are worded in UTC too).
var goldenNow = time.Date(2026, 11, 1, 14, 30, 0, 0, time.UTC)

// goldenView is a busy project: serve up in launchd, a gate running, one lane per state color, a needs-you, two roles,
// a full queue and events.
func goldenView() view.Project {
	now := goldenNow
	at := func(ago time.Duration) int64 { return now.Add(-ago).Unix() }
	ptr := func(s string) *string { return &s }
	since := func(ago time.Duration) *int64 { n := at(ago); return &n }
	ev := func(ago time.Duration, line string) view.Event {
		return view.Event{At: now.Add(-ago), Line: line}
	}
	return view.Project{
		Serve: view.Serve{Up: true, Herdr: true, Project: true, Merging: true, PID: 4821, Launchd: "running",
			SinceTime: now.Add(-6 * time.Hour)},
		Running: []view.Running{{Step: event.StepGate, Task: "T-660", Command: "make -s check", Since: at(72 * time.Second)}},
		NeedsYou: []view.NeedsYou{{Task: "T-640", Lane: "O-02", Reason: "held: open questions: which table?",
			Answer: `obor resume T-640 "<decision>" | obor drop O-02 "<why>"`, Since: at(48 * time.Minute)}},
		Lanes: []view.Lane{
			{Lane: "O-01", Task: ptr("T-659"), Column: "working", Since: since(12 * time.Minute), Agent: "ob-o-01",
				AgentStatus: "working", SpecRev: 2, Job: &view.Job{ID: "T-659/1/work", Delivered: true}},
			{Lane: "O-02", Task: ptr("T-640"), Column: "held", Since: since(48 * time.Minute), Agent: "ob-o-02",
				AgentStatus: "idle", SpecRev: 1},
			{Lane: "O-03", Task: ptr("T-660"), Column: "gating", Since: since(72 * time.Second)},
			{Lane: "S-01", Column: "free"},
		},
		Roles: []view.Role{
			{Role: "integrator", Agent: "ob-int", Status: "idle", Age: 7200},
			{Role: "reviewer", Agent: "ob-terra", Status: "working", Age: 300,
				Brief: &app.BriefStatus{To: "ob-terra", Since: at(12 * time.Minute)}},
			{Role: "advisor", Agent: "ob-fable"},
		},
		Queue: view.Queue{
			Merge: []view.MergeItem{{Task: "T-660", Lane: "O-03", State: "gating", Stage: view.StageGating},
				{Task: "T-661", State: "submitted", Stage: view.StageSubmitted}},
			Waiting: []view.WaitItem{{Task: "T-664", WaitsOn: view.WaitsAfter, After: []string{"T-662"}},
				{Task: "T-665", WaitsOn: view.WaitsLane, Level: "medium", Types: []string{"opus", "sonnet"}}},
			Pause: &view.Pause{Reason: "quota"},
		},
		Events: []view.Event{
			ev(2*time.Second, "gate T-660 pass (ok 212 tests, 71s)"),
			ev(30*time.Second, "submitted T-660"),
			ev(5*time.Minute, "gate T-659 fail (RED T-659: e2e failed)"),
			ev(9*time.Minute, "held T-640: open questions: which table?"),
			ev(20*time.Minute, "dispatched T-659 (O-01)"),
			ev(40*time.Minute, "merged T-658 abcdef0"),
			ev(50*time.Minute, "accepted T-658"),
			ev(55*time.Minute, "dispatched T-640 (O-02)"),
			ev(70*time.Minute, "review started RV-3"),
			ev(80*time.Minute, "serve restarted"),
		},
	}
}

// goldenFrame is the fixture at cols x rows as the monitor draws it.
func goldenFrame(st view.Project, rows, cols int, color bool) []string {
	return fitFrame(st, renderOpts{name: "obor-kit", now: goldenNow, color: color, loc: time.UTC}, rows, cols)
}

// TestMonitorGoldenFrames: the plain frames at 80x24 and 120x40 are the golden texts (UPDATE_GOLDEN=1 rewrites them).
func TestMonitorGoldenFrames(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		rows, cols int
	}{{"monitor_80x24.txt", 24, 80}, {"monitor_120x40.txt", 40, 120}} {
		got := strings.Join(goldenFrame(goldenView(), c.rows, c.cols, false), "\n") + "\n"
		file := filepath.Join("testdata", c.name)
		if os.Getenv("UPDATE_GOLDEN") != "" {
			if err := os.WriteFile(file, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("%s differs from the frame:\n--- got\n%s--- want\n%s", c.name, got, want)
		}
		if strings.Contains(got, "\033") {
			t.Errorf("%s: a plain frame has an escape", c.name)
		}
	}
}

var sgr = regexp.MustCompile(`\033\[[0-9;]*m`)

// TestMonitorColorFramesFit: painted, every row is at most the columns wide (escapes not counted) at several sizes,
// the rows number at most the height, and every row's escapes balance: each opening one is closed by a reset before the
// row ends.
func TestMonitorColorFramesFit(t *testing.T) {
	t.Parallel()
	painted := false
	for _, size := range [][2]int{{24, 80}, {40, 120}, {10, 40}, {6, 20}, {30, 200}} {
		rows, cols := size[0], size[1]
		lines := goldenFrame(goldenView(), rows, cols, true)
		if len(lines) == 0 || len(lines) > rows {
			t.Errorf("%dx%d: %d rows", cols, rows, len(lines))
		}
		for i, l := range lines {
			if w := visibleWidth(l); w > cols {
				t.Errorf("%dx%d row %d is %d wide: %q", cols, rows, i, w, l)
			}
			open := 0
			for _, e := range sgr.FindAllString(l, -1) {
				painted = true
				if e == escReset {
					open--
				} else {
					open++
				}
				if open < 0 || open > 1 {
					t.Errorf("%dx%d row %d: escapes nest or close twice: %q", cols, rows, i, l)
				}
			}
			if open != 0 {
				t.Errorf("%dx%d row %d leaves an escape open: %q", cols, rows, i, l)
			}
			if strings.Contains(sgr.ReplaceAllString(l, ""), "\033") {
				t.Errorf("%dx%d row %d has an escape that is not a color: %q", cols, rows, i, l)
			}
		}
	}
	if !painted {
		t.Error("no escape in any painted frame: the precondition (color on) is not met")
	}
}

// TestMonitorSectionOrderAndNeedsYou: the header and NEEDS YOU come first, NEEDS YOU is absent when nothing needs the
// owner, and a state dot is green for working, yellow for gating, red for held, dim for free.
func TestMonitorSectionOrderAndNeedsYou(t *testing.T) {
	t.Parallel()
	st := goldenView()
	text := strings.Join(goldenFrame(st, 40, 120, false), "\n")
	last := -1
	for _, title := range []string{"obor-kit", "NEEDS YOU", "LANES", "ROLES", "QUEUE", "EVENTS"} {
		i := strings.Index(text, title)
		if i < 0 || i < last {
			t.Errorf("section %q at %d, after %d:\n%s", title, i, last, text)
		}
		last = i
	}
	st.NeedsYou = nil
	if text := strings.Join(goldenFrame(st, 40, 120, false), "\n"); strings.Contains(text, "NEEDS YOU") {
		t.Errorf("an empty NEEDS YOU is drawn:\n%s", text)
	}
	p := palette{on: true}
	for col, want := range map[string]string{"working": "32", "gating": "33", "held": "31", "free": "2"} {
		if got := p.state(col, dot); got != "\033["+want+"m"+dot+escReset {
			t.Errorf("state %s dot %q, want code %s", col, got, want)
		}
	}
}

// TestMonitorEventsRedByBad: an event row is painted red by the view's bad flag only: a green gate whose summary
// counts a fail is not red, a red gate, a hold and any other event the view marks bad are (T-688).
func TestMonitorEventsRedByBad(t *testing.T) {
	t.Parallel()
	now := goldenNow
	st := view.Project{Events: []view.Event{
		{At: now.Add(-2 * time.Second), Line: "gate T-1 pass (check 25 pass, 0 fail)"},
		{At: now.Add(-time.Minute), Line: "gate T-2 fail (RED T-2: e2e failed)", Bad: true},
		{At: now.Add(-2 * time.Minute), Line: "held T-3: decide", Bad: true},
		{At: now.Add(-3 * time.Minute), Line: "stalled T-4", Bad: true},
	}}
	rows := eventRows(st, renderOpts{now: now, color: true, cols: 80, loc: time.UTC}, palette{on: true})
	cases := []struct {
		line string
		red  bool
	}{
		{"gate T-1 pass (check 25 pass, 0 fail)", false},
		{"gate T-2 fail (RED T-2: e2e failed)", true},
		{"held T-3: decide", true},
		{"stalled T-4", true},
	}
	if len(rows) != 1+len(cases) {
		t.Fatalf("%d rows, want the title and one per event:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	for i, c := range cases {
		row := rows[i+1]
		if !strings.Contains(row, c.line) {
			t.Errorf("row %d %q, want %q", i, row, c.line)
		}
		if got := strings.Contains(row, "\033[31m"); got != c.red {
			t.Errorf("row %d red %v, want %v: %q", i, got, c.red, row)
		}
	}
}

// TestMonitorClipsByDisplayWidth: a wide (two-column) rune counts two, an escape none, and the cut line ends in an
// ellipsis within the width.
func TestMonitorClipsByDisplayWidth(t *testing.T) {
	t.Parallel()
	if got := clip("日本語日本語", 7); got != "日本語…" || visibleWidth(got) != 7 {
		t.Errorf("clip of wide runes = %q (%d wide), want 日本語… in 7 columns", got, visibleWidth(got))
	}
	painted := "\033[32m" + strings.Repeat("a", 30) + escReset
	got := clip(painted, 10)
	if visibleWidth(got) != 10 || !strings.HasSuffix(got, escReset+"…") {
		t.Errorf("clip of a painted line = %q, want 10 columns, the paint closed before the ellipsis", got)
	}
	if got := clip("short", 10); got != "short" {
		t.Errorf("a line that fits was changed: %q", got)
	}
}

// writes records what the screen wrote, per flush.
type writes struct {
	bytes.Buffer
	flushes int
}

func (w *writes) Flush() error { w.flushes++; return nil }

// TestScreenWritesOnlyChanges: an unchanged frame writes nothing; a changed one, or the same text at a new size, does.
func TestScreenWritesOnlyChanges(t *testing.T) {
	t.Parallel()
	for _, tty := range []bool{true, false} {
		w := &writes{}
		s := &screen{w: w, tty: tty}
		s.show("a\nb", 24, 80)
		if w.Len() == 0 {
			t.Fatalf("tty %v: precondition: the first frame writes", tty)
		}
		w.Reset()
		s.show("a\nb", 24, 80)
		if w.Len() != 0 {
			t.Errorf("tty %v: an unchanged frame wrote %q", tty, w.String())
		}
		s.show("a\nc", 24, 80)
		if w.Len() == 0 {
			t.Errorf("tty %v: a changed frame wrote nothing", tty)
		}
		w.Reset()
		s.show("a\nc", 30, 100)
		if w.Len() == 0 {
			t.Errorf("tty %v: a resize wrote nothing", tty)
		}
	}
}

// TestScreenNeverClearsOrScrolls: a tty frame starts at home, clears each row to its end and the rest below, never
// the whole screen (\033[2J, which writes into the scrollback), and ends in no newline; the alternate screen is entered
// and left; a pipe gets no escape.
func TestScreenNeverClearsOrScrolls(t *testing.T) {
	t.Parallel()
	w := &writes{}
	s := &screen{w: w, tty: true}
	s.enter()
	if got := w.String(); got != altOn || !strings.Contains(got, "\033[?25l") {
		t.Errorf("enter wrote %q, want the alternate screen and the hidden cursor", got)
	}
	w.Reset()
	s.show("one\ntwo\nthree", 24, 80)
	got := w.String()
	if got != "\033[Hone\033[K\r\ntwo\033[K\r\nthree\033[K\033[J" {
		t.Errorf("a tty frame = %q", got)
	}
	for _, bad := range []string{"\033[2J", "\033[3J", "\033c"} {
		if strings.Contains(got, bad) {
			t.Errorf("the frame has %q, which clears the screen or its scrollback", bad)
		}
	}
	w.Reset()
	s.leave()
	if got := w.String(); got != altOff || !strings.Contains(got, "\033[?25h") {
		t.Errorf("leave wrote %q, want the cursor and the main screen back", got)
	}

	w = &writes{}
	p := &screen{w: w, tty: false}
	p.enter()
	p.show("plain\ntext", 24, 80)
	p.leave()
	if w.String() != "plain\ntext\n" {
		t.Errorf("a pipe got %q, want the plain frame and a newline", w.String())
	}
}

// TestMonitorFrameEndsWithoutNewlineAndPlainWithoutColor: the monitor's frame (the real path, a project in a temp dir)
// has no newline after its last row and, with color off (NO_COLOR or a pipe), no escape byte.
func TestMonitorFrameNoEscapeWhenPlain(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p, _ := monitorFx(t, now)
	m := monitorOf(t, p, now, 30)
	text := m.frame(now)
	if text == "" || strings.Contains(text, "\033") || strings.HasSuffix(text, "\n") {
		t.Errorf("a plain frame has an escape or ends in a newline: %q", text)
	}
	m.color = true
	if painted := m.frame(now); !strings.Contains(painted, "\033[") || strings.HasSuffix(painted, "\n") {
		t.Errorf("a painted frame has no color, or ends in a newline: %q", painted)
	}
}

// TestMonitorResizeRedrawsAtOnce: with nothing else changed, a new terminal size makes the next tick build a frame of the
// new size at once, and the screen writes it.
func TestMonitorResizeRedrawsAtOnce(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p, _ := monitorFx(t, now)
	m := monitorOf(t, p, now, 20)
	rows := 20
	m.height = func() int { return rows }
	text, drawn := m.tick()
	if !drawn || len(strings.Split(text, "\n")) != 20 {
		t.Fatalf("precondition: first frame drawn %v with %d rows, want 20", drawn, len(strings.Split(text, "\n")))
	}
	if _, drawn := m.tick(); drawn {
		t.Fatal("precondition: a second tick at the same size and time draws nothing")
	}
	w := &writes{}
	s := &screen{w: w, tty: true}
	s.show(text, 20, m.cols())
	w.Reset()
	rows = 12
	text, drawn = m.tick()
	if !drawn {
		t.Fatal("no frame after a resize")
	}
	if n := len(strings.Split(text, "\n")); n != 12 {
		t.Errorf("the resized frame has %d rows, want 12", n)
	}
	s.show(text, m.rows(), m.cols())
	if w.Len() == 0 {
		t.Error("the resized frame was not written")
	}
}

// TestMonitorRunRefitsOnWinch: run draws a first frame, and a SIGWINCH makes it redraw at the new size before the
// 2 s cadence would; Ctrl-C (the context) ends it and leaves nothing drawn after.
func TestMonitorRunRefitsOnWinch(t *testing.T) {
	t.Parallel()
	now := time.Now().Truncate(time.Second)
	p, _ := monitorFx(t, now)
	m := monitorOf(t, p, now, 20)
	rows := 20
	m.height = func() int { return rows }
	pr := &syncWrites{}
	s := &screen{w: pr, tty: true}
	ctx, cancel := context.WithCancel(context.Background())
	winch := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() { m.run(ctx, s, winch); close(done) }()
	if !waitFor(func() bool { return pr.count("\033[H") >= 1 }) {
		t.Fatal("no first frame")
	}
	rows = 12
	winch <- syscall.SIGWINCH
	if !waitFor(func() bool { return pr.count("\033[H") >= 2 }) {
		t.Fatal("no frame after the resize signal")
	}
	if !strings.Contains(pr.String(), "\033[J") {
		t.Error("the frames do not clear below")
	}
	cancel()
	<-done
	if strings.Contains(pr.String(), "\033[2J") {
		t.Error("a frame cleared the whole screen")
	}
}

// syncWrites is a writer safe to read while the monitor writes to it.
type syncWrites struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncWrites) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
func (w *syncWrites) Flush() error { return nil }
func (w *syncWrites) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}
func (w *syncWrites) count(sub string) int { return strings.Count(w.String(), sub) }
