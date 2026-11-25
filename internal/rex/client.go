// Package rex is obor's client of Rex, the third terminal adapter (docs/plan/terminal-adapters.md step 9): the `rex`
// CLI found on PATH, run once per request (`rex api call <method> '<json>'`), and one long-lived `rex events --all
// --json` as the event stream. It implements port.Panes (Panes: the snapshot, the stream, Notify). A pane is a
// terminal block: the block's id is the pane's id, its session's label the pane's group and its own label the pane's
// label. No project's terminal is Rex yet.
//
// Every failure is an *Error: Rex's own answer carries its code (not_found and session_not_found → port.NotFound); a
// server that does not answer (the CLI's exit status 4: none runs at the address, or it went away) is port.NotRunning.
package rex

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/project"
)

// Error is every error the client returns.
type Error struct {
	Op   string // the method, e.g. "session.list"; "events" for the stream
	Code string // Rex's error code (not_found, session_not_found, method_not_found, …); "" when Rex sent no answer
	Msg  string
	// down: no server answered (exit status 4), or the call timed out or was cancelled (port.NotRunning).
	down bool
	// refresh: the stream ended only so the caller reads a new snapshot and subscribes again (port.Refreshed).
	refresh bool
}

// NotFound, Unavailable, Refreshed and Answered let the core read an *Error without importing rex (port.NotFound,
// port.NotRunning, port.Refreshed, port.Answered). A block or a session Rex does not know is not found.
func (e *Error) NotFound() bool    { return e.Code == "not_found" || e.Code == "session_not_found" }
func (e *Error) Unavailable() bool { return e.down }
func (e *Error) Refreshed() bool   { return e.refresh }
func (e *Error) Answered() bool    { return e.Code != "" }

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("rex %s: %s (%s)", e.Op, e.Msg, e.Code)
	}
	return fmt.Sprintf("rex %s: %s", e.Op, e.Msg)
}

// Client runs the rex CLI.
type Client struct {
	// Bin is the CLI ("": rex on PATH; never a path guessed inside the app bundle).
	Bin string
	// Server is the server's address (-S unix:///…/s.sock), "": the CLI's default, the server the Rex app shows.
	Server string
	// NoAutostart passes --autostart=false: a call to a server that does not run fails (exit status 4) instead of
	// starting one.
	NoAutostart bool
	// Env, when set, is the CLI's whole environment (nil: obor's own). A scratch server's clients need its
	// XDG_RUNTIME_DIR, which moves the CLI's server registry.
	Env []string
	// Timeout bounds each call (0: 10 s).
	Timeout time.Duration
	// Kinds are the project's agent kinds (config.Config.Kinds), read at each use: a status's app names one of them.
	// nil: the compiled kinds.
	Kinds func() agents.Kinds
}

// kinds are the kinds now (Client.Kinds).
func (cl *Client) kinds() agents.Kinds {
	if cl.Kinds == nil {
		return agents.Kinds{}
	}
	return cl.Kinds()
}

func (cl *Client) timeout() time.Duration {
	if cl.Timeout > 0 {
		return cl.Timeout
	}
	return 10 * time.Second
}

// args are the CLI's global options, then rest.
func (cl *Client) args(rest ...string) []string {
	var a []string
	if cl.Server != "" {
		a = append(a, "-S", cl.Server)
	}
	if cl.NoAutostart {
		a = append(a, "--autostart=false")
	}
	return append(a, rest...)
}

// command is the CLI with args, bounded by ctx; an error when no rex is on PATH.
func (cl *Client) command(ctx context.Context, op string, args ...string) (*exec.Cmd, error) {
	bin := cl.Bin
	if bin == "" {
		p, err := exec.LookPath("rex")
		if err != nil {
			return nil, &Error{Op: op, Msg: "no rex on PATH (Rex's CLI ships in the app bundle: put its directory on PATH)"}
		}
		bin = p
	}
	cmd := exec.CommandContext(ctx, bin, cl.args(args...)...)
	cmd.Env = cl.Env
	return cmd, nil
}

// call runs one `rex api call method params` and decodes its JSON answer into out (nil: not read).
func (cl *Client) call(ctx context.Context, out any, method string, params any) error {
	return cl.callStarted(ctx, out, method, params, nil)
}

// callStarted is call, with started (when set) told the CLI's pid once it runs: the stream's client_joined line names
// that pid (Panes.Subscribe's sync).
func (cl *Client) callStarted(ctx context.Context, out any, method string, params any, started func(pid int)) error {
	ctx, cancel := context.WithTimeout(ctx, cl.timeout())
	defer cancel()
	if params == nil {
		params = struct{}{}
	}
	p, _ := json.Marshal(params)
	cmd, err := cl.command(ctx, method, "api", "call", method, string(p))
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return &Error{Op: method, Msg: "rex did not start: " + err.Error()}
	}
	if started != nil {
		started(cmd.Process.Pid)
	}
	if err := cmd.Wait(); err != nil {
		var ee *exec.ExitError
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &Error{Op: method, Msg: "timed out", down: true}
		case ctx.Err() != nil:
			return &Error{Op: method, Msg: ctx.Err().Error(), down: true}
		case errors.As(err, &ee):
			return exitError(method, ee.ExitCode(), stderr.String())
		}
		return &Error{Op: method, Msg: err.Error()}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return &Error{Op: method, Msg: "unreadable answer: " + project.FirstLine(strings.TrimSpace(stdout.String()))}
	}
	return nil
}

// codeRe is the code at the end of Rex's error line: `… (status=404 code=not_found)`; streamRe the CLI's own prefix
// before Rex's message.
var (
	codeRe   = regexp.MustCompile(`\(status=\d+ code=([a-z_]+)\)\s*$`)
	streamRe = regexp.MustCompile(`^stream response "[^"]*" failed: `)
)

// exitError is a CLI run that exited with code as an *Error, from its stderr: exit status 4 is no server
// (port.NotRunning: none runs at the address with autostart off, or it went away); a line ending in Rex's
// `(status=… code=…)` carries that code; any other failure only its message and status.
func exitError(op string, code int, stderr string) *Error {
	msg := streamRe.ReplaceAllString(strings.TrimPrefix(project.FirstLine(strings.TrimSpace(stderr)), "Error: "), "")
	if code == 4 {
		return &Error{Op: op, Msg: cmp.Or(msg, "no Rex server answers"), down: true}
	}
	if m := codeRe.FindStringSubmatch(msg); m != nil {
		return &Error{Op: op, Code: m[1], Msg: strings.TrimSpace(codeRe.ReplaceAllString(msg, ""))}
	}
	return &Error{Op: op, Msg: fmt.Sprintf("%s (exit status %d)", cmp.Or(msg, "rex failed"), code)}
}

// blockParams are a block method's params: its block, the session that holds it and the method's own (empty) args.
func blockParams(session, block string) map[string]any {
	return map[string]any{"session_id": session, "block_id": block, "args": map[string]any{}}
}
