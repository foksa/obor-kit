package app_test

import (
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
)

// The wall watch of a bubble (wallWatch, rig_ext_test.go): a wait on the bubble's clock ends in real time too. Each
// test builds its own watch with short limits and a stalled that records; times read outside the bubble are real.

// stallBubble makes the clock of the bubble it is called in stand still: a goroutine of the bubble waits for lock,
// which is not the bubble's own, until lock is unlocked.
func stallBubble(lock *sync.Mutex) {
	lock.Lock()
	go func() {
		lock.Lock()
		lock.Unlock()
	}()
}

// A wait that never holds, in a bubble whose clock moves slower than the real one: it ends at the wall limit on its
// own goroutine, long before its limit on the bubble's clock, and nothing is reported as stalled.
func TestBubbleWaitEndsAtTheWallLimit(t *testing.T) {
	t.Parallel()
	real := time.NewTicker(time.Millisecond) // outside the bubble: each try of the wait takes a real millisecond
	defer real.Stop()
	var stalls atomic.Int32
	w := newWallWatch(t.Name(), 50*time.Millisecond, time.Minute, time.Hour, func(string) { stalls.Add(1) })
	began := time.Now()
	bubbleWatched(t, w, func(t *testing.T) {
		start := time.Now()
		how := poll(t, "never", time.Hour, 5*time.Millisecond, nil, func() bool { <-real.C; return false })
		if want := "timed out (50ms of real time, the bubble's clock short of 1h0m0s)"; how != want {
			t.Errorf("the wait ended %q; want %q", how, want)
		}
		if on := time.Since(start); on <= 0 || on >= time.Hour {
			t.Errorf("the bubble's clock moved %s during the wait; want some of its 1h limit, not all", on)
		}
	})
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the wait took %s of real time; its wall limit is 50ms", took)
	}
	if n := stalls.Load(); n != 0 {
		t.Errorf("%d stalls reported; the wait ended itself", n)
	}
}

// A wait in a bubble whose clock stands still: its poll never wakes, so the watch reports it once the grace after
// the wall limit has passed, with what it waits for, its records and the stack of the goroutine that holds the clock.
// With the clock moving again the wait ends as over its wall limit, after the one sleep it was in.
func TestBubbleWaitOnAStalledClockIsReported(t *testing.T) {
	t.Parallel()
	var lock sync.Mutex
	reports := make(chan string, 1)
	w := newWallWatch(t.Name(), 50*time.Millisecond, 50*time.Millisecond, time.Hour, func(report string) {
		reports <- report
		lock.Unlock() // the clock moves again
	})
	began := time.Now()
	bubbleWatched(t, w, func(t *testing.T) {
		stallBubble(&lock)
		start := time.Now()
		how := poll(t, "never", time.Hour, 5*time.Millisecond, func() string { return "the wait's records" },
			func() bool { return false })
		if !strings.Contains(how, "50ms of real time") {
			t.Errorf("the wait ended %q; want over its wall limit", how)
		}
		if on := time.Since(start); on != 5*time.Millisecond {
			t.Errorf("the bubble's clock moved %s during the wait; want the one 5ms sleep: it stood still", on)
		}
	})
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the report took %s of real time; the wall limit and its grace are 100ms", took)
	}
	select {
	case report := <-reports:
		for _, want := range []string{t.Name() + ": waiting for never for ", "its poll never woke", "the wait's records",
			"sync.(*Mutex).Lock"} {
			if !strings.Contains(report, want) {
				t.Errorf("the stall's report lacks %q:\n%s", want, report)
			}
		}
	default:
		t.Fatal("no stall reported")
	}
}

// A bubble with no polling wait open whose clock stands still (the test sleeps on it) is reported once it has run
// for the whole bubble's wall limit.
func TestBubbleStalledOutsideAWaitIsReported(t *testing.T) {
	t.Parallel()
	var lock sync.Mutex
	reports := make(chan string, 1)
	w := newWallWatch(t.Name(), time.Hour, time.Hour, 50*time.Millisecond, func(report string) {
		reports <- report
		lock.Unlock()
	})
	began := time.Now()
	bubbleWatched(t, w, func(t *testing.T) {
		stallBubble(&lock)
		time.Sleep(time.Hour)
	})
	if took := time.Since(began); took > 30*time.Second {
		t.Errorf("the report took %s of real time; the bubble's wall limit is 50ms", took)
	}
	select {
	case report := <-reports:
		for _, want := range []string{t.Name() + ": still running after 50ms of real time", "sync.(*Mutex).Lock"} {
			if !strings.Contains(report, want) {
				t.Errorf("the stall's report lacks %q:\n%s", want, report)
			}
		}
	default:
		t.Fatal("no stall reported")
	}
}

// A write lock the rig holds on the store (withStore) while serve writes a record (a dispatch's fact, under the app's
// lock, a goroutine waiting for that lock) does not stall the bubble: the rig holds serve's own connection, so
// serve's write waits for it, a wait of the bubble, and goes in once the rig lets go. Through a second connection the
// lock leaves serve's write in SQLite's busy sleep on the bubble's clock with the app's lock held; the goroutine
// waiting for that lock keeps the clock still, so the sleep never ends and the bubble stalls.
func TestRigStoreLockDuringAServeWrite(t *testing.T) {
	t.Parallel()
	armed, let := make(chan struct{}), make(chan struct{}) // outside the bubble: the rig lets go in real time
	go func() {
		<-armed
		time.Sleep(200 * time.Millisecond)
		close(let)
	}()
	bubble(t, func(t *testing.T) {
		r := fetchRig(t)
		r.start()
		r.waitFor("serve's store", func() bool { return servingDB(r.app, r.done, r.p.Root) != nil })
		if r.has("1 fact dispatch") {
			t.Fatalf("precondition: T-1 dispatched before the test's dispatch: %q", r.steps())
		}
		looked, replied := make(chan struct{}), make(chan struct{})
		var once sync.Once
		app.Refuse(r.app, r.p.Root, func(rec event.Record) error { // serve's append, the app's lock held
			if rec.Kind == event.KindFact && rec.Step == event.StepDispatched {
				once.Do(func() {
					go func() { // status takes the app's lock: it waits for it while serve's write holds it
						defer close(looked)
						r.app.Status()
					}()
				})
			}
			return nil
		})
		held, hold := make(chan struct{}), make(chan error, 1)
		go func() {
			hold <- r.withStore(func(db *sql.DB) error {
				tx, err := db.Begin()
				if err != nil {
					return err
				}
				defer tx.Rollback()
				// a write: the transaction holds the database's write lock until it ends
				if _, err := tx.Exec("UPDATE meta SET value = value WHERE key = 'schema_version'"); err != nil {
					return err
				}
				close(held)
				close(armed)
				<-let // real time: the bubble's clock stands still meanwhile, so no wait of the dispatch times out
				select {
				case <-replied:
					return errors.New("the dispatch replied while the rig held the write lock")
				default:
				}
				return nil
			})
		}()
		select {
		case <-held:
		case err := <-hold:
			t.Fatalf("the rig's write lock: %v", err)
		}
		var reply string
		var derr error
		go func() {
			defer close(replied)
			reply, derr = r.dispatch("d1", "L-01")
		}()
		r.waitFor("the dispatch's reply", func() bool {
			select {
			case <-replied:
				return true
			default:
				return false
			}
		})
		<-looked
		if err := <-hold; err != nil {
			t.Fatalf("the rig's write lock: %v", err)
		}
		if derr != nil || reply != "DISPATCHED T-1 → L-01 (queued) as sonnet" {
			t.Fatalf("dispatch: %q, %v; want T-1 dispatched once the rig let go", reply, derr)
		}
		r.waitStep("1 fact dispatch")
	})
}
