package launch

// The obor.events herdr plugin's restore (`obor plugin restore`; design "herdr integration"; T-470: package plugin
// moved here). herdr runs the manifest's command from the plugin directory; the command backgrounds the work at once
// (cmd/obor), so herdr never waits on it. It acts only on panes whose label is an agent of an obor project (R-28 §2:
// obor setup and obor restart label the pane): the main checkout of the pane cwd's repository, when its config knows
// the label. Anything else (no label, no git, no .obor/config.json, a label the project has no agent for) is silent.
//
// After herdr restores a session (with [session] resume_agents_on_restore = false obor panes come back as shells):
// each labelled obor pane that runs no agent is restarted with obor restart, one at a time, each bounded by
// OBOR_RESTORE_TIMEOUT (s, default 120). Skipped, with a log line: a pane that runs an agent, and the worker of a free
// (or dirty-free) lane (obor dispatch starts it, T-169). A pane with no recorded
// session (serve's store, T-583) or a kind that resumes nothing starts --fresh (RV-18 #3, T-627: a role pane started
// fresh with no prompt wrote no transcript either, so no restart could resume it). Log: ~/.obor/restore.log
// (last 200).

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
	"obor/internal/store"
)

// restoreKeep is how many lines restore.log keeps.
const restoreKeep = 200

// panesTimeout bounds the hook's one pane list.
const panesTimeout = 5 * time.Second

// Restorer is the restore hook's world: the environment, the panes and the clock.
type Restorer struct {
	Env project.Env
	H   port.PaneControl // only its Panes
	Now func() time.Time // nil: time.Now
	// Restart is `obor restart` in the loaded project: the result line, or the error (project.Fail) it failed with.
	Restart func(ctx context.Context, cfg *config.Config, req Request, out io.Writer) (string, error)
	// LaneState is the lane's state as the lane table says (free, dirty-free, working, …).
	LaneState func(cfg *config.Config, l config.Lane) string
}

func (p *Restorer) stamp() string {
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	return now().Format("2006-01-02 15:04:05")
}

// paneConfig is the loaded config of the obor project a pane with cwd belongs to: the main checkout of cwd's
// repository (the cwd is no fallback; ok false otherwise). What the pane's label is made of is the caller's: an
// agent's (project.AgentLookup).
func paneConfig(cwd string) (cfg *config.Config, ok bool) {
	if cwd == "" {
		return nil, false
	}
	repo, err := project.Find(cwd)
	if err != nil || project.Stopped(repo.Root) { // a stopped project (obor stop, T-709): nothing to restore
		return nil, false
	}
	if cfg, err = config.Load(repo.Root); err != nil {
		return nil, false
	}
	return cfg, true
}

// RestoreLog is ${OBOR_STATE_HOME:-$HOME/.obor}/restore.log.
func RestoreLog(env project.Env) string {
	return filepath.Join(project.StateHome(env), "restore.log")
}

// Restore restarts the labelled obor panes herdr's pane list shows without an agent, in its order.
func (p *Restorer) Restore(ctx context.Context) {
	hctx, cancel := context.WithTimeout(ctx, panesTimeout)
	panes, err := p.H.Panes(hctx)
	cancel()
	if err != nil {
		return
	}
	limit := 120 * time.Second
	if n, err := strconv.Atoi(p.Env.Get("OBOR_RESTORE_TIMEOUT")); err == nil && n > 0 {
		limit = time.Duration(n) * time.Second
	}
	log := RestoreLog(p.Env)
	for _, pane := range panes {
		if pane.Label == "" {
			continue
		}
		cfg, ok := paneConfig(pane.Cwd)
		if !ok {
			continue
		}
		if _, ok := project.AgentLookup(cfg, pane.Label, ""); !ok {
			continue
		}
		appendLog(log, func(w io.Writer) {
			p.restoreOne(ctx, cfg, pane, limit, w)
		})
	}
}

func (p *Restorer) restoreOne(ctx context.Context, cfg *config.Config, pane port.PaneInfo, limit time.Duration, w io.Writer) {
	now, agent := p.stamp(), pane.Label
	if pane.Kind != "" {
		fmt.Fprintf(w, "%s skip %s (pane %s): runs %s\n", now, agent, pane.ID, pane.Kind)
		return
	}
	info, _ := project.AgentLookup(cfg, agent, "")
	if lane := p.freeLane(cfg, info); lane != "" {
		fmt.Fprintf(w, "%s skip %s (pane %s): lane %s is free (dispatch starts its worker)\n", now, agent, pane.ID, lane)
		return
	}
	// A pane with no session it can resume starts fresh (RV-18 #3, T-583, T-627): none in serve's store, or a kind
	// that resumes nothing (OpenCode, T-581). Every dispatch starts a fresh worker anyway; a role pane started fresh
	// with no prompt has no transcript either (Claude writes none before its first message), so no restart could
	// resume it.
	req := Request{Agent: agent, Pane: pane.ID}
	note := ""
	role := info.Role
	if role == "" {
		role = "agent"
	}
	s, _ := store.CurrentAt(cfg.Root, agent)
	switch kind := agentKind(cfg, info); {
	case !agents.Resumes(kind):
		req.Fresh, note = true, " ("+role+", kind "+kind+" resumes nothing: --fresh)"
	case s.ID == "":
		req.Fresh, note = true, " ("+role+", no recorded session: --fresh)"
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

// freeLane is the lane of a worker whose lane is free or dirty-free (no task in flight), "" otherwise. Role panes
// (reviewer, deep-dive) are no lanes, so never skipped (T-179). T-864: stop_idle_workers is gone, so a free worker lane
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
		case "free", "dirty-free":
			return l.Name
		}
	}
	return ""
}

// agentKind is agent's kind as its restart starts it: the kind of the type it runs now (project.RunningType: a lane
// agent's, its open dispatch's, T-739), else agents.Default (a restore pane runs no agent, so herdr names no kind).
func agentKind(cfg *config.Config, info config.AgentInfo) string {
	if lt, ok := cfg.Model(project.RunningType(cfg, info)); ok && lt.Kind != "" {
		return lt.Kind
	}
	return agents.Default
}

// appendLog runs write against the log file (created with its directory), then cuts the log to its last keep lines.
// A log that cannot be opened: nothing runs (the hooks have nowhere to say anything).
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
