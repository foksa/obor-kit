package main

// The interrupt path (T-816, RV-104 #3): Ctrl-C or SIGTERM stops every open throwaway session and exits 130. This file
// carries no build tag, so its test (interrupt_test.go) runs the path in `go test ./...` with a stub lab: no herdr, no
// session.

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// closable is a lab the interrupt stops: the live tier's lab, or the test's stub.
type closable interface{ close() }

var (
	labsMu sync.Mutex
	labs   = map[closable]bool{} // open labs, closed on an interrupt too
)

// openLab registers a lab the run now owns: the interrupt closes it too.
func openLab(l closable) {
	labsMu.Lock()
	labs[l] = true
	labsMu.Unlock()
}

// stopLabs closes every open lab once. It snapshots the labs under the mutex and closes them after unlocking: a close
// takes the mutex itself (it forgets the lab), so closing under it deadlocked the interrupt (RV-104 #3).
func stopLabs() {
	labsMu.Lock()
	open := make([]closable, 0, len(labs))
	for l := range labs {
		open = append(open, l)
	}
	labsMu.Unlock()
	for _, l := range open {
		l.close()
	}
}

// interrupt is the signal handler: it tells the runner to stop (done is closed before the first close, so no lab is
// taken up after it), stops the labs and exits 130.
func interrupt(sig <-chan os.Signal, done chan struct{}, say io.Writer, exit func(int)) {
	<-sig
	fmt.Fprintln(say, "conformance: interrupted, stopping the throwaway sessions")
	close(done)
	stopLabs()
	exit(130)
}
