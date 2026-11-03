package wire

import (
	"io"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// T-685: OBOR_CALL_DEADLINE, in seconds (fractions allowed), is every method's deadline when set; unset or no number,
// the table's own.
func TestCallDeadlineEnv(t *testing.T) {
	for _, c := range []struct {
		env, method string
		want        time.Duration
	}{
		{"", MethodAccept, verdictDeadline},
		{"", MethodTurn, turnDeadline},
		{"", MethodStopped, stopFailureDeadline},
		{"0.2", MethodAccept, 200 * time.Millisecond},
		{"0.2", MethodTurn, 200 * time.Millisecond},
		{"0.2", MethodStopped, 200 * time.Millisecond},
		{"bogus", MethodAccept, verdictDeadline},
	} {
		t.Setenv("OBOR_CALL_DEADLINE", c.env)
		if got := callDeadline(c.method); got != c.want {
			t.Errorf("OBOR_CALL_DEADLINE=%q: callDeadline(%s) = %v, want %v", c.env, c.method, got, c.want)
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
