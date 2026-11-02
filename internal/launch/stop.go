package launch

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

// Stop quits agent in its pane (pane "": the pane labelled agent) and leaves the pane labelled, at its shell:
// idle/done → the kind's quit (agents.Kind.Stop), the exit dialog closed, wait for the shell; a kind with no quit is terminated. Working/blocked, or an agent whose
// quit opens its kind's dialog (background tasks) → FAIL unless force; force → Escape, wait (≤ Idle, 30 s) for it to go idle, then the same exit
// (the kind's Stop.Dialog option when the dialog asks); an agent that ignores the exit is terminated (terminate). No session
// check, no start. Returns the STOPPED line.
func (l *Launcher) Stop(ctx context.Context, agent, pane string, force bool) (string, error) {
	if !config.AgentNameRE.MatchString(agent) {
		return "", project.Failf("bad agent name %q", agent)
	}
	waited, err := l.waitScheduled(ctx, agent, pane)
	if err != nil {
		return "", err
	}
	t, err := l.resolve(ctx, agent, pane, "")
	if err != nil {
		return "", err
	}
	if waited {
		if err := l.sameAgent(ctx, t); err != nil {
			return "", err
		}
	}
	end, err := l.begin(ctx, t.agent, t.info.Lane)
	if err != nil {
		return "", err
	}
	defer end()
	if l.ownPane(t) && !waited {
		return l.schedule(t, Request{Force: force}, true)
	}
	running := t.entry.Kind != "" && !l.atShell(ctx, t.pane)
	if err := l.stop(ctx, t, force, "left running; --force stops it"); err != nil {
		return "", err
	}
	if !running {
		return fmt.Sprintf("STOPPED %s (pane %s, already at a shell)", t.agent, t.pane), nil
	}
	return fmt.Sprintf("STOPPED %s (pane %s)", t.agent, t.pane), nil
}

// stop quits the agent running in t's pane, if any (hint: what a refusal of a working agent says to do).
func (l *Launcher) stop(ctx context.Context, t *target, force bool, hint string) error {
	running := t.entry.Kind
	if running == "" || l.atShell(ctx, t.pane) {
		return nil
	}
	recipe := recipeOf(running)
	restored := false // AfterEscape ran: the interrupted prompt is already cleared
	if status := t.entry.Status; status == "working" || status == "blocked" {
		if !force {
			return project.Failf("%s is %s (%s)", t.agent, status, hint)
		}
		_ = l.H.SendKeys(ctx, t.pane, "esc")
		idle := l.Idle
		if idle <= 0 {
			idle = 30 * time.Second
		}
		if !l.until(ctx, idle, func() bool {
			a, err := l.H.Agent(ctx, t.pane)
			return err != nil || (a.Status != "working" && a.Status != "blocked")
		}) {
			l.say("warning: %s still %s %s after Escape: exiting anyway", t.agent, status, idle)
			l.logEvidence(t, fmt.Sprintf("%s still %s %s after Escape", t.agent, status, idle), l.evidence(ctx, t))
		}
		for _, k := range recipe.AfterEscape { // the kind's interrupted prompt, put back into the input, cleared
			_ = l.H.SendKeys(ctx, t.pane, k)
			restored = true
		}
	}
	// The agent leads the pane's foreground group (T-217): its exit is that pid gone, even when the MCP servers it
	// started stay in the group and keep the shell from getting the foreground back.
	var before group
	if g, err := l.group(ctx, t.pane); err == nil && g.shell != 0 && !g.atShell() && g.pgid > 1 {
		before = g
	}
	// Background tasks: the kind's exit dialog decides (Claude's Bash tool shells lead their own sessions, outside the
	// group process info lists; live 2026-10-17).
	bgFail := project.Failf("%s has background tasks (finish them, or --force to stop them)", t.agent)
	quit := recipe.Quit
	if quit == "" {
		// No clean quit to wait for: the proven SIGTERM/SIGKILL path at once (T-292).
		if err := l.terminate(ctx, t, before, running, "has no clean quit"); err != nil {
			return err
		}
		return l.finish(ctx, t, before, running)
	}
	if !restored {
		// A person may have typed into the input: the quit typed after it would be sent as part of that text (T-712).
		for _, k := range recipe.ClearInput {
			_ = l.H.SendKeys(ctx, t.pane, k)
		}
	}
	if err := l.H.Type(ctx, t.pane, quit); err != nil {
		return project.Failf("herdr pane run %s %s failed", t.pane, quit)
	}
	chose, polls := false, 0
	var dialogErr error
	exited := l.until(ctx, l.wait(), func() bool {
		if l.exited(ctx, t.pane, before.pgid) {
			return true
		}
		n, open := l.exitDialog(ctx, t.pane, recipe.Dialog)
		// The second Enter submits a quit a completion menu took the first Enter for (T-712); never into the kind's exit
		// dialog, where a bare Enter would pick its default option.
		if polls++; polls == 2 && recipe.SecondEnter && !open {
			_ = l.H.SendKeys(ctx, t.pane, "enter")
		}
		if !open {
			return false
		}
		// Never leave the dialog open: force picks the dialog's option by its key, else Escape closes it.
		if !force || n == "" {
			_ = l.H.SendKeys(ctx, t.pane, "esc")
			dialogErr = bgFail
			if force {
				dialogErr = project.Failf("%s: exit dialog without a numbered %q option (closed it)", t.agent, recipe.Dialog.Option)
			}
			return true
		}
		if !chose {
			_ = l.H.SendKeys(ctx, t.pane, n)
			l.say("exit dialog: chose %s. %s (--force)", n, recipe.Dialog.Option)
			chose = true
		}
		return false
	})
	if dialogErr != nil {
		return dialogErr
	}
	if !exited {
		_, open := l.exitDialog(ctx, t.pane, recipe.Dialog)
		if open {
			_ = l.H.SendKeys(ctx, t.pane, "esc")
		}
		if !open && recipe.SecondEnter && polls < 2 {
			// The wait ended before the second poll: send the quit's second Enter anyway, so a popup that took the
			// first one never leaves a stop that "did not exit" without it (a wait shorter than two polls).
			_ = l.H.SendKeys(ctx, t.pane, "enter")
		}
		if !force {
			cleared := ""
			if keys := recipe.ClearInput; len(keys) > 0 {
				for _, k := range keys {
					_ = l.H.SendKeys(ctx, t.pane, k)
				}
				cleared = "; the input was cleared"
			}
			return l.giveUp(ctx, t, "%s did not exit in %s (pane %s%s; --force terminates it)", t.agent, l.wait(), t.pane, cleared)
		}
		if err := l.terminate(ctx, t, before, running, fmt.Sprintf("did not exit in %s", l.wait())); err != nil {
			return err
		}
	}
	return l.finish(ctx, t, before, running)
}

// recipeOf is kind's quit recipe (agents.Kind.Stop); none (no quit: terminated, proven, without the wait) for a kind
// herdr runs that obor has no bridge for.
func recipeOf(kind string) agents.Stop {
	if k, ok := agents.Lookup(kind); ok {
		return k.Stop()
	}
	return agents.Stop{}
}

// leads: leader (herdr's canonical executable) is the program an agent of kind leads its process group as: the
// kind's Stop.Program or its Name() (a kind with no bridge: its name).
func leads(kind, leader string) bool {
	r := recipeOf(kind)
	return leader != "" && (leader == kind || leader == r.Program)
}

// programs names what leads accepts for kind, for a refusal: the kind, or the kind and its Stop.Program when one is set.
func programs(kind string) string {
	if r := recipeOf(kind); r.Program != "" && r.Program != kind {
		return r.Program + " or " + kind
	}
	return kind
}

// finish ends the exited agent's leftovers, if they keep the foreground.
func (l *Launcher) finish(ctx context.Context, t *target, before group, running string) error {
	if !l.atShell(ctx, t.pane) {
		if err := l.stopLeftovers(ctx, t, before); err != nil {
			return err
		}
	}
	l.say("stopped %s in pane %s", running, t.pane)
	return nil
}

// group is a snapshot of a pane's foreground process group (herdr process-info): the pane's shell, the group in the
// foreground (the shell's own when it has it), its leader's program (pid pgid; "" when not listed) and its members.
type group struct {
	shell, pgid int
	leader      string
	members     []port.Process
}

func (l *Launcher) group(ctx context.Context, pane string) (group, error) {
	pi, err := l.H.ProcessInfo(ctx, pane)
	if err != nil {
		return group{}, err
	}
	g := group{shell: pi.ShellPID, pgid: pi.FgPGID, members: pi.Processes}
	for _, pr := range pi.Processes {
		if pr.PID == pi.FgPGID {
			g.leader = pr.Program()
		}
	}
	return g, nil
}

func (g group) atShell() bool { return g.shell != 0 && g.pgid == g.shell }

func (g group) listed(pid int) bool {
	for _, pr := range g.members {
		if pr.PID == pid {
			return true
		}
	}
	return false
}

func (l *Launcher) alive(pid int) bool {
	if l.Alive != nil {
		return l.Alive(pid)
	}
	return project.PIDAlive(pid)
}

// exited: the agent that led group pgid (0: none recorded) has left the pane's foreground: the shell has it back, or
// the leader is gone: not alive, or no longer listed in its group, still in the foreground (an empty list proves
// nothing: herdr did not read the group). herdr gives no process info: herdr sees no agent in the pane, or the pid is
// not alive. A zombie leader is alive and listed until the pane's shell reaps it, at once: it runs until then.
func (l *Launcher) exited(ctx context.Context, pane string, pgid int) bool {
	now, err := l.group(ctx, pane)
	if err != nil || now.shell == 0 {
		_, aerr := l.H.Agent(ctx, pane)
		return aerr != nil || (pgid > 1 && !l.alive(pgid))
	}
	unlisted := now.pgid == pgid && len(now.members) > 0 && !now.listed(pgid)
	return now.atShell() || (pgid > 1 && (unlisted || !l.alive(pgid)))
}

// same: why ("" when proven) group now is not before's to signal. before was recorded before the exit (pgid > 1, not
// the shell's); now has the same shell and the same group in the foreground; leaderGone: its leader is no longer listed
// (only the agent's leftovers are), else it is still listed, alive, as before's leader program, and that program is
// one an agent of kind leads as (leads: herdr's kind for the pane and a matching leader are the evidence, not the
// configured kind). With exited, the only authority to signal a group.
func (l *Launcher) same(before, now group, leaderGone bool, kind string) string {
	switch {
	case before.pgid <= 1 || before.shell == 0 || before.pgid == before.shell:
		return "no agent process group recorded before the exit"
	case now.shell != before.shell:
		return fmt.Sprintf("the pane's shell is pid %d, not %d", now.shell, before.shell)
	case now.pgid != before.pgid:
		return fmt.Sprintf("the foreground is group %d, not %d", now.pgid, before.pgid)
	case leaderGone && now.listed(before.pgid):
		return fmt.Sprintf("the agent (pid %d) is still in the group", before.pgid)
	case leaderGone:
		return ""
	case !leads(kind, now.leader):
		return fmt.Sprintf("the group's leader (pid %d) is %q, not %s", before.pgid, now.leader, programs(kind))
	case now.leader != before.leader || !now.listed(before.pgid):
		return fmt.Sprintf("the group's leader (pid %d) is %q, not %s", before.pgid, now.leader, before.leader)
	case !l.alive(before.pgid):
		return fmt.Sprintf("pid %d is gone", before.pgid)
	}
	return ""
}

// ladder ends process group pgid, never without proof: proven, SIGTERM, ≤ 3 s for it to be done; then, proven again,
// SIGKILL once, ≤ 2 s more. proof reads the pane now: done (nothing left to end), else why ("" = proven) the group is
// not the one to signal. sent: the last signal sent ("": none); why: the proof that stopped the next one; ok: done.
func (l *Launcher) ladder(ctx context.Context, pgid int, proof func() (done bool, why string)) (sent, why string, ok bool) {
	done := func() bool { d, _ := proof(); return d }
	if d, why := proof(); d || why != "" {
		return "", why, d
	}
	_ = l.kill(pgid, syscall.SIGTERM)
	if l.until(ctx, 3*time.Second, done) {
		return "SIGTERM", "", true
	}
	if d, why := proof(); d || why != "" {
		return "SIGTERM", why, d
	}
	_ = l.kill(pgid, syscall.SIGKILL)
	return "SIGKILL", "", l.until(ctx, 2*time.Second, done)
}

// terminate ends an agent of kind running (herdr's kind for the pane) that ignored the quit after the wait (force,
// T-249; every kind, opencode too) or has none (T-292; why: which, e.g. "did not exit in 20s"): the ladder on the group
// recorded before the quit (before), each signal proven by same: still led by the agent as at the start, as running's
// program. nil: the agent is gone (its leftovers are the caller's).
func (l *Launcher) terminate(ctx context.Context, t *target, before group, running, why string) error {
	sent, not, ok := l.ladder(ctx, before.pgid, func() (bool, string) {
		if l.exited(ctx, t.pane, before.pgid) {
			return true, ""
		}
		now, err := l.group(ctx, t.pane)
		if err != nil {
			return false, fmt.Sprintf("process info unavailable: %v", err)
		}
		return false, l.same(before, now, false, running)
	})
	switch {
	case ok && sent != "":
		l.say("terminated %s (%s pid %d, %s): it %s", t.agent, before.leader, before.pgid, sent, why)
		return nil
	case ok:
		return nil
	case sent == "":
		return l.giveUp(ctx, t, "%s %s (pane %s; not terminated: %s)", t.agent, why, t.pane, not)
	case sent == "SIGTERM":
		return l.giveUp(ctx, t, "%s %s, nor on SIGTERM (pane %s; no SIGKILL: %s)", t.agent, why, t.pane, not)
	}
	return l.giveUp(ctx, t, "%s %s, nor on SIGTERM and SIGKILL (pane %s)", t.agent, why, t.pane)
}

// stopLeftovers ends what is left in the exited agent's group (its MCP servers) while that group still holds the
// pane's foreground: the ladder, proven to be only the leftovers (same, leader gone). The leftovers race the checks:
// they may exit by themselves at any time, and the shell takes the foreground back only once it has reaped them. The
// shell back in the foreground, or an empty group, is a clean pane: success, nothing (more) signalled.
func (l *Launcher) stopLeftovers(ctx context.Context, t *target, before group) error {
	var comms []string // the leftovers' programs, as first proven
	sent, why, ok := l.ladder(ctx, before.pgid, func() (bool, string) {
		now, err := l.group(ctx, t.pane)
		switch {
		case err != nil:
			return false, fmt.Sprintf("process info unavailable: %v", err)
		case now.atShell():
			return true, ""
		}
		if why := l.same(before, now, true, ""); why != "" {
			return false, why
		}
		if comms == nil {
			for _, pr := range now.members {
				comms = append(comms, pr.Program())
			}
		}
		return len(now.members) == 0, ""
	})
	left := strings.Join(comms, ", ")
	switch {
	case ok && sent != "":
		l.say("stopped %d leftover process(es) of %s (%s)", len(comms), t.agent, left)
		return nil
	case ok:
		return nil
	case sent == "":
		return l.giveUp(ctx, t, "%s exited, but the pane's shell did not get the foreground back (pane %s; not signalled: %s)",
			t.agent, t.pane, why)
	case sent == "SIGTERM":
		return l.giveUp(ctx, t, "%s exited, but its leftover process(es) (%s) kept the foreground after SIGTERM (pane %s; no SIGKILL: %s)",
			t.agent, left, t.pane, why)
	}
	return l.giveUp(ctx, t, "%s exited, but its leftover process(es) (%s) kept the foreground after SIGTERM and SIGKILL (pane %s)",
		t.agent, left, t.pane)
}

// atShell: the pane's shell has the foreground; herdr gives no process info → herdr sees no agent in the pane.
func (l *Launcher) atShell(ctx context.Context, pane string) bool {
	if pi, err := l.H.ProcessInfo(ctx, pane); err == nil && pi.ShellPID != 0 {
		return pi.AtShell()
	}
	_, err := l.H.Agent(ctx, pane)
	return err != nil
}

// exitDialog: the kind's quit dialog d (nil: the kind has none) is on the pane's screen; n is the key of its option
// ("" when the screen shows none).
func (l *Launcher) exitDialog(ctx context.Context, pane string, d *agents.Dialog) (n string, open bool) {
	if d == nil {
		return "", false
	}
	screen, err := l.H.Screen(ctx, pane, 30)
	if err != nil || !strings.Contains(screen, d.Option) {
		return "", false
	}
	if m := d.Key.FindStringSubmatch(screen); m != nil {
		return m[1], true
	}
	return "", true
}
