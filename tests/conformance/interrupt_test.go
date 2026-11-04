package main

// T-816 (RV-104 #3): the interrupt closes every open lab once, tells the runner to stop first, and exits 130 within a
// bound. The stub lab reproduces a real lab's close: it forgets itself under labsMu, the mutex the handler held while
// closing (a deadlock in the code this replaces).

import (
	"bytes"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubLab is a lab without a session: close counts its calls, records whether the runner was told first, and forgets
// itself under the mutex.
type stubLab struct {
	done  <-chan struct{} // the runner's stop signal
	mu    sync.Mutex
	count int
	early bool // close ran before the runner was told
}

func (s *stubLab) close() {
	select {
	case <-s.done:
	default:
		s.mu.Lock()
		s.early = true
		s.mu.Unlock()
	}
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	labsMu.Lock()
	delete(labs, s)
	labsMu.Unlock()
}

func (s *stubLab) closed() (count int, early bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count, s.early
}

func TestInterruptClosesEveryOpenLabOnceAndExits130(t *testing.T) {
	done := make(chan struct{})
	first, second := &stubLab{done: done}, &stubLab{done: done}
	openLab(first)
	openLab(second)
	t.Cleanup(func() {
		labsMu.Lock()
		delete(labs, first)
		delete(labs, second)
		labsMu.Unlock()
	})
	sig, code, said := make(chan os.Signal, 1), make(chan int, 1), &bytes.Buffer{}
	go interrupt(sig, done, said, func(c int) { code <- c })
	sig <- os.Interrupt
	select {
	case c := <-code:
		if c != 130 {
			t.Errorf("exit %d, want 130", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interrupt did not finish within 5 s: a close takes labsMu again, the mutex the handler closed under")
	}
	for _, s := range []*stubLab{first, second} {
		n, early := s.closed()
		if n != 1 {
			t.Errorf("lab closed %d times, want once", n)
		}
		if early {
			t.Error("the lab was closed before the runner was told to stop")
		}
	}
	if !strings.Contains(said.String(), "interrupted, stopping the throwaway sessions") {
		t.Errorf("the interrupt said nothing on stderr: %q", said.String())
	}
}
