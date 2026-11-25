package rex

import (
	"context"
	"slices"
	"strings"
	"time"
)

// env.go: a block's environment. A Rex block inherits the server's whole environment (docs/research/rex-probes.md
// R4), so whoever started the server (an obor pane, a Claude session) leaks its identity into every agent. Every block
// obor makes therefore starts its shell through `env -u` for each such name in the server's environment as a block
// reports it (envProbe: a detached block that runs /usr/bin/env), then sets the block's own variables; obor's own
// identity is then set by launch's env line, as on any terminal. A block's options take no environment of their own.

// scrubPrefixes and scrubNames are the names no block obor makes keeps from the server's environment: obor's, herdr's
// and hc's pane identity, and a Claude session's (whose markers put a Claude started in the block into its
// parent's permission mode).
var (
	scrubPrefixes = []string{"OBOR_", "HERDR_", "HC_", "CLAUDE_CODE_"}
	scrubNames    = []string{"CLAUDECODE"}
)

// scrubbed is the names of environ (NAME=VALUE lines, as `env` prints them; any other line is skipped) that no block
// may keep, sorted, each once.
func scrubbed(environ []string) []string {
	var out []string
	for _, line := range environ {
		name, _, ok := strings.Cut(line, "=")
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			continue
		}
		hit := slices.Contains(scrubNames, name)
		for _, p := range scrubPrefixes {
			hit = hit || strings.HasPrefix(name, p)
		}
		if hit && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// blockEnv is what a block obor makes needs of the server's environment: the names to unset and the login shell to run
// (SHELL).
type blockEnv struct {
	Unset []string
	Shell string
}

// readEnv is blockEnv of environ; an environment that names no SHELL is an error (no shell is guessed).
func readEnv(environ []string) (blockEnv, error) {
	e := blockEnv{Unset: scrubbed(environ)}
	for _, line := range environ {
		if v, ok := strings.CutPrefix(line, "SHELL="); ok {
			e.Shell = v
		}
	}
	if e.Shell == "" {
		return blockEnv{}, errorf("env", "the Rex server's environment names no SHELL: no shell to start a block with")
	}
	return e, nil
}

// command is the block's program: /usr/bin/env unsetting e.Unset and setting env (NAME=VALUE; "NAME=": set empty),
// then e.Shell as a login shell. env applies every -u before the assignments, so a name both unsets and sets ends set.
func (e blockEnv) command(env []string) []string {
	out := []string{"/usr/bin/env"}
	for _, n := range e.Unset {
		out = append(out, "-u", n)
	}
	out = append(out, env...)
	return append(out, e.Shell, "-l")
}

// shellOptions are the shell flavor's options obor sends (contract.go's Options): no shell of Rex's around the
// command, the cwd, and (the env probe's) the block kept once its program exits.
type shellOptions struct {
	Shell   string     `json:"shell"`
	Command []string   `json:"command"`
	Cwd     string     `json:"cwd,omitempty"`
	Exit    *exitRules `json:"exit,omitempty"`
}

type exitRules struct {
	OnCompletion bool `json:"on_completion"`
}

// probeWait bounds the env probe's wait for its program to exit; probePoll is how often it looks.
const (
	probeWait = 5 * time.Second
	probePoll = 50 * time.Millisecond
)

// envProbe is the server's environment as a block reports it: a detached block (session.new_block, shown in no
// window) in session runs /usr/bin/env and is kept once it exits; its text is read (format, lines unwrapped) and the
// block closed.
func (cl *Client) envProbe(ctx context.Context, session string) (blockEnv, error) {
	var nb struct {
		BlockID string `json:"block_id"`
	}
	opts := shellOptions{Shell: "none", Command: []string{"/usr/bin/env"}, Exit: &exitRules{OnCompletion: false}}
	if err := cl.call(ctx, &nb, "session.new_block", map[string]any{"session_id": session, "flavor": ShellFlavor, "options": opts}); err != nil {
		return blockEnv{}, err
	}
	if nb.BlockID == "" {
		return blockEnv{}, errorf("session.new_block", "no block id in the answer")
	}
	defer func() {
		_ = cl.call(context.WithoutCancel(ctx), nil, "block.close", map[string]any{"session_id": session, "block_id": nb.BlockID})
	}()
	for end := time.Now().Add(probeWait); ; {
		var proc Processes
		if err := cl.call(ctx, &proc, Terminal+".process", blockParams(session, nb.BlockID, nil)); err != nil {
			return blockEnv{}, err
		}
		if proc.Child == nil && proc.LastExit != nil {
			break
		}
		if time.Now().After(end) {
			return blockEnv{}, errorf("env", "the env probe block %s did not exit within %s", nb.BlockID, probeWait)
		}
		select {
		case <-ctx.Done():
			return blockEnv{}, ctx.Err()
		case <-time.After(probePoll):
		}
	}
	text, err := cl.format(ctx, session, nb.BlockID, 0, true)
	if err != nil {
		return blockEnv{}, err
	}
	return readEnv(strings.Split(text, "\n"))
}
