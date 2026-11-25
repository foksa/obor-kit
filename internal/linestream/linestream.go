// Package linestream turns a source of lines (a socket, a subprocess's stdout) into a channel of events: one goroutine
// reads the lines from the start, so a source that must not stall is drained while its caller still prepares; Run
// applies each line and sends what it makes; the first error ends the stream once. A terminal adapter's event stream
// is one (internal/hc, internal/rex).
package linestream

import (
	"context"
	"sync"
	"time"
)

// Stream is one source's lines turned into events of type T.
type Stream[T any] struct {
	ctx    context.Context
	cancel context.CancelFunc
	closed error       // what Close and ctx's end stop the stream with
	lines  chan []byte // read's: each line, closed when the source ends
	out    chan T

	mu      sync.Mutex
	err     error
	readErr error // the source's end, set before lines closes
}

// Start reads lines with read in its own goroutine until read fails or ctx ends; up to buf lines wait for Run. cancel
// is ctx's: the stream's end calls it, and ctx's end must end a read that blocks (a connection closed with ctx, a
// process killed with it). closed is the error Close and ctx's end stop the stream with.
func Start[T any](ctx context.Context, cancel context.CancelFunc, read func() ([]byte, error), buf int, closed error) *Stream[T] {
	s := &Stream[T]{ctx: ctx, cancel: cancel, closed: closed, lines: make(chan []byte, buf), out: make(chan T)}
	go s.read(read)
	return s
}

func (s *Stream[T]) read(read func() ([]byte, error)) {
	defer close(s.lines)
	for {
		b, err := read()
		if err != nil {
			s.mu.Lock()
			s.readErr = err
			s.mu.Unlock()
			return
		}
		select {
		case s.lines <- b:
		case <-s.ctx.Done():
			return
		}
	}
}

// Rules are what Run makes of the lines.
type Rules[T any] struct {
	// Apply reads one line: the events to send (none: nothing), or the error that ends the stream.
	Apply func(line []byte) ([]T, error)
	// End is the error the stream ends with when the source ended (read's error).
	End func(readErr error) error
	// Quiet, when not 0, is how long the stream waits for any line before it ends with QuietErr.
	Quiet    time.Duration
	QuietErr error
}

// Run applies the lines by r in its own goroutine until one ends the stream; it alone sends on and closes C.
func (s *Stream[T]) Run(r Rules[T]) { go s.run(r) }

func (s *Stream[T]) run(r Rules[T]) {
	defer close(s.out)
	var quiet <-chan time.Time
	var timer *time.Timer
	if r.Quiet > 0 {
		timer = time.NewTimer(r.Quiet)
		defer timer.Stop()
		quiet = timer.C
	}
	for {
		var b []byte
		var ok bool
		select {
		case <-s.ctx.Done():
			s.End(s.closed)
			return
		case <-quiet:
			s.End(r.QuietErr)
			return
		case b, ok = <-s.lines:
		}
		if !ok {
			s.mu.Lock()
			err := s.readErr
			s.mu.Unlock()
			s.End(r.End(err))
			return
		}
		if timer != nil {
			timer.Reset(r.Quiet)
		}
		evs, err := r.Apply(b)
		if err != nil {
			s.End(err)
			return
		}
		for _, ev := range evs {
			select {
			case s.out <- ev:
			case <-s.ctx.Done():
			}
		}
	}
}

// C carries the events and closes when the stream ends (Err says why).
func (s *Stream[T]) C() <-chan T { return s.out }

// End stops the stream once with err (a later end keeps the first error): Run then closes C.
func (s *Stream[T]) End(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.cancel()
}

// Err is why the stream ended (nil while it runs).
func (s *Stream[T]) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the stream with the closed error.
func (s *Stream[T]) Close() { s.End(s.closed) }
