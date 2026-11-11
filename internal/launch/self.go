package launch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
)

// A restart (or stop) of the caller's own pane: `obor restart` run by the agent itself can neither stop it in
// the foreground (the /exit ends the restart too) nor as a background task (it is the task). So it starts a detached
// `obor restart` (own session, outside the agent's process tree), says SCHEDULED and exits; that run (ScheduledEnv
// set) waits until the agent is idle, ≤ Sched (10 min), then restarts it as usual, from the top: who, where and which
// session are decided after the wait. Its output goes to
// .obor/state/restart-<agent>.log; giving up is logged with evidence in launch.log, like any other give-up.

// ScheduledEnv marks the detached run of a scheduled restart or stop; its value is the operation (restart or stop),
// which the give-up text names.
const ScheduledEnv = "OBOR_RESTART_SCHEDULED"

// ownPane: t's pane is the caller's (project.Pane; none: the caller runs in no pane).
func (l *Launcher) ownPane(t *target) bool {
	p, err := project.Pane(l.envOrOS())
	return err == nil && p == t.pane
}

// schedule starts the detached run of req (stop: --stop) and returns the SCHEDULED line.
func (l *Launcher) schedule(t *target, req Request, stop bool) (string, error) {
	// The pane resolved here is the target's; Request.Args owns every other field.
	req.Agent, req.Pane = t.agent, t.pane
	args, what := append([]string{"restart"}, Args(req)...), "restart"
	if stop {
		args, what = append(args, "--stop"), "stop"
	}
	// The run loads its project from its cwd: this project's main checkout, never the caller's cwd (a herdr
	// plugin kick runs in the plugin directory, inside the kit's own checkout, so the run loaded the kit's config).
	dir, out := "", os.DevNull
	if l.Cfg != nil {
		dir, out = l.Cfg.Root, filepath.Join(project.StateDir(l.Cfg.Root), "restart-"+t.agent+".log")
	}
	spawn := l.Spawn
	if spawn == nil {
		spawn = func(dir string, args []string, out string) error { return spawnDetached(dir, args, out, what) }
	}
	if err := spawn(dir, args, out); err != nil {
		return "", project.Failf("cannot schedule the %s of %s: %v", what, t.agent, err)
	}
	return fmt.Sprintf("SCHEDULED %s of %s (pane %s): runs once it is idle", what, t.agent, t.pane), nil
}

// spawnDetached starts this binary with args in a new session in dir ("": this cwd), output appended to out, not
// waited for. op is the operation the run carries (restart or stop), recorded in ScheduledEnv for the give-up text.
func spawnDetached(dir string, args []string, out, op string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if out != os.DevNull {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "%s obor %s\n", time.Now().UTC().Format(time.RFC3339), strings.Join(args, " "))
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), ScheduledEnv+"="+op)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// waitScheduled: in the detached run (ScheduledEnv) of a restart or stop of the caller's own pane, do all the waiting
// of that run and say so (waited). An agent in the pane: until it is idle (whenIdle). No agent: until the caller
// (`obor restart` at this pane's shell) has given the shell the foreground back, ≤ the exit wait, else
// give up naming the program that kept it. The caller then runs its normal path from the top on fresh state, with no
// further wait (stopped job, identity and session are checked after every wait), skipping the
// schedule. The pane is resolved here only to learn which one to watch. Not the detached run, or not the caller's
// pane: nothing to wait for.
func (l *Launcher) waitScheduled(ctx context.Context, agent, pane string) (waited bool, err error) {
	op := l.Env.Get(ScheduledEnv)
	if op == "" {
		return false, nil
	}
	word := "restart"
	if op == "stop" {
		word = "stop"
	}
	at := pane
	if at == "" { // the scheduled run names its pane; else the labelled one, as the run's Locate finds it
		at, _ = l.PaneOf(ctx, agent, "")
	}
	t, err := l.resolve(ctx, agent, pane, at, "")
	if err != nil {
		return false, err
	}
	if !l.ownPane(t) {
		return false, nil
	}
	end, err := l.begin(ctx, t.agent, t.info.Lane)
	if err != nil {
		return false, err
	}
	defer end()
	if err := l.whenIdle(ctx, t, word); err != nil {
		return true, err
	}
	if a, err := l.H.Pane(ctx, t.pane); err != nil || a.Agent != "" { // an agent, or a pane that cannot be read
		return true, nil
	}
	if l.atShell(ctx, t.pane) {
		return true, nil
	}
	d := l.wait()
	l.say("waiting for the shell of pane %s to have the foreground (≤ %s)", t.pane, d)
	if l.until(ctx, d, func() bool { return l.atShell(ctx, t.pane) }) {
		return true, nil
	}
	prog := "unknown"
	if pi, err := l.H.ProcessInfo(ctx, t.pane); err == nil && pi.Foreground() != "" {
		prog = pi.Foreground()
	}
	return true, l.giveUp(ctx, t, "%s still has the foreground of pane %s after %s: scheduled %s of %s not run",
		prog, t.pane, d, word, t.agent)
}

// sameAgent (after the scheduled wait): confirms the terminal still answers for t's pane before anything is stopped.
// The pane's label is its agent, and resolve refused another agent's label just before; a pane that cannot be read
// (identity unknown) is a FAIL. No retry.
func (l *Launcher) sameAgent(ctx context.Context, t *target) error {
	if _, err := l.H.Pane(ctx, t.pane); err != nil {
		return l.giveUp(ctx, t, "cannot confirm pane %s runs %s: %v: not stopped", t.pane, t.agent, err)
	}
	return nil
}

// whenIdle (the scheduled run) waits until t's agent is not working or blocked, ≤ Sched. A pane with no agent, or one
// the terminal no longer has, is nothing to wait for; any other error is an unknown status: keep looking. word ("restart" or
// "stop") names the operation in the give-up text.
func (l *Launcher) whenIdle(ctx context.Context, t *target, word string) error {
	d := l.Sched
	if d <= 0 {
		d = 10 * time.Minute
	}
	l.say("waiting for %s to go idle (≤ %s)", t.agent, d)
	status := t.entry.Status
	var last error
	if l.until(ctx, d, func() bool {
		a, err := l.H.Pane(ctx, t.pane)
		switch {
		case port.NotFound(err):
			return true
		case err != nil:
			last = err
			return false
		case a.Agent == "":
			return true
		}
		status, last = a.Status, nil
		return !a.Busy()
	}) {
		return nil
	}
	if last != nil {
		return l.giveUp(ctx, t, "%s status unknown after %s (last error: %v): scheduled %s not run (pane %s)",
			t.agent, d, last, word, t.pane)
	}
	return l.giveUp(ctx, t, "%s still %s after %s: scheduled %s not run (pane %s)", t.agent, status, d, word, t.pane)
}
