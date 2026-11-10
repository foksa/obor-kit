package app_test

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
