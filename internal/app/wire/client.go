package wire

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// Ask is the status of the app listening on the unix socket at path.
func Ask(ctx context.Context, path string) (Status, error) {
	ans, err := ask(ctx, path, Request{Method: "status"})
	if err != nil {
		return Status{}, err
	}
	if ans.Status == nil {
		return Status{}, errors.New("no status in the answer")
	}
	return *ans.Status, nil
}

// SocketPath is the app socket of the serve of the project at root: <root>/.obor/state/serve.sock (one serve per
// project, T-522).
func SocketPath(root string) string { return filepath.Join(root, ".obor", "state", "serve.sock") }

// NewCallID is a new call's id: a repeated call id gets the app's stored reply, so every call (a CLI request, an MCP
// tool call) has its own.
func NewCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Verdict sends the verdict call c (MethodAccept, …) to the app listening at path: its reply line, or the app's
// refusal as the error. The call has its method's deadline (callDeadline) or ctx's, the earlier.
func Verdict(ctx context.Context, path, method string, c Call) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callDeadline(method))
	defer cancel()
	ans, err := verdictAsk(ctx, path, Request{Method: method, Call: &c})
	if err != nil {
		return "", err
	}
	return ans.Reply, nil
}

// The deadlines of the socket's callers, the one table (T-659): a call is retried inside its own deadline, so a serve
// restart that ends before it loses nothing. The hooks' are under their timeouts (10 s and 15 s); status has none: a
// read fails at once. OBOR_CALL_DEADLINE, in seconds (fractions allowed: 0.2), is every method's deadline when set: a
// test's call to a serve that is not running ends in that time, not in the table's (T-685); unset, the table.
const (
	verdictDeadline     = 15 * time.Second // a CLI verdict, an MCP tool's call
	turnDeadline        = 8 * time.Second  // a hook's turn and SessionStart (session) calls
	stopFailureDeadline = 5 * time.Second  // the StopFailure hook's stopped call
)

// callDeadline is method's deadline: OBOR_CALL_DEADLINE when it is set to a positive number of seconds, else the table
// above.
func callDeadline(method string) time.Duration {
	if s := os.Getenv("OBOR_CALL_DEADLINE"); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	switch method {
	case MethodTurn, MethodSession:
		return turnDeadline
	case MethodStopped:
		return stopFailureDeadline
	}
	return verdictDeadline
}

// ask's retries (T-659): every askRetryWait, flat, and none later than askMargin before the call's deadline, so the
// last attempt still has time to be answered.
const (
	askRetryWait = 500 * time.Millisecond
	askMargin    = 200 * time.Millisecond
)

// ask sends req once to the app socket at path and reads its answer; an answer's error is the error. It is not
// retried: obor status reports a serve that is not there at once.
func ask(ctx context.Context, path string, req Request) (Answer, error) {
	return askOnce(ctx, path, req)
}

// verdictAsk is ask for a verdict's request (a CLI verdict, an MCP tool's, a hook's: task_done, job_next, turn …),
// retried until ctx's deadline less askMargin while it cannot have done harm: a refused or missing socket carried
// nothing (serve binds only once its project is loaded, so a restart is a socket not there yet), and a reply lost
// after the request was written is sent again with the same call id, which the app's stored reply answers
// (docs/state-model.md, class F). Any other error, the app's refusal included, is returned at once.
func verdictAsk(ctx context.Context, path string, req Request) (Answer, error) {
	last := time.Now().Add(-askMargin) // no deadline: one attempt
	if d, ok := ctx.Deadline(); ok {
		last = d.Add(-askMargin)
	}
	for {
		ans, err := askOnce(ctx, path, req)
		if err == nil || ctx.Err() != nil || !(absent(err) || lostReply(err)) {
			return ans, err
		}
		wait := min(askRetryWait, time.Until(last))
		if wait <= 0 {
			return Answer{}, err
		}
		select {
		case <-ctx.Done():
			return Answer{}, err
		case <-time.After(wait):
		}
	}
}

// lostReply is a transport failure that may follow an app which recorded the call and then went away unanswered: the
// connection closed before a line, or reset.
func lostReply(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF)
}

// absent is a request that certainly did not land: the dial was refused or the socket missing (no serve, or one that
// has not loaded its project yet: it binds after its replay, T-659).
func absent(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// askOnce is one request on its own connection; see ask.
func askOnce(ctx context.Context, path string, req Request) (Answer, error) {
	ctx, cancel := context.WithTimeout(ctx, verdictDeadline) // a caller with no deadline still has one
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return Answer{}, fmt.Errorf("obor serve is not running (%w)", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	b, _ := json.Marshal(req) // strings only: cannot fail
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Answer{}, err
	}
	b, err = bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return Answer{}, err
	}
	var ans Answer
	if err := json.Unmarshal(b, &ans); err != nil {
		return Answer{}, err
	}
	if ans.Error != "" {
		return Answer{}, errors.New(ans.Error)
	}
	return ans, nil
}
