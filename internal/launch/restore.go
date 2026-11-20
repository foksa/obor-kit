package launch

// The restore after the terminal replaced panes' programs: obor serve, once a connect shows panes that lost their
// program, restarts those of its project (port.Restorer, port.Lost names them). It acts only on panes whose label is an agent of the obor
// project they work in (R-28 §2: obor setup and obor restart label the pane): the main checkout of the pane cwd's
// repository, when its config knows the label. Anything else (no label, no git, no .obor/config.json, a label the
// project has no agent for, a drive agent's (obor drive <T-xx> starts it again), another project's pane) is silent.
//
// After herdr restores a session, or hc runs a fresh shell in a kept terminal: each lost labelled obor pane is restored,
// one at a time (a project's under its restore lock, so a reconnect during a run restarts a pane at most once), each
// restart bounded by OBOR_RESTORE_TIMEOUT (s, default 120). A lost pane comes back as a shell (herdr with [session]
// resume_agents_on_restore = false: every pane is lost; hc) or runs the agent herdr resumed on its own (resume on:
// a bare resume with none of obor's start, no MCP config, settings or OBOR_* env). An agent that runs in a lost pane
// is obor's only when its start record (StartedFile) names the pane and the pid leading its foreground group now: obor
// started it since the loss, and it is left as it is. Any other is the terminal's: the restore waits for it to settle
// (herdr starts its resumed agents one after another; a pane still starting runs an agent too), then treats the pane
// as it treats a shell, and the log line says which way. A pane that would be restarted is: obor restart stops the
// terminal's agent and starts obor's. A pane that would be skipped (a free or dirty-free lane's worker: obor dispatch
// starts it; a role agent that is neither the lead nor a worker while its OpenWork read says serve's state has no
// work open for it: its next brief or job starts it) has the terminal's agent stopped and its shell left. A pane with
// no recorded session (serve's store) or a kind that resumes nothing starts --fresh (a role pane started fresh with no
// prompt wrote no transcript either, so no restart could resume it). A lane's worker and the integrator start with
// the wake of the job serve holds open for them (Launcher.Job: the restart's own) as their first prompt: typed once a
// resumed session is up, the first prompt of a fresh one; no open job, no prompt. One rule for both reads (OpenWork,
// Launcher.Job): an error restores the pane (a real job is never left stopped), with no wake. Log: ~/.obor/restore.log (last 200).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/store"
)

// restoreKeep is how many lines restore.log keeps.
const restoreKeep = 200

// panesTimeout bounds one pane list, and one pane's read.
const panesTimeout = 5 * time.Second

// Restorer is serve's restore's world: the environment, the panes and the clock.
type Restorer struct {
	Env project.Env
	H   port.PaneControl // only its Panes
	Now func() time.Time // nil: time.Now
	// Restart is `obor restart` in the loaded project: the result line, or the error (project.Fail) it failed with. Its
	// launcher carries the open job's wake (Launcher.Job); an error of that read restores the pane with no wake (the
	// same rule as OpenWork's), where obor restart refuses.
	Restart func(ctx context.Context, cfg *config.Config, req Request, out io.Writer) (string, error)
	// LaneState is the lane's state as the lane table says (free, dirty-free, working, …).
	LaneState func(cfg *config.Config, l config.Lane) string
	// OpenWork is whether a role agent (any but the lead and a worker) has work open in serve's state: a brief not
	// finished, the integrator's open job, the reviewer's open review or verify. An error restores the pane (a real
	// job is never left stopped). Nil: every role pane is restored.
	OpenWork func(agent, role string) (bool, error)
	// Stop is `obor restart --stop` in the loaded project (launch's Stop): it quits the agent the terminal resumed in
	// pane, leaving its shell (a pane the restore skips); the result line, or the error it failed with. Nil: that
	// agent is left running, said in the log.
	Stop func(ctx context.Context, cfg *config.Config, agent, pane string, out io.Writer) (string, error)
	// Settle bounds one run's wait for the agents the terminal resumed to settle (0: settleBound); Poll is how often
	// it looks (0: settlePoll).
	Settle, Poll time.Duration
}

// settleBound is how long one restore run waits, at most, for the agents the terminal resumed to settle: herdr starts
// them startup_per_agent_delay_ms apart (default 100 ms) and an agent takes seconds to come up (Claude ~4 s).
const settleBound = 60 * time.Second

// settlePoll is how often the settle wait looks at a pane.
const settlePoll = 500 * time.Millisecond

func (p *Restorer) stamp() string {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return now().Format("2006-01-02 15:04:05")
}

// paneConfig is the loaded config of the project at root, when cwd is in that project: the main checkout of cwd's
// repository is root (the cwd is no fallback; ok false otherwise, and no config is read for another project's pane).
// What the pane's label is made of is the caller's: an agent's (config.AgentLookup).
func paneConfig(root, cwd string) (cfg *config.Config, ok bool) {
	if cwd == "" {
		return nil, false
	}
	repo, err := project.Find(cwd)
	if err != nil || filepath.Clean(repo.Root) != root {
		return nil, false
	}
	if project.Stopped(root) { // obor stop: nothing to restore
		return nil, false
	}
	if cfg, err = config.Load(root); err != nil {
		return nil, false
	}
	return cfg, true
}

// RestoreLog is ${OBOR_STATE_HOME:-$HOME/.obor}/restore.log.
func RestoreLog(env project.Env) string {
	return filepath.Join(project.StateHome(env), "restore.log")
}

// RestoreProject restores the labelled panes of the obor project at root that lost lists (serve's run after a connect
// showed them lost), each at its shell or running an agent the terminal started on its own: those with work to come
// back to are restarted (the lead always, a worker unless its lane is free, every other role while its OpenWork read
// says work is open), the terminal's agent in the others stopped. A pane running an agent obor started since (its
// start record) and a pane lost does not list are left as they are (a lead stopped on purpose stays at its shell);
// another project's panes are its serve's.
func (p *Restorer) RestoreProject(ctx context.Context, root string, lost port.Lost) {
	root = filepath.Clean(root)
	panes, ok := p.panes(ctx)
	if !ok {
		return
	}
	found := false
	for _, pane := range panes {
		if !lost.Has(pane.ID) {
			continue
		}
		if _, ok := oborPane(root, pane); ok {
			found = true
			break
		}
	}
	if !found {
		return
	}
	limit := 120 * time.Second
	if n, err := strconv.Atoi(p.Env.Get("OBOR_RESTORE_TIMEOUT")); err == nil && n > 0 {
		limit = time.Duration(n) * time.Second
	}
	p.restoreRoot(ctx, root, lost, limit)
}

var _ port.Restorer = (*Restorer)(nil)

// RestoreLock is a project's restore lock, <state>/restore.lock: held while one run restores the project's panes, so
// a reconnect's run that starts meanwhile waits, then reads the panes again and finds each restarted pane running the
// agent its start record names (left as it is): no pane is restarted twice.
func RestoreLock(root string) *project.Lock {
	return project.NewLock(filepath.Join(project.StateDir(root), "restore.lock"))
}

// restoreRoot restores the project at root's panes that lost lists under its restore lock, from a pane list read once
// the lock is held: a pane another run restarted meanwhile runs the agent its start record names now.
func (p *Restorer) restoreRoot(ctx context.Context, root string, lost port.Lost, limit time.Duration) {
	log := RestoreLog(p.Env)
	lock := RestoreLock(root)
	lock.Output = io.Discard
	ok, err := lock.Try()
	if err == nil && !ok {
		err = lock.Wait(ctx, -1)
	}
	if err != nil {
		appendLog(log, func(w io.Writer) {
			fmt.Fprintf(w, "%s restore of %s: FAIL cannot take its restore lock: %v\n", p.stamp(), root, err)
		})
		return
	}
	defer lock.Unlock()
	panes, ok := p.panes(ctx)
	if !ok {
		return
	}
	settle := settleBound
	if p.Settle > 0 {
		settle = p.Settle
	}
	settleBy := time.Now().Add(settle) // one bound for the run: herdr starts its resumed agents one after another
	for _, pane := range panes {
		if !lost.Has(pane.ID) {
			continue
		}
		cfg, ok := oborPane(root, pane)
		if !ok {
			continue
		}
		appendLog(log, func(w io.Writer) {
			p.restoreOne(ctx, cfg, pane, limit, settleBy, w)
		})
	}
}

// panes is herdr's pane list, bounded by panesTimeout; false when it cannot be read.
func (p *Restorer) panes(ctx context.Context) ([]port.Pane, bool) {
	hctx, cancel := context.WithTimeout(ctx, panesTimeout)
	defer cancel()
	panes, err := p.H.Panes(hctx)
	return panes, err == nil
}

// oborPane is the config of the project at root when pane is an agent pane of it (paneConfig): false for an unlabelled
// pane, one outside root's project (its config is never read), a label the project has no agent for, or a drive
// agent's (the owner's: obor drive <T-xx> starts it again).
func oborPane(root string, pane port.Pane) (*config.Config, bool) {
	if pane.Label == "" {
		return nil, false
	}
	cfg, ok := paneConfig(root, pane.Cwd)
	if !ok {
		return nil, false
	}
	if info, ok := cfg.AgentLookup(pane.Label, ""); !ok || info.Role == roles.Driver {
		return nil, false
	}
	return cfg, true
}

// restoreOne restores the lost pane of cfg's project: a shell, or an agent the terminal started on its own (resumed),
// which obor replaces or stops; an agent obor started there since the loss (its start record) is left as it is.
func (p *Restorer) restoreOne(ctx context.Context, cfg *config.Config, pane port.Pane, limit time.Duration,
	settleBy time.Time, w io.Writer) {
	agent := pane.Label
	resumed := "" // the kind of the agent the terminal started in the pane on its own ("": the pane is a shell)
	if pane.Agent != "" {
		if p.oborStarted(ctx, cfg.Root, agent, pane.ID) {
			fmt.Fprintf(w, "%s skip %s (pane %s): runs %s (obor started it since)\n", p.stamp(), agent, pane.ID, pane.Agent)
			return
		}
		kind := pane.Agent
		var settled bool
		if pane, settled = p.settle(ctx, pane, settleBy); !settled {
			fmt.Fprintf(w, "%s %s (pane %s): the terminal's %s has not settled by the restore's bound: taken as it is\n",
				p.stamp(), agent, pane.ID, kind)
		}
		resumed = pane.Agent
	}
	now := p.stamp()
	info, _ := cfg.AgentLookup(agent, "")
	if lane := p.freeLane(cfg, info); lane != "" {
		p.skip(ctx, cfg, pane, resumed, "lane "+lane+" is free (dispatch starts its worker)", w)
		return
	}
	if !alwaysRestored(info.Role) && !p.openWork(info.Role, agent) {
		p.skip(ctx, cfg, pane, resumed, "no open work (its next brief or job starts it)", w)
		return
	}
	// A pane with no session it can resume starts fresh: none in serve's store, or a kind
	// that resumes nothing (OpenCode). Every dispatch starts a fresh worker anyway; a role pane started fresh
	// with no prompt has no transcript either (Claude writes none before its first message), so no restart could
	// resume it.
	req := Request{Agent: agent, Pane: pane.ID}
	note := ""
	if resumed != "" {
		note = " (the terminal resumed " + resumed + " without obor's start: obor restart replaces it)"
	}
	role := info.Role
	if role == "" {
		role = "agent"
	}
	s, _ := store.CurrentAt(cfg.Root, agent)
	switch kind := project.RunningKind(cfg, info); {
	case !agents.Resumes(kind):
		req.Fresh, note = true, note+" ("+role+", kind "+kind+" resumes nothing: --fresh)"
	case s.ID == "":
		req.Fresh, note = true, note+" ("+role+", no recorded session: --fresh)"
	}
	fmt.Fprintf(w, "%s restore %s (pane %s) in %s%s\n", now, agent, pane.ID, cfg.Root, note)
	rctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	line, err := p.Restart(rctx, cfg, req, w)
	if ev := Evidence(err); ev != "" {
		fmt.Fprintln(w, ev)
	}
	if err != nil {
		fmt.Fprintf(w, "obor restart %s: FAIL %v\n", agent, err)
		return
	}
	fmt.Fprintln(w, line)
}

// skip leaves the lost pane's agent unrestarted for why: a shell stays a shell; an agent the terminal resumed
// (resumed: its kind) is stopped, its shell left.
func (p *Restorer) skip(ctx context.Context, cfg *config.Config, pane port.Pane, resumed, why string, w io.Writer) {
	now, agent := p.stamp(), pane.Label
	if resumed == "" {
		fmt.Fprintf(w, "%s skip %s (pane %s): %s\n", now, agent, pane.ID, why)
		return
	}
	if p.Stop == nil {
		fmt.Fprintf(w, "%s skip %s (pane %s): %s; the terminal resumed %s without obor's start: left running\n", now,
			agent, pane.ID, why, resumed)
		return
	}
	fmt.Fprintf(w, "%s stop %s (pane %s): %s; the terminal resumed %s without obor's start: stopped, its shell left\n",
		now, agent, pane.ID, why, resumed)
	sctx, cancel := context.WithTimeout(ctx, StopBound+panesTimeout)
	defer cancel()
	line, err := p.Stop(sctx, cfg, agent, pane.ID, w)
	if ev := Evidence(err); ev != "" {
		fmt.Fprintln(w, ev)
	}
	if err != nil {
		fmt.Fprintf(w, "obor restart --stop %s: FAIL %v\n", agent, err)
		return
	}
	fmt.Fprintln(w, line)
}

// oborStarted: the agent leading pane's foreground group now is the one agent's start record names there (obor
// started it since the pane was lost). No record, another pane, a pane at its shell or another pid: false.
func (p *Restorer) oborStarted(ctx context.Context, root, agent, pane string) bool {
	rp, pid, ok := startRecord(root, agent)
	if !ok || rp != pane {
		return false
	}
	hctx, cancel := context.WithTimeout(ctx, panesTimeout)
	defer cancel()
	pi, err := p.H.ProcessInfo(hctx, pane)
	return err == nil && !pi.AtShell() && pi.FgPGID == pid
}

// settle waits (until by) for the agent the terminal started in pane on its own to settle: two looks in a row (Poll
// apart) find the same process leading the pane's foreground group and the terminal reporting a status for it. herdr
// names a resumed agent before its process runs, still reports its old status for a moment after the process starts,
// then reports it unknown while it starts: a pane still starting runs an agent. It returns the pane as last read, and
// whether it settled: an agent that runs, or none at all (the terminal names none: the pane is a shell). Past by, a
// pane whose shell has the foreground is a shell (its Agent ""), else the agent as it is. A read that fails ends the
// wait with the pane as it was.
func (p *Restorer) settle(ctx context.Context, pane port.Pane, by time.Time) (port.Pane, bool) {
	poll := settlePoll
	if p.Poll > 0 {
		poll = p.Poll
	}
	seen := 0 // the pid the last look found settled (0: none)
	for {
		hctx, cancel := context.WithTimeout(ctx, panesTimeout)
		pi, err := p.H.ProcessInfo(hctx, pane.ID)
		var now port.Pane
		if err == nil {
			now, err = p.H.Pane(hctx, pane.ID)
		}
		cancel()
		if err != nil {
			return pane, false
		}
		now.Label, now.Cwd = pane.Label, pane.Cwd
		atShell := pi.FgPGID == 0 || pi.AtShell()
		up := !atShell && now.Agent != "" && now.Status != "" && now.Status != "unknown"
		switch {
		case now.Agent == "":
			return now, true
		case up && seen == pi.FgPGID:
			return now, true
		case !time.Now().Before(by):
			if atShell {
				now.Agent = ""
			}
			return now, false
		}
		seen = 0
		if up {
			seen = pi.FgPGID
		}
		select {
		case <-ctx.Done():
			return pane, false
		case <-time.After(poll):
		}
	}
}

// alwaysRestored: a pane's role is restored whatever serve's state says: the lead's always, a worker's by its lane.
func alwaysRestored(role string) bool {
	return role == "lead" || role == "worker"
}

// openWork is the OpenWork read for role: true when work is open or the read fails (a real job is never left
// stopped); without the callback, true.
func (p *Restorer) openWork(role, agent string) bool {
	if p.OpenWork == nil {
		return true
	}
	open, err := p.OpenWork(agent, role)
	return err != nil || open
}

// freeLane is the lane of a worker whose lane is free or dirty-free (no task in flight), "" otherwise. Role panes
// (reviewer, deep-dive) are no lanes, so never skipped. A free worker lane
// is always skipped (obor dispatch starts its worker).
func (p *Restorer) freeLane(cfg *config.Config, info config.AgentInfo) string {
	if info.Role != "worker" {
		return ""
	}
	for _, l := range cfg.Lanes() {
		if l.Name != info.Lane {
			continue
		}
		switch p.LaneState(cfg, l) {
		case port.LaneFree, port.LaneDirtyFree:
			return l.Name
		}
	}
	return ""
}

// appendLog runs write against the log file (created with its directory), then cuts the log to its last keep lines.
// A log that cannot be opened: nothing runs (the restore has nowhere to say anything).
func appendLog(path string, write func(w io.Writer)) {
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	write(f)
	f.Close()
	trim(path, restoreKeep)
}

// trim keeps the last n lines of the file (rewritten by rename), when it has more.
func trim(path string, n int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	f.Close()
	if sc.Err() != nil || len(lines) <= n {
		return
	}
	tmp := path + ".tmp." + strconv.Itoa(os.Getpid())
	if os.WriteFile(tmp, []byte(strings.Join(lines[len(lines)-n:], "\n")+"\n"), 0o644) == nil {
		os.Rename(tmp, path)
	}
}
