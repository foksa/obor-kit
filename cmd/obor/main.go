// obor (Go): every command in `commands` (whoami, config, status, pause, run, restart, lead, dispatch, drop,
// resume, send, projects, brief, spec, flakes, nightly,
// milestone-check, plugin (the herdr plugin's restore hook), setup, has, serve, mcp, stop, start, version) and the hooks noop, pre-tool-use, stop, stop-failure, session-start and user-prompt-submit. bin/obor (the kit's one bash
// file) execs this binary for the commands in bin/go-commands.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	// the agent kinds (agents.Kind): each registers itself
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
	_ "obor/internal/agents/opencode"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/guard"
	"obor/internal/project"
	"obor/internal/version"
)

// out is the one stdout writer: buffered, flushed by exit; a closed pipe (`obor status | head`) exits 0 quietly.
var out = bufio.NewWriterSize(os.Stdout, 64<<10)

func exit(code int) {
	if err := out.Flush(); err != nil {
		if errors.Is(err, syscall.EPIPE) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "obor: stdout: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func errf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }

func usage() {
	fmt.Fprintln(out, "usage: obor <subcommand> [args…]")
	fmt.Fprintln(out, "subcommands (Go):")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintln(out, "  "+name)
	}
}

func main() {
	// Handling SIGPIPE turns a write to a closed stdout into EPIPE (exit quietly) instead of a signal death.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGPIPE)
	if len(os.Args) < 2 {
		w := out
		out = bufio.NewWriter(os.Stderr)
		usage()
		out.Flush()
		out = w
		exit(2)
	}
	sub, args := os.Args[1], os.Args[2:]
	switch sub {
	case "help", "-h", "--help":
		usage()
		exit(0)
	case "whoami":
		exit(whoami(args))
	case "status":
		exit(status(args))
	case "monitor":
		exit(monitorCmd(args))
	case "web":
		exit(webCmd(args))
	case "config":
		exit(configCmd(args))
	case "hook":
		exit(hook(args))
	case "has":
		exit(has(args))
	case "restart":
		exit(restart(args))
	case "lead":
		exit(lead(args))
	case "send":
		exit(sendCmd(args))
	case "projects":
		exit(projectsCmd(args))
	case "brief":
		exit(briefCmd(args))
	case "dispatch":
		exit(dispatchCmd(args))
	case "drop":
		exit(dropCmd(args))
	case "drive":
		exit(driveCmd(args))
	case "land":
		exit(landCmd(args))
	case "tell-lead":
		exit(tellLeadCmd(args))
	case "resume":
		exit(resumeCmd(args))
	case "spec":
		exit(specCmd(args))
	case "pause":
		exit(pauseCmd(args))
	case "watch":
		exit(watchCmd(args))
	case "review":
		exit(reviewCmd(args))
	case "run":
		exit(runCmd(args))
	case "affected":
		exit(affectedCmd(args))
	case "flakes":
		exit(flakesCmd(args))
	case "nightly":
		exit(nightlyCmd("nightly", "NIGHTLY", args))
	case "milestone-check":
		exit(nightlyCmd("milestone-check", "MILESTONE", args))
	case "plugin":
		exit(pluginCmd(args))
	case "setup":
		exit(setupCmd(args))
	case "init":
		exit(initCmd(args))
	case "serve":
		exit(serveCmd(args))
	case "mcp":
		exit(mcpCmd(args))
	case "stop":
		exit(stopCmd(args))
	case "start":
		exit(startCmd(args))
	case "version":
		if len(args) != 0 {
			errf("usage: obor version")
			exit(2)
		}
		fmt.Fprintln(out, version.CurrentLine())
		exit(0)
	}
	errf("obor: unknown subcommand: %s", sub)
	exit(2)
}

// load finds the project of the cwd and loads its effective config: exit 3 without .obor/config.json, 1 when broken.
func load() (*project.Repo, *config.Config, int) {
	cwd, _ := os.Getwd()
	repo, err := project.Find(cwd)
	if err != nil {
		errf("obor: not in a git repository (no .obor/config.json)")
		return nil, nil, 3
	}
	cfg, err := config.Load(repo.Root)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, config.ErrNoConfig) {
			errf("%s", strings.TrimSuffix(msg, ": "+config.ErrNoConfig.Error()))
			return nil, nil, 3
		}
		errf("%s", msg)
		return nil, nil, 1
	}
	return repo, cfg, 0
}

// loadReader is load for the readers (obor status, obor monitor; T-709): a config key this binary does not know is
// warn (config.LoadReader), not a failure.
func loadReader() (cfg *config.Config, warn string, rc int) {
	cwd, _ := os.Getwd()
	repo, err := project.Find(cwd)
	if err != nil {
		errf("obor: not in a git repository (no .obor/config.json)")
		return nil, "", 3
	}
	cfg, warn, err = config.LoadReader(repo.Root)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, config.ErrNoConfig) {
			errf("%s", strings.TrimSuffix(msg, ": "+config.ErrNoConfig.Error()))
			return nil, "", 3
		}
		errf("%s", msg)
		return nil, "", 1
	}
	return cfg, warn, 0
}

// commands is what this binary implements: subcommand → its sub-subcommands (nil: takes none). `obor has` reads it,
// so bin/obor runs a binary for a hook only when it knows it (T-195: a stale dist/obor broke every hook).
var commands = map[string][]string{
	"whoami":          nil,
	"init":            nil,
	"status":          nil,
	"monitor":         nil,
	"web":             nil,
	"config":          {"get", "validate"},
	"hook":            {"noop", "pre-tool-use", "stop", "stop-failure", "session-start", "user-prompt-submit"},
	"version":         nil,
	"has":             nil,
	"restart":         nil,
	"lead":            nil,
	"send":            nil,
	"projects":        nil,
	"brief":           nil,
	"dispatch":        nil,
	"drop":            nil,
	"drive":           nil,
	"land":            nil,
	"tell-lead":       nil,
	"resume":          nil,
	"spec":            {"set", "show", "list"},
	"run":             nil,
	"pause":           nil,
	"watch":           nil,
	"review":          {"verify", "close"},
	"affected":        {"go"},
	"flakes":          nil,
	"nightly":         nil,
	"milestone-check": nil,
	"plugin":          {"restore"},
	"serve":           {"status", "install", "uninstall", "restart"},
	"mcp":             nil,
	"stop":            nil,
	"start":           nil,
	"setup":           {"init", "lane", "pane", "role", "githooks", "herdr-plugin", "agents", "opencode-plugin", "monitor", "migrate"},
}

// has is `obor has <subcommand> [<sub-subcommand>]`: exit 0 when implemented, 1 when not; no output.
func has(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		return 2
	}
	subs, ok := commands[args[0]]
	if !ok {
		return 1
	}
	if len(args) == 1 {
		return 0
	}
	for _, s := range subs {
		if s == args[1] {
			return 0
		}
	}
	return 1
}

func hook(args []string) int {
	switch {
	case len(args) == 1 && args[0] == "noop":
		return 0
	case len(args) == 1 && args[0] == "pre-tool-use":
		return preToolUse()
	case len(args) == 1 && args[0] == "stop-failure":
		input, _ := io.ReadAll(os.Stdin)
		stopFailure(input, project.OSEnv)
		return 0
	case len(args) == 1 && (args[0] == "stop" || args[0] == "session-start" || args[0] == "user-prompt-submit"):
		// A hook never fails: an unreadable stdin is an empty event (each hook then allows).
		input, _ := io.ReadAll(os.Stdin)
		switch args[0] {
		case "stop":
			guard.Stop(input, project.OSEnv, out, os.Stderr)
		case "session-start":
			if b, ok := guard.SessionStart(input, project.OSEnv, out, paneLabel); ok {
				sessionBegan(b, os.Stderr)
			}
		default:
			guard.UserPromptSubmit(input, project.OSEnv, out)
		}
		if args[0] != "session-start" {
			turn(input, project.OSEnv, args[0] == "stop")
		}
		return 0
	}
	errf("usage: obor hook noop|pre-tool-use|stop|stop-failure|session-start|user-prompt-submit")
	return 2
}

// stopFailure is the StopFailure hook (T-532): the log line (guard.StopFailure), then one request to the project's
// serve (wire.MethodStopped) with the agent, its error and details, under the call's deadline (wire.Verdict's table:
// 5 s, retried through a serve restart; T-659). Serve down, a refusal or a session with no agent name: a line on
// stderr, nothing else; the hook always exits 0.
func stopFailure(input []byte, env project.Env) {
	s, ok, err := guard.StopFailure(input, env, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "obor stop-failure hook: log:", err)
	}
	if !ok || s.Agent == "" {
		return
	}
	c := wire.Call{Root: s.Root, Agent: s.Agent, Role: s.Role, Lane: s.Lane, CallID: wire.NewCallID(), Error: s.Error,
		Details: s.Details}
	if _, err := wire.Verdict(context.Background(), wire.SocketPath(s.Root), wire.MethodStopped, c); err != nil {
		fmt.Fprintln(os.Stderr, "obor stop-failure hook: serve not told:", err)
	}
}

// turn is a worker's or role agent's UserPromptSubmit or Stop hook (ended) telling the project's serve its next turn
// began or ended (wire.MethodTurn, T-540, T-569; the phase: T-571): a stop in force (its last turn ended on an API
// error) ends. The call's deadline is wire.Verdict's (8 s, retried through a serve restart; T-659). Serve down, a
// refusal, the lead or no role: nothing (no line: the hook's own output is its verdict).
func turn(input []byte, env project.Env, ended bool) {
	s, ok := guard.Turn(input, env)
	if !ok {
		return
	}
	c := wire.Call{Root: s.Root, Agent: s.Agent, Role: s.Role, Lane: s.Lane, CallID: wire.NewCallID(), Ended: ended}
	_, _ = wire.Verdict(context.Background(), wire.SocketPath(s.Root), wire.MethodTurn, c)
}

// sessionBegan is the SessionStart hook telling the project's serve the agent's session began (wire.MethodSession,
// T-577): its id and transcript. Serve down or a refusal: nothing is recorded (the hook's log line says so), and a
// restart resumes the agent's last recorded session, else herdr's (T-607).
func sessionBegan(b guard.Began, log io.Writer) {
	ctx := context.Background() // the call's deadline is wire.Verdict's (8 s; T-659)
	c := wire.Call{Root: b.Root, Agent: b.Agent, Kind: b.Kind, Session: b.ID, Transcript: b.Transcript, Pane: b.Pane,
		Lane: b.Lane, Task: b.Task, Started: time.Now().Unix(), Source: "SessionStart hook"}
	if _, err := wire.Verdict(ctx, wire.SocketPath(b.Root), wire.MethodSession, c); err != nil {
		fmt.Fprintf(log, "obor: session %s of %s not recorded: %v (a restart resumes %s's last recorded session; obor restart %s --fresh starts a new one)\n",
			b.ID, b.Agent, err, b.Agent, b.Agent)
	}
}

// paneLabel is the label of a herdr pane: one `herdr pane get` bounded by 3 s; "" on any error (the SessionStart
// hook's lost-identity warning).
func paneLabel(pane string) string {
	c := herdrSocket(project.OSEnv)
	c.Timeout = 3 * time.Second
	p, err := c.Pane(context.Background(), pane)
	if err != nil {
		return ""
	}
	return p.Label
}

// preToolUse is the PreToolUse hook (bin/obor execs it): the hook JSON on stdin; allow = exit 0, no output; deny = exit 2, one line
// "obor: …" on stderr. Anything else (unreadable stdin, an internal error) allows.
func preToolUse() int {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0
	}
	code, reason := guard.Run(input, project.OSEnv)
	if code == 2 {
		errf("obor: %s", reason)
	}
	return code
}
