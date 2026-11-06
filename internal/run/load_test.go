package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/testkit"
)

// fakeLoad is a load reader that returns its values in turn (the last one repeats) and counts its reads.
type fakeLoad struct {
	mu    sync.Mutex
	vals  []float64
	ok    bool
	reads int
}

func (f *fakeLoad) read() (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	v := f.vals[0]
	if len(f.vals) > 1 {
		f.vals = f.vals[1:]
	}
	return v, f.ok
}

// T-720: Acquire waits while the machine load is above LoadLimit, for worker and merge alike: it reports the wait and
// claims once the load drops (unmarked); a load that stays above for LoadCap is claimed anyway, marked with the load;
// an unreadable load, a limit of 0 and a held slot claim at once.
func TestAcquireWaitsForLoad(t *testing.T) {
	t.Parallel()
	slots := func(t *testing.T, load *fakeLoad) Slots {
		return Slots{Dir: t.TempDir(), Max: 2, BlockMax: 900 * time.Second, Marker: "merge-running-proj",
			Poll: 5 * time.Millisecond, Report: time.Hour, LoadLimit: 20, LoadCap: time.Hour, Load: load.read}
	}
	for _, prio := range []string{"worker", "merge"} {
		t.Run(prio+": above the limit waits and reports, claims when it drops", func(t *testing.T) {
			t.Parallel()
			load := &fakeLoad{vals: []float64{45.1, 30, 30, 19.5}, ok: true}
			s := slots(t, load)
			var w bytes.Buffer
			path, loaded, err := s.Acquire(context.Background(), prio, os.Getpid(), &w)
			if err != nil || path == "" {
				t.Fatalf("acquire: %q %v", path, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("claimed slot %s not on disk: %v", path, err)
			}
			if load.reads != 4 {
				t.Errorf("load reads %d, want 4 (three above the limit, then below)", load.reads)
			}
			if got := w.String(); got != "waiting: machine load 45.1 (> 20)\n" {
				t.Errorf("wait lines %q, want one load line (report interval: an hour)", got)
			}
			if loaded != 0 || UnderLoad(loaded) != "" {
				t.Errorf("claimed below the limit, yet loaded %v", loaded)
			}
		})
	}
	t.Run("above the limit for the cap: claims anyway, marked", func(t *testing.T) {
		t.Parallel()
		load := &fakeLoad{vals: []float64{50}, ok: true}
		s := slots(t, load)
		s.LoadCap = 30 * time.Millisecond
		var w bytes.Buffer
		t0 := time.Now()
		path, loaded, err := s.Acquire(context.Background(), "merge", os.Getpid(), &w)
		if err != nil || path == "" {
			t.Fatalf("acquire: %q %v", path, err)
		}
		if took := time.Since(t0); took < 30*time.Millisecond {
			t.Errorf("claimed after %v, before the cap", took)
		}
		if !strings.HasPrefix(w.String(), "waiting: machine load 50 (> 20)\n") {
			t.Errorf("no load wait line: %q", w.String())
		}
		if loaded != 50 || UnderLoad(loaded) != " (run under load 50)" {
			t.Errorf("loaded %v, mark %q; want 50", loaded, UnderLoad(loaded))
		}
	})
	t.Run("unreadable load: claims at once", func(t *testing.T) {
		t.Parallel()
		load := &fakeLoad{vals: []float64{0}, ok: false}
		s := slots(t, load)
		var w bytes.Buffer
		path, loaded, err := s.Acquire(context.Background(), "worker", os.Getpid(), &w)
		if err != nil || path == "" || loaded != 0 {
			t.Fatalf("acquire: %q %v %v", path, loaded, err)
		}
		if load.reads != 1 || w.Len() != 0 {
			t.Errorf("reads %d (want 1), wait lines %q (want none)", load.reads, w.String())
		}
	})
	t.Run("limit 0 disables: claims at once, the load not read", func(t *testing.T) {
		t.Parallel()
		load := &fakeLoad{vals: []float64{99}, ok: true}
		s := slots(t, load)
		s.LoadLimit = 0
		var w bytes.Buffer
		path, loaded, err := s.Acquire(context.Background(), "worker", os.Getpid(), &w)
		if err != nil || path == "" || loaded != 0 {
			t.Fatalf("acquire: %q %v %v", path, loaded, err)
		}
		if load.reads != 0 || w.Len() != 0 {
			t.Errorf("reads %d (want 0), wait lines %q (want none)", load.reads, w.String())
		}
	})
	t.Run("a live held slot does not wait", func(t *testing.T) {
		t.Parallel()
		load := &fakeLoad{vals: []float64{99}, ok: true}
		s := slots(t, load)
		held := filepath.Join(s.Dir, "slot-1")
		if err := os.WriteFile(held, []byte("0 0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s.Held = held
		var w bytes.Buffer
		path, _, err := s.Acquire(context.Background(), "worker", os.Getpid(), &w)
		if err != nil || path != held || w.Len() != 0 {
			t.Errorf("acquire: %q %v, wait lines %q; want the held slot at once", path, err, w.String())
		}
	})
	t.Run("a cancelled context ends the load wait", func(t *testing.T) {
		t.Parallel()
		load := &fakeLoad{vals: []float64{99}, ok: true}
		s := slots(t, load)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		var w bytes.Buffer
		if path, _, err := s.Acquire(ctx, "worker", os.Getpid(), &w); err == nil || path != "" {
			t.Errorf("acquire: %q %v; want the context's error", path, err)
		}
		if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
			t.Errorf("claimed under load before the cap: %v", entries)
		}
	})
}

// ctlLoad is a load reader the test sets while Acquire runs.
type ctlLoad struct {
	mu sync.Mutex
	v  float64
}

func (c *ctlLoad) set(v float64) { c.mu.Lock(); c.v = v; c.mu.Unlock() }
func (c *ctlLoad) read() (float64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.v, true
}

// acquireResult is one Acquire's outcome and when it returned.
type acquireResult struct {
	path   string
	loaded float64
	err    error
	at     time.Time
}

// fakeClock is Acquire's clock in the tests: each poll advances it by one poll and then runs script with the poll's
// number and the new time, so the script and not wall time decides the outcome (T-736, T-922).
type fakeClock struct {
	clock  time.Time
	polls  int
	script func(polls int, now time.Time)
}

func newFakeClock(s *Slots, start time.Time, script func(polls int, now time.Time)) *fakeClock {
	c := &fakeClock{clock: start, script: script}
	s.Now = func() time.Time { return c.clock }
	s.After = func(d time.Duration) <-chan time.Time {
		c.polls++
		c.clock = c.clock.Add(d)
		c.script(c.polls, c.clock)
		ch := make(chan time.Time, 1)
		ch <- c.clock
		return ch
	}
	return c
}

// T-726 (RV-85 #2): LoadCap bounds the time spent waiting on the load, summed over its stretches; time queued for a
// slot does not use it up, and a cancelled context ends either wait.
func TestAcquireLoadCapCountsOnlyLoadWaits(t *testing.T) {
	t.Parallel()
	// one slot, held by a live holder (0) until the test frees it; a wait line every poll
	setup := func(t *testing.T, load *ctlLoad, loadCap time.Duration) (Slots, string) {
		s := Slots{Dir: t.TempDir(), Max: 1, BlockMax: 900 * time.Second, Marker: "merge-running-proj",
			Poll: 5 * time.Millisecond, Report: time.Nanosecond, LoadLimit: 20, LoadCap: loadCap, Load: load.read}
		busy := filepath.Join(s.Dir, "slot-0")
		if err := os.WriteFile(busy, []byte("0 0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return s, busy
	}
	start := func(ctx context.Context, s Slots, w *lockedBuffer) chan acquireResult {
		done := make(chan acquireResult, 1)
		go func() {
			path, loaded, err := s.Acquire(ctx, "worker", os.Getpid(), w)
			done <- acquireResult{path, loaded, err, time.Now()}
		}()
		return done
	}
	pending := func(t *testing.T, done chan acquireResult, what string) {
		t.Helper()
		select {
		case r := <-done:
			t.Fatalf("%s: Acquire returned %+v", what, r)
		default:
		}
	}
	t.Run("queued past the cap at low load, the load rises as the slot frees: waits for the load", func(t *testing.T) {
		t.Parallel()
		// Fake clock (T-922): queued for three caps at low load, then the load rises and the slot frees with the cap's
		// rest to spare; no sleep and no poll counter, so a delayed goroutine cannot let the cap run out.
		const loadCap = 200 * time.Millisecond
		const poll = 50 * time.Millisecond
		load := &ctlLoad{v: 10}
		s, busy := setup(t, load, loadCap)
		s.Poll = poll
		t0 := time.Unix(2000, 0)
		var raised time.Time
		var w lockedBuffer
		c := newFakeClock(&s, t0, func(polls int, now time.Time) {
			switch {
			case polls == 6: // queued for three caps: slot waits only, no load wait counted
				if got := w.String(); !strings.Contains(got, "waiting for a check slot (1 running)\n") || strings.Contains(got, "machine load") {
					t.Errorf("precondition: wait lines %q, want slot waits only", got)
				}
				load.set(50)
				if err := os.Remove(busy); err != nil {
					t.Error(err)
				}
				raised = now
			case polls == 7 || polls == 8: // load high, slot free, well inside the cap: must not claim
				if _, err := os.Stat(busy); err == nil {
					t.Errorf("claimed the freed slot %v into the load wait, under load 50", now.Sub(raised))
				}
			case polls == 9: // the load drops before the cap runs out: three polls of load wait, 150 ms of the 200
				if took := now.Sub(raised); took >= loadCap {
					t.Errorf("the load waited %v before dropping; want under the %v cap", took, loadCap)
				}
				load.set(10)
			}
		})
		r := <-start(context.Background(), s, &w)
		if r.err != nil || r.path != busy {
			t.Fatalf("acquire: %+v, want %s", r, busy)
		}
		if raised.IsZero() {
			t.Fatal("precondition: the script never raised the load")
		}
		if queued := raised.Sub(t0); queued <= loadCap {
			t.Errorf("precondition: queued %v at low load, want more than the %v cap", queued, loadCap)
		}
		if c.clock.Sub(raised) >= loadCap {
			t.Errorf("claimed %v after the raise; want inside the %v cap", c.clock.Sub(raised), loadCap)
		}
		if r.loaded != 0 {
			t.Errorf("claimed under load %v; want after the load dropped", r.loaded)
		}
		if got := w.String(); !strings.Contains(got, "waiting: machine load 50 (> 20)\n") {
			t.Errorf("wait lines %q: no load wait line", got)
		}
	})
	t.Run("load wait capped across alternating load", func(t *testing.T) {
		t.Parallel()
		// Fake clock: each poll advances it by one poll and runs the script, so no wall time decides the outcome.
		const loadCap = 400 * time.Millisecond
		const poll = 100 * time.Millisecond
		load := &ctlLoad{v: 50}
		s, busy := setup(t, load, loadCap)
		s.Poll = poll
		var third time.Time
		c := newFakeClock(&s, time.Unix(1000, 0), func(polls int, now time.Time) {
			switch polls {
			case 2: // stretch 2: slot busy, then load low (queued, not counted); the other order would let it claim the free slot
				if err := os.WriteFile(busy, []byte("0 0\n"), 0o644); err != nil {
					t.Error(err)
				}
				load.set(10)
			case 5: // stretch 3: load high, then slot free
				load.set(50)
				if err := os.Remove(busy); err != nil {
					t.Error(err)
				}
				third = now
			}
		})
		if err := os.Remove(busy); err != nil { // stretch 1: load high, slot free (two polls: half the cap)
			t.Fatal(err)
		}
		var w lockedBuffer
		path, loaded, err := s.Acquire(context.Background(), "worker", os.Getpid(), &w)
		if err != nil || path != busy || loaded != 50 {
			t.Fatalf("acquire: %q %v %v; want %s claimed under load 50 once the cap ran out", path, loaded, err, busy)
		}
		if third.IsZero() {
			t.Fatal("precondition: the script never reached the third stretch")
		}
		// the rest of the cap is loadCap/2: not at once (the queue counted), not a whole cap (the first stretch forgotten)
		if took := c.clock.Sub(third); took < loadCap/4 || took >= loadCap*9/10 {
			t.Errorf("claimed %v into the third stretch; want about %v (the cap's rest)", took, loadCap/2)
		}
		got := w.String()
		if !strings.Contains(got, "waiting: machine load 50 (> 20)\n") || !strings.Contains(got, "waiting for a check slot (1 running)\n") {
			t.Errorf("wait lines %q: want a load wait and a slot wait", got)
		}
	})
	t.Run("a cancelled context ends both waits", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			load float64
			free bool
			line string
		}{
			{"slot wait", 10, false, "waiting for a check slot (1 running)\n"},
			{"load wait", 50, true, "waiting: machine load 50 (> 20)\n"},
		} {
			load := &ctlLoad{v: tc.load}
			s, busy := setup(t, load, time.Hour)
			if tc.free {
				if err := os.Remove(busy); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			var w lockedBuffer
			done := start(ctx, s, &w)
			time.Sleep(50 * time.Millisecond)
			pending(t, done, tc.name)
			if got := w.String(); !strings.HasPrefix(got, tc.line) {
				t.Fatalf("%s precondition: wait lines %q, want %q", tc.name, got, tc.line)
			}
			cancel()
			select {
			case r := <-done:
				if r.path != "" || r.err != context.Canceled {
					t.Errorf("%s: acquire %+v, want the context's error", tc.name, r)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s: Acquire still waiting 5 s after the cancel", tc.name)
			}
		}
	})
}

// SlotsFor's load limit: LoadFactor × NumCPU by default, checks.load_limit (0 too), CHECK_LOAD_LIMIT over both (a
// value that is not a non-negative number ignored).
func TestSlotsForLoadLimit(t *testing.T) {
	t.Parallel()
	def := float64(LoadFactor * runtime.NumCPU())
	for _, tc := range []struct {
		name  string
		limit any // checks.load_limit; nil: unset
		env   string
		want  float64
	}{
		{"default: 2 × NumCPU", nil, "", def},
		{"checks.load_limit", 7.5, "", 7.5},
		{"checks.load_limit 0 disables", 0, "", 0},
		{"CHECK_LOAD_LIMIT over the config", 7.5, "3", 3},
		{"CHECK_LOAD_LIMIT 0 over the config", 7.5, "0", 0},
		{"CHECK_LOAD_LIMIT not a number: the config's", 7.5, "lots", 7.5},
		{"CHECK_LOAD_LIMIT negative: the default", nil, "-1", def},
	} {
		p := testkit.New(t)
		if tc.limit != nil {
			p.Config(testkit.Set("checks", map[string]any{"load_limit": tc.limit}))
		} else {
			p.Config()
		}
		cfg, err := config.Load(p.Root)
		if err != nil {
			t.Fatal(err)
		}
		// p.Env sets CHECK_LOAD_LIMIT=0 for the tests; "CHECK_LOAD_LIMIT=" removes it.
		if got := SlotsFor(cfg, p.Env("CHECK_LOAD_LIMIT="+tc.env)).LoadLimit; got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if def < 2 {
		t.Errorf("precondition: default limit %v from %d CPUs", def, runtime.NumCPU())
	}
}

// The load readers: /proc/loadavg's first field; the machine's own reads where the OS has one.
func TestSystemLoad(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]float64{"0.52 0.58 0.59 1/467 1234\n": 0.52, "45.10 66.47 72.41 3/900 1\n": 45.1} {
		if got, ok := parseLoadavg(in); !ok || got != want {
			t.Errorf("parseLoadavg(%q) = %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "x 1 2", "-1 0 0"} {
		if _, ok := parseLoadavg(in); ok {
			t.Errorf("parseLoadavg(%q): ok", in)
		}
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		if l, ok := SystemLoad(); !ok || l < 0 {
			t.Errorf("SystemLoad on %s: %v %v", runtime.GOOS, l, ok)
		}
	}
	if got := FormatLoad(45.1044921875); got != "45.1" {
		t.Errorf("FormatLoad: %q", got)
	}
}

// obor run of a heavy command that claimed its slot after the load wait ran out: a closing line carries the mark;
// below the limit no such line.
func TestRunUnderLoadMark(t *testing.T) {
	t.Parallel()
	f := setup(t, `{"h": {"cmd": "echo ran", "heavy": true}}`)
	f.load = func() (float64, bool) { return 33, true }
	r := f.run(t, f.lane, []string{"CHECK_LOAD_LIMIT=1"}, nil, "h")
	if r.rc != 0 || r.out != "ran\n" {
		t.Fatalf("run: %+v", r)
	}
	if !strings.Contains(r.errOut, "waiting: machine load 33 (> 1)\n") || !strings.HasSuffix(r.errOut, "obor run: h exited 0 (run under load 33)\n") {
		t.Errorf("stderr %q: want the load wait line and the marked exit line", r.errOut)
	}
	r = f.run(t, f.lane, []string{"CHECK_LOAD_LIMIT=40"}, nil, "h")
	if r.rc != 0 || strings.Contains(r.errOut, "under load") || strings.Contains(r.errOut, "machine load") {
		t.Errorf("below the limit: %+v", r)
	}
}
