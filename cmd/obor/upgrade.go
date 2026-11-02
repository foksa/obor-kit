package main

// obor projects --upgrade [--notify <file>] (T-715): the follow-through of `make ship`. With the new binary installed,
// for each project of the machine's list (obor projects; a stopped project is not there): its serve restarted; then
// every herdr pane whose foreground is an `obor monitor` or `obor web` of another binary than this one gets Ctrl-C and
// the same command again; with --notify each project's lead but the caller's own project's is told
// "obor v<version> is installed and your serve is restarted. <notes>". One line per step; a failing step is reported
// and the rest go on (exit 1 at the end).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/version"
)

const upgradeUsage = "usage: obor projects --upgrade [--notify <release notes file>]"

// upgradePanes is the part of herdr the upgrade uses.
type upgradePanes interface {
	Panes(ctx context.Context) ([]port.PaneInfo, error)
	ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error)
	SendKeys(ctx context.Context, pane string, keys ...string) error
	Type(ctx context.Context, pane, text string) error
}

// upgrader is one `obor projects --upgrade`; the funcs are its outside systems (tests fake them).
type upgrader struct {
	Out      io.Writer
	Version  string // this binary's version, as the message says it
	Binary   string // this binary's path: a monitor or web on another one is old
	Projects []project.Project
	Own      string // the root of the caller's own project ("": none): its lead is not told
	Notes    string // the release notes to tell the leads ("" with Notify false)
	Notify   bool
	H        upgradePanes // nil: herdr is not reachable
	// Restart restarts one project's serve (its result line); Send tells one project's lead.
	Restart func(ctx context.Context, p project.Project) (string, error)
	Send    func(ctx context.Context, p project.Project, text string) error
	// Wait bounds the wait for a pane's shell to get the foreground back after Ctrl-C; Poll is its step.
	Wait, Poll time.Duration
	failed     bool
}

func (u *upgrader) say(format string, a ...any) { fmt.Fprintf(u.Out, format+"\n", a...) }

func (u *upgrader) fail(format string, a ...any) {
	u.failed = true
	u.say("FAIL "+format, a...)
}

// projectsUpgrade is `obor projects --upgrade [--notify <file>]`.
func projectsUpgrade(args []string) int {
	notes, notify := "", false
	upgrade := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--upgrade":
			upgrade = true
		case a == "--notify" && i+1 < len(args):
			i, notify = i+1, true
			b, err := os.ReadFile(args[i])
			if err != nil || strings.TrimSpace(string(b)) == "" {
				fmt.Fprintf(out, "FAIL --notify: %s is missing or empty\n", args[i])
				return 1
			}
			notes = strings.Join(strings.Fields(string(b)), " ")
		default:
			fmt.Fprintln(out, "FAIL "+upgradeUsage)
			return 1
		}
	}
	if !upgrade {
		fmt.Fprintln(out, "FAIL "+upgradeUsage)
		return 1
	}
	env := project.OSEnv
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintf(out, "FAIL cannot tell which binary this is: %v\n", err)
		return 1
	}
	u := &upgrader{Out: out, Version: version.Version, Binary: bin, Projects: project.Projects(env), Notes: notes,
		Notify: notify, H: herdrSocket(env), Wait: 10 * time.Second, Poll: 200 * time.Millisecond}
	var own *config.Config
	if repo, err := project.Find(mustGetwd()); err == nil {
		own, _ = config.LoadDaemon(repo.Root)
	}
	if own != nil {
		u.Own = own.Root
	}
	if notify && own == nil {
		fmt.Fprintln(out, "FAIL --notify needs the sending project: run it in the kit checkout")
		return 1
	}
	u.Restart = func(ctx context.Context, p project.Project) (string, error) { return restartServe(ctx, p.Root, env) }
	u.Send = func(ctx context.Context, p project.Project, text string) error {
		return sendTo(ctx, env, own, p.Root, text)
	}
	if u.run(context.Background()) {
		return 0
	}
	return 1
}

func mustGetwd() string {
	cwd, _ := os.Getwd()
	return cwd
}

// restartServe is `obor serve restart` in the project at root: launchd's agent restarted.
func restartServe(ctx context.Context, root string, env project.Env) (string, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return serviceOf(cfg, env).Restart(ctx)
}

// sendTo is obor send: text to the lead of the project at root, from the project own.
func sendTo(ctx context.Context, env project.Env, own *config.Config, root, text string) error {
	cwd, _ := os.Getwd()
	c := caller(env, cwd, own)
	c.Root, c.Note, c.From = root, text, own.Data.Name
	if c.From == "" {
		c.From = filepath.Base(own.Root)
	}
	_, err := wire.Verdict(ctx, wire.SocketPath(root), wire.MethodSend, c)
	return err
}

// run does the upgrade; it reports whether every step went through.
func (u *upgrader) run(ctx context.Context) bool {
	if len(u.Projects) == 0 {
		u.say("no obor projects known yet (obor serve and obor init register one)")
	}
	restarted := map[string]bool{}
	for _, p := range u.Projects {
		line, err := u.Restart(ctx, p)
		if err != nil {
			u.fail("%s: serve restart: %v", p.Name, err)
			continue
		}
		restarted[p.Name] = true
		u.say("%s: serve restart: %s", p.Name, oneLine(line))
	}
	u.panes(ctx)
	if !u.Notify {
		return !u.failed
	}
	for _, p := range u.Projects {
		switch {
		case u.Own != "" && project.Physical(p.Root) == project.Physical(u.Own):
			u.say("%s: not told (this project's own lead)", p.Name)
		case !restarted[p.Name]:
			u.say("%s: not told (its serve was not restarted)", p.Name)
		default:
			text := fmt.Sprintf("obor %s is installed and your serve is restarted. %s", u.Version, u.Notes)
			if err := u.Send(ctx, p, text); err != nil {
				u.fail("%s: send: %v", p.Name, err)
				continue
			}
			u.say("%s: lead told", p.Name)
		}
	}
	return !u.failed
}

// panes restarts the monitor and web processes of another binary than this one.
func (u *upgrader) panes(ctx context.Context) {
	if u.H == nil {
		return
	}
	panes, err := u.H.Panes(ctx)
	if err != nil {
		u.fail("herdr panes: %v (monitors and web left as they are)", err)
		return
	}
	for _, pane := range panes {
		pi, err := u.H.ProcessInfo(ctx, pane.ID)
		if err != nil || pi.AtShell() {
			continue // a pane that is gone or at its shell is nobody's monitor
		}
		argv := foregroundArgv(pi)
		if len(argv) < 2 || !isObor(argv[0]) || (argv[1] != "monitor" && argv[1] != "web") {
			continue
		}
		u.restartPane(ctx, pane.ID, argv)
	}
}

// foregroundArgv is the argv of the pane's foreground group leader (else its first process).
func foregroundArgv(pi port.ProcessInfo) []string {
	var lead *port.Process
	for i := range pi.Processes {
		if pi.Processes[i].PID == pi.FgPGID {
			lead = &pi.Processes[i]
		}
	}
	if lead == nil && len(pi.Processes) > 0 {
		lead = &pi.Processes[0]
	}
	if lead == nil {
		return nil
	}
	if len(lead.Argv) > 0 {
		return lead.Argv
	}
	return strings.Fields(lead.Cmdline)
}

// isObor: argv0 is an obor binary: obor, or a release binary (obor-<version>-<os>-<arch>).
func isObor(argv0 string) bool {
	base := filepath.Base(argv0)
	return base == "obor" || strings.HasPrefix(base, "obor-")
}

// sameBinary: the process's argv0 is this binary.
func (u *upgrader) sameBinary(argv0 string) bool {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	return real(argv0) == real(u.Binary)
}

func (u *upgrader) restartPane(ctx context.Context, pane string, argv []string) {
	what := "obor " + strings.Join(argv[1:], " ")
	switch {
	case !strings.Contains(argv[0], "/"):
		u.say("pane %s: %s left (its binary %q is no path: version unknown)", pane, what, argv[0])
		return
	case u.sameBinary(argv[0]):
		u.say("pane %s: %s is on this binary already", pane, what)
		return
	}
	if err := u.H.SendKeys(ctx, pane, "ctrl+c"); err != nil {
		u.fail("pane %s: %s: Ctrl-C: %v", pane, what, err)
		return
	}
	if err := u.atShell(ctx, pane); err != nil {
		u.fail("pane %s: %s: %v (not started again)", pane, what, err)
		return
	}
	cmd := "obor"
	for _, a := range argv[1:] {
		cmd += " " + shellQuote(a)
	}
	if err := u.H.Type(ctx, pane, cmd); err != nil {
		u.fail("pane %s: %s: typing it again: %v", pane, what, err)
		return
	}
	u.say("pane %s: %s restarted (was %s)", pane, what, argv[0])
}

// atShell waits for the pane's shell to get the foreground back.
func (u *upgrader) atShell(ctx context.Context, pane string) error {
	deadline := time.Now().Add(u.Wait)
	for {
		pi, err := u.H.ProcessInfo(ctx, pane)
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			if port.NotFound(err) {
				return fmt.Errorf("the pane is gone")
			}
		case err == nil && pi.AtShell():
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("still running %s after Ctrl-C", u.Wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(u.Poll):
		}
	}
}

// shellQuote quotes a for a shell unless it is plain.
func shellQuote(a string) string {
	plain := a != ""
	for _, r := range a {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@%+,", r)) {
			plain = false
		}
	}
	if plain {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}
