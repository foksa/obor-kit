// Package herdrapp is obor's client of the herdr-app window (`herdr-app`), the surface that shows hc's terminals: it
// runs the CLI's documented commands (`herdr-app ls`, `herdr-app do <command> '<json>'`, `herdr-app commands`,
// `herdr-app --version`) as a subprocess, never its socket and never the app itself. A space is a place to work, a tab
// a piece of work in it; obor places a terminal by the labels herdr.space and herdr.tab (docs/research/hc-probes.md
// P7). A window that is not there is typed: the caller places by labels later and never starts the app.
//
// Every failure is an *Error: the CLI's `{"error": …}` reply, a command that printed no id, a version too old, or no
// window answering (which port.NotRunning reads as Unavailable).
package herdrapp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"obor/internal/project"
	"obor/internal/version"
)

// MinVersion is the oldest herdr-app obor drives: the commands and arguments of its command list, and the labels a
// terminal carries into the window.
const MinVersion = "0.1.16"

// The labels herdr-app reads on an hc terminal: the space and tab it shows it in. The ids are local to the machine.
const (
	LabelSpace = "herdr.space"
	LabelTab   = "herdr.tab"
)

// noWindow is what the CLI prints when it finds no window: "herdr-app: no Herdr window at <home>/herdr-app.sock: …",
// exit 2. It is the one sign that the person closed the window: obor then places by labels and waits for the next one.
const noWindow = "no Herdr window"

// Error is every error the client returns.
type Error struct {
	Op  string // the verb or command that failed, e.g. "ls", "space.new", "--version"
	Msg string
	// gone: no window answered (port.NotRunning).
	gone bool
}

func (e *Error) Error() string { return fmt.Sprintf("herdr-app %s: %s", e.Op, e.Msg) }

// Unavailable is port.NotRunning's proof: the window is closed, so the caller places a terminal by labels later
// instead of failing the layout.
func (e *Error) Unavailable() bool { return e.gone }

// Reply is one CLI call's answer: what it printed and its exit code.
type Reply struct {
	Stdout string
	Stderr string
	Code   int
}

// Client is the herdr-app CLI. One client makes its calls one at a time.
type Client struct {
	Command string        // the CLI; "" is "herdr-app"
	Environ []string      // its environment; nil is this process's (HERDR_APP_HOME selects the window)
	Dir     string        // its working directory
	Timeout time.Duration // one call (0: 10 s)
	// Run, when set, answers a call in place of running Command: a window held in memory (herdrapptest.App), whose
	// calls a test can read. Production leaves it nil.
	Run func(ctx context.Context, args []string) (Reply, error)

	mu      sync.Mutex
	checked bool // Version passed once
}

func (c *Client) command() string {
	if c.Command != "" {
		return c.Command
	}
	return "herdr-app"
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return 10 * time.Second
}

// run is the CLI with args: its answer, its exit code included. A call that could not run at all (no CLI, a timeout)
// is an error; a nonzero exit is the answer's.
func (c *Client) run(ctx context.Context, args []string) (Reply, error) {
	if c.Run != nil {
		return c.Run(ctx, args)
	}
	return c.exec(ctx, args)
}

func (c *Client) exec(ctx context.Context, args []string) (Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, c.command(), args...)
	cmd.Env, cmd.Dir = c.Environ, c.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cleanup := project.Group(cmd)
	err := cmd.Run()
	cleanup()
	r := Reply{Stdout: stdout.String(), Stderr: stderr.String()}
	switch {
	case err == nil:
		return r, nil
	case ctx.Err() != nil:
		return Reply{}, &Error{Op: args[0], Msg: "timed out"}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		r.Code = ee.ExitCode()
		return r, nil
	}
	return Reply{}, &Error{Op: args[0], Msg: err.Error()}
}

// call is run with the failure a caller sees: the CLI's no-window answer is the gone error, any other answer is
// returned for the caller to read (a do command's {"error": …} is its answer's).
func (c *Client) call(ctx context.Context, op string, args ...string) (Reply, error) {
	r, err := c.run(ctx, args)
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			e.Op = op
		}
		return Reply{}, err
	}
	if r.Code != 0 && strings.Contains(r.Stderr, noWindow) {
		return Reply{}, &Error{Op: op, Msg: line(r.Stderr), gone: true}
	}
	return r, nil
}

// failure is a nonzero answer as an error: the CLI's own line, else its exit code.
func failure(op string, r Reply) *Error {
	msg := line(r.Stderr)
	if msg == "" {
		msg = line(r.Stdout)
	}
	if msg == "" {
		msg = "exit " + strconv.Itoa(r.Code)
	}
	return &Error{Op: op, Msg: msg}
}

// line is text's first non-blank line, trimmed of the CLI's own "herdr-app: " prefix ("" when it printed none).
func line(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimPrefix(l, "herdr-app: ")
		}
	}
	return ""
}

// Version is the installed app's version, read from `herdr-app --version` ("herdr-app 0.1.16"). An app older than
// MinVersion is an Old error naming the update; an answer with no version in it is an error.
func (c *Client) Version(ctx context.Context) (string, error) {
	r, err := c.call(ctx, "--version", "--version")
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", failure("--version", r)
	}
	v := VersionOf(r.Stdout)
	if v == "" {
		return "", &Error{Op: "--version", Msg: "no version in " + line(r.Stdout)}
	}
	older, ok := version.Older(v, MinVersion)
	switch {
	case !ok:
		return v, &Error{Op: "--version", Msg: fmt.Sprintf("%q is not a version obor reads", v)}
	case older:
		return v, &Error{Op: "--version",
			Msg: fmt.Sprintf("herdr-app %s is older than %s, the oldest obor drives: update Herdr.app", v, MinVersion)}
	}
	return v, nil
}

// VersionOf is the version in a tool's version line: the first field that is a dotted version ("herdr-app 0.1.16" is
// "0.1.16"; "" when there is none).
func VersionOf(text string) string {
	for _, f := range strings.Fields(text) {
		if f == "" || f[0] < '0' || f[0] > '9' {
			continue
		}
		if _, ok := version.Older(f, "0"); ok {
			return f
		}
	}
	return ""
}

// check is the version rule before the first window call: one --version per client, and none after it passed.
func (c *Client) check(ctx context.Context) error {
	c.mu.Lock()
	done := c.checked
	c.mu.Unlock()
	if done {
		return nil
	}
	if _, err := c.Version(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	c.checked = true
	c.mu.Unlock()
	return nil
}
