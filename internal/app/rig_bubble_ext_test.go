package app_test

import (
	"bytes"
	"context"
	"fmt"
	"obor/internal/app/port"
	"obor/internal/herdr/herdrtest"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// bubbles holds the tests running in a bubble (bubble), each with its wall watch: a rig built by one uses in-memory
// connections.
var bubbles sync.Map // *testing.T → *wallWatch

// bubble runs f in a testing/synctest bubble. A rig built inside it has no unix socket: the fake herdr and the app's
// socket are in-memory pipes, so every wait of the test and of the app is on the bubble's clock, which stands still
// while anything runs. Machine load cannot expire such a wait; a wall watch ends it in real time (wallWatch). The rig
// reads and writes the store through serve's own connection while serve runs (readStore): no second connection. No
// t.Run, t.Parallel or t.Deadline inside.
func bubble(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	bubbleWatched(t, newWallWatch(t.Name(), waitWall, stallGrace, bubbleWall, func(msg string) { panic(msg) }), f)
}

// bubbleWatched is bubble under the wall watch w, which it stops when the bubble ends.
func bubbleWatched(t *testing.T, w *wallWatch, f func(t *testing.T)) {
	t.Helper()
	defer w.stop()
	synctest.Test(t, func(t *testing.T) {
		bubbles.Store(t, w)
		defer bubbles.Delete(t)
		f(t)
	})
}

func inBubble(t *testing.T) bool { return watchOf(t) != nil }

// watchOf is the wall watch of t's bubble (nil outside one).
func watchOf(t *testing.T) *wallWatch {
	w, _ := bubbles.Load(t)
	watch, _ := w.(*wallWatch)
	return watch
}

// The wall limits of a bubble: a polling wait is over after waitWall of real time; one still asleep stallGrace later,
// or a bubble still running after bubbleWall, is reported as stalled. All are well under go test's timeout, so a
// stuck test fails as itself and not as the package's deadline.
const (
	waitWall   = 2 * time.Minute
	stallGrace = 10 * time.Second
	bubbleWall = 5 * time.Minute
)

// wallWatch ends a bubble's waits in real time. A bubble's clock stands still while one of its goroutines is blocked
// on something that is not the bubble's own (a lock, real I/O), so a wait sleeping on that clock never wakes, and
// nothing outside a bubble can wake it. The watch is a goroutine outside the bubble on the real clock:
//   - a polling wait open for wait is over: its poll sees that at its next try and fails with its records;
//   - a wait still open grace later sleeps on a clock that stands still, and so does a bubble running for whole:
//     stalled gets the report (what was waited for, its records, every goroutine's stack). bubble's stalled panics:
//     the package fails then, with the report, and no other way ends a test whose goroutines cannot run.
type wallWatch struct {
	name               string
	wait, grace, whole time.Duration
	stalled            func(report string)
	quit               chan struct{}

	mu   sync.Mutex
	ids  int
	open map[int]*wallWait
}

// wallWait is one open polling wait of a bubble.
type wallWait struct {
	what    string
	records func() string
	seen    time.Time // the watch's first sight of it, on the real clock
	over    bool
	told    bool // reported as stalled
}

// newWallWatch starts a wall watch for the bubble of the test name. It is called outside the bubble: the watch's
// goroutine and its clock are real.
func newWallWatch(name string, wait, grace, whole time.Duration, stalled func(report string)) *wallWatch {
	w := &wallWatch{name: name, wait: wait, grace: grace, whole: whole, stalled: stalled, quit: make(chan struct{}),
		open: map[int]*wallWait{}}
	go w.run()
	return w
}

func (w *wallWatch) stop() { close(w.quit) }

func (w *wallWatch) run() {
	tick := time.NewTicker(min(w.wait/10, 100*time.Millisecond))
	defer tick.Stop()
	start := time.Now()
	for {
		select {
		case <-w.quit:
			return
		case now := <-tick.C:
			for _, s := range w.check(now) {
				w.report(fmt.Sprintf("waiting for %s for %s of real time and its poll never woke", s.what, now.Sub(s.seen).Round(time.Millisecond)), s.records)
			}
			if now.Sub(start) >= w.whole {
				w.report(fmt.Sprintf("still running after %s of real time", w.whole), nil)
				return
			}
		}
	}
}

// check ages the open waits to now: a wait open for the limit is over, and the ones open grace longer are returned
// once.
func (w *wallWatch) check(now time.Time) []*wallWait {
	w.mu.Lock()
	defer w.mu.Unlock()
	var stalled []*wallWait
	for _, s := range w.open {
		if s.seen.IsZero() {
			s.seen = now
		}
		age := now.Sub(s.seen)
		s.over = s.over || age >= w.wait
		if age >= w.wait+w.grace && !s.told {
			s.told = true
			stalled = append(stalled, s)
		}
	}
	return stalled
}

// report hands stalled what the bubble is stuck in, the wait's records (read for at most a second: a reader may need
// what the bubble holds) and every goroutine's stack: the one blocked outside the bubble is among them.
func (w *wallWatch) report(what string, records func() string) {
	recs := "none"
	if records != nil {
		read := make(chan string, 1)
		go func() { read <- records() }()
		select {
		case recs = <-read:
		case <-time.After(time.Second):
			recs = "not read within 1s"
		}
	}
	stacks := make([]byte, 8<<20)
	stacks = stacks[:runtime.Stack(stacks, true)]
	w.stalled(fmt.Sprintf("%s: %s: the bubble's clock stands still (a goroutine of the bubble is blocked on a lock or "+
		"on real I/O)\n%s\ngoroutines:\n%s", w.name, what, recs, stacks))
}

// begin opens a polling wait for what (records: what its failure prints); end closes it. Outside a bubble (a nil
// watch) there is nothing to open: the test's clock is the real one.
func (w *wallWatch) begin(what string, records func() string) int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ids++
	w.open[w.ids] = &wallWait{what: what, records: records}
	return w.ids
}

func (w *wallWatch) end(id int) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.open, id)
}

// over is whether the wait id has been open for the wall limit.
func (w *wallWatch) over(id int) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.open[id].over
}

// poll calls ok every step until it holds, for at most limit on t's clock and, in a bubble, the bubble's wall limit:
// "" once ok held, else how the wait ended ("timed out", for a failure's first words). records is what the wait's
// failure prints (nil: nothing).
func poll(t *testing.T, what string, limit, step time.Duration, records func() string, ok func() bool) string {
	w := watchOf(t)
	id := w.begin(what, records)
	defer w.end(id)
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(step) {
		if ok() {
			return ""
		}
		if w.over(id) {
			return fmt.Sprintf("timed out (%s of real time, the bubble's clock short of %s)", w.wait, limit)
		}
	}
	return "timed out"
}

// serveHerdr is the fake herdr on f: in memory in a bubble, on a unix socket outside one.
func serveHerdr(t *testing.T, f *herdrtest.Fake) *herdrtest.Server {
	t.Helper()
	if inBubble(t) {
		return herdrtest.ServeMem(t, f)
	}
	return herdrtest.Serve(t, f)
}

// The app at its boundary: the herdr socket client (herdr.Panes) on the fake herdr socket, a scripted lane table and
// a clock the test moves. Ticks run every few ms; the watchdog bound is the real 10 min of the clock.

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Move runs f with the clock held still, then moves it on by d: with f's file work that is one step for the watchdogs,
// which cannot read a time between them. A test that writes what a watchdog samples must hold the clock across it, or
// a loaded machine lets a sample run between the write and the move and take a baseline at the old time.
func (c *clock) Move(d time.Duration, f func(now time.Time)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c.t)
	c.t = c.t.Add(d)
}

type laneTable struct {
	mu    sync.Mutex
	lanes []port.Lane
}

func (l *laneTable) Lanes(context.Context, string) ([]port.Lane, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]port.Lane(nil), l.lanes...), nil
}

// syncBuf is the app's output, read while it writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
