package wire

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"obor/internal/app/port"
)

// T-685, T-825, T-829: OBOR_CALL_DEADLINE, in seconds (fractions allowed), is every method's deadline when set; unset
// or no number, the table's own. A call whose serve side may stop an agent (a dispatch — a move is one — drop, a
// drive's take-over) covers the stop's one total bound (port.StopBound: the idle wait, the quit wait and the terminate)
// plus the verdict margin. The other methods keep theirs.
func TestCallDeadline(t *testing.T) {
	for _, c := range []struct {
		env, method string
		want        time.Duration
	}{
		{"", MethodAccept, verdictDeadline},
		{"", MethodSendback, verdictDeadline},
		{"", MethodTurn, turnDeadline},
		{"", MethodSession, turnDeadline},
		{"", MethodStopped, stopFailureDeadline},
		{"", MethodDispatch, port.StopBound + verdictDeadline},
		{"", MethodDrop, port.StopBound + verdictDeadline},
		{"", MethodDrive, port.StopBound + verdictDeadline},
		{"0.2", MethodAccept, 200 * time.Millisecond},
		{"0.2", MethodTurn, 200 * time.Millisecond},
		{"0.2", MethodStopped, 200 * time.Millisecond},
		{"0.2", MethodDrive, 200 * time.Millisecond},
		{"bogus", MethodAccept, verdictDeadline},
	} {
		t.Setenv("OBOR_CALL_DEADLINE", c.env)
		if got := callDeadline(c.method); got != c.want {
			t.Errorf("OBOR_CALL_DEADLINE=%q: callDeadline(%s) = %v, want %v", c.env, c.method, got, c.want)
		}
	}
}

// T-825: a verdict its deadline ends says serve may still be working on it, naming the method and the deadline: a stop
// inside the call can outlast the answer, and its transport failure ("use of closed network connection") is not a
// thing a caller can act on.
func TestVerdictDeadlineLine(t *testing.T) {
	dir, err := os.MkdirTemp("", "wire") // a short dir: a unix socket path is limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := dir + "/serve.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept() // accepted, never answered
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	t.Setenv("OBOR_CALL_DEADLINE", "0.2")
	for _, method := range []string{MethodDispatch, MethodDrop, MethodDrive} {
		_, err := Verdict(context.Background(), sock, method, Call{})
		if err == nil {
			t.Fatalf("Verdict(%s) answered from a socket that never does", method)
		}
		want := "obor serve did not answer " + method + " in 200ms: it may still be working on it (obor status shows whether it landed)"
		if err.Error() != want {
			t.Errorf("Verdict(%s) error = %q, want %q", method, err, want)
		}
		if strings.Contains(err.Error(), "closed network connection") {
			t.Errorf("Verdict(%s) error carries the transport's text: %q", method, err)
		}
	}
}

// T-767: a verdict is retried for the transport failures that carried nothing (the socket is not there) or may have
// carried the call to a serve that then went away (a connection closed before its line: a reset, an EOF, and the EPIPE
// of a write to a connection the app closed). Any other failure is the app's answer and is not retried.
func TestRetryClasses(t *testing.T) {
	t.Parallel()
	write := func(err error) error {
		return &net.OpError{Op: "write", Net: "unix", Err: os.NewSyscallError("write", err)}
	}
	for _, c := range []struct {
		name         string
		err          error
		absent, lost bool
	}{
		{"a refused dial", &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, true, false},
		{"a missing socket", &net.OpError{Op: "dial", Net: "unix", Err: os.NewSyscallError("connect", syscall.ENOENT)}, true, false},
		{"a reset connection", write(syscall.ECONNRESET), false, true},
		{"a closed connection", write(syscall.EPIPE), false, true},
		{"an ended stream", write(io.EOF), false, true},
		{"a refused write", write(syscall.EACCES), false, false},
	} {
		if got := absent(c.err); got != c.absent {
			t.Errorf("%s: absent = %v, want %v", c.name, got, c.absent)
		}
		if got := lostReply(c.err); got != c.lost {
			t.Errorf("%s: lostReply = %v, want %v", c.name, got, c.lost)
		}
	}
}
