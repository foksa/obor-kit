package hc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

// env.go: the core's environment. Every terminal gets the core's environment (docs/research/hc-probes.md P4), and a
// core keeps the environment of the process that started it across `hc update` and `hc server restart`. Any `hc` call
// that needs a core starts one with its caller's environment (P11), so one call from an obor pane (or another
// terminal's) while no core runs puts that one's identity (HERDR_PANE_ID, OBOR_ROLE, REX_BLOCK) into every later
// terminal. obor reads the core's environment and refuses such a core (CheckEnv), and serve starts a core that is down
// from a scrubbed environment (Core); it never restarts or stops a core that runs.

// LeakLine is the refusal of a core whose environment holds an identity name of another terminal, of obor or of a
// Claude session.
const LeakLine = "hc's core was started from an obor pane or another terminal: run `hc server restart --fresh` from a " +
	"shell outside any pane"

// Leaked is the identity names a core must not carry (project.ForeignIdentity: another terminal's, obor's, a Claude
// session's) that env holds with a value, sorted: an empty value reads as unset (terminal.run's env sets a name empty,
// it never unsets one).
func Leaked(env map[string]string) []string {
	var out []string
	for k, v := range env {
		if v != "" && project.ForeignIdentity(config.TerminalHC, k) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// Scrub is environ (NAME=VALUE entries) without the identity names of other terminals a core must not get: the same
// names Leaked reports. hc's own names stay: the session's (HC_SESSION, HC_SOCKET, the XDG_ dirs) pick the core, and
// HC_TERMINAL_ is this kind's own identity.
func Scrub(environ []string) []string {
	var out []string
	for _, kv := range environ {
		if name, _, _ := strings.Cut(kv, "="); !project.ForeignIdentity(config.TerminalHC, name) {
			out = append(out, kv)
		}
	}
	return out
}

// CoreEnv is the core's environment: machine.exec of /usr/bin/env, which runs with it (P4).
func (cl *Client) CoreEnv(ctx context.Context) (map[string]string, error) {
	var r struct {
		Code   *int   `json:"code"`
		Stdout []byte `json:"stdout"` // base64 in the answer
	}
	if err := cl.call(ctx, &r, "machine.exec", map[string]any{"argv": []string{"/usr/bin/env"}}); err != nil {
		return nil, err
	}
	if r.Code == nil || *r.Code != 0 {
		return nil, &Error{Op: "machine.exec", Msg: "/usr/bin/env did not exit 0: the core's environment is not read"}
	}
	env := map[string]string{}
	for _, kv := range strings.Split(string(r.Stdout), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = v
		}
	}
	return env, nil
}

// CheckEnv is nil when the core's environment holds no identity name of another terminal; else the refusal, LeakLine
// with the names. A core that does not answer is the read's error (port.NotRunning).
func (cl *Client) CheckEnv(ctx context.Context) error {
	env, err := cl.CoreEnv(ctx)
	if err != nil {
		return err
	}
	if names := Leaked(env); len(names) > 0 {
		return &Error{Op: "core env", Msg: fmt.Sprintf("%s (its environment holds %s)", LeakLine, strings.Join(names, ", "))}
	}
	return nil
}

// Check is `obor setup check`'s line for the core: its environment holds no pane's identity, or it is not running
// (serve starts it). The refusal is the error.
func (cl *Client) Check(ctx context.Context) (string, error) {
	switch err := cl.CheckEnv(ctx); {
	case port.NotRunning(err):
		return "hc's core is not running: obor serve starts it from a scrubbed environment", nil
	case err != nil:
		return "", err
	}
	return "hc's core environment holds no other terminal's or agent's identity", nil
}

// StartBound bounds `hc server start`.
const StartBound = 30 * time.Second

// Core is serve's hold on the core (Panes.Core): before each subscribe a core that is down is started from Environ
// scrubbed (Scrub), and a core whose environment holds another terminal's identity is refused (CheckEnv). A core that
// runs is never restarted or stopped.
type Core struct {
	Environ []string // serve's own environment (os.Environ())
	// Run runs the hc CLI with args and env (nil: hc on PATH, bounded by StartBound).
	Run func(ctx context.Context, env []string, args ...string) ([]byte, error)
}

// ready makes sure cl's core runs and its environment is clean: the check, and when no core answers a start and the
// check again.
func (c *Core) ready(ctx context.Context, cl *Client) error {
	err := cl.CheckEnv(ctx)
	if !port.NotRunning(err) {
		return err
	}
	run := c.Run
	if run == nil {
		run = runHC
	}
	if out, err := run(ctx, Scrub(c.Environ), "server", "start"); err != nil {
		return &Error{Op: "server start", Msg: fmt.Sprintf("hc server start: %v: %s", err, strings.TrimSpace(string(out))),
			down: true}
	}
	return cl.CheckEnv(ctx)
}

// runHC runs hc on PATH with env and args, bounded by StartBound. The core it starts is not its child to wait for
// (launchd's job on macOS): WaitDelay ends the wait on output a detached process still holds.
func runHC(ctx context.Context, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, StartBound)
	defer cancel()
	cmd := exec.CommandContext(ctx, "hc", args...)
	cmd.Env, cmd.Stdin, cmd.WaitDelay = env, nil, 2*time.Second
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	return cmd.CombinedOutput()
}
