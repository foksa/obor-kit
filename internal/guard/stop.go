package guard

// The Stop, SessionStart and UserPromptSubmit hooks (`obor hook stop|session-start|user-prompt-submit`, run through
// bin/obor; T-470: package stop moved here): the worker's task_done, the session's obor context, the handoff reminder
// and the wrong-tab guard. The lead and the integrator stop when they like (T-465: no nudges, no ledger) — except for a
// background run they started, which blocks every agent's stop (T-1142, background).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/background"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// Stop is the Stop hook on the event JSON in input: a block verdict ({"decision":"block","reason":…}) on out, or
// nothing (allow). Any internal error allows the stop with one line on errw.
func Stop(input []byte, env project.Env, out, errw io.Writer) {
	reason, err := runStop(input, env)
	if err != nil {
		fmt.Fprintln(errw, "obor stop hook: internal error; allowing the stop:", err)
		return
	}
	if reason != "" {
		b := config.Compact(map[string]string{"decision": "block", "reason": reason})
		fmt.Fprintln(out, b)
	}
}

// stopEvent is the part of the Stop event the hook reads.
type stopEvent struct {
	Cwd             string `json:"cwd"`
	SessionID       string `json:"session_id"`
	TranscriptPath  string `json:"transcript_path"`
	StopHookActive  any    `json:"stop_hook_active"` // only true counts (not "true")
	BackgroundTasks []struct {
		Status string `json:"status"`
	} `json:"background_tasks"`
}

type stopHook struct {
	ev   stopEvent
	env  project.Env
	cfg  *config.Config
	root string
	now  int64 // epoch seconds
	role project.Role

	bgRunning int64 // running background tasks
}

// staleBg is how long nothing in a worker's lane may change before its running background tasks look stale (T-167).
const staleBg = 1200

// stopProcs is the process table the background check reads (a test's fake; internal/background's one ps), stopSelf
// the hook's own pid in it (a test's fake: the walk up to the agent starts there).
var (
	stopProcs = background.ReadProcs
	stopSelf  = os.Getpid
)

func runStop(input []byte, env project.Env) (reason string, err error) {
	h := &stopHook{env: env}
	if json.Unmarshal(input, &h.ev) != nil {
		return "", nil // no event to act on
	}
	cwd := h.ev.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	repo, cfg, _, err := loadAt(cwd)
	if repo == nil || errors.Is(err, config.ErrNoConfig) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	h.cfg = cfg
	h.root = repo.Root
	h.role = project.DetectRole(repo, cfg, env)
	h.now = time.Now().Unix()
	for _, t := range h.ev.BackgroundTasks {
		if t.Status == "running" {
			h.bgRunning++
		}
	}

	// A background run this session started and that still runs blocks any agent's stop (T-1142): the turn it ended is
	// not over, and obor would read the silence as finished work. First, so the reason is the run, not the task.
	if reason := h.background(); reason != "" {
		return reason, nil
	}

	// The role's verdict: a block reason, or "" (allow). Only a worker's (its task_done); no block for context size
	// (T-232): the handoff reminder rides on each prompt (handoffReminder).
	if h.role.Role == "worker" {
		return h.workerDone()
	}
	return "", nil
}

// background: a background Bash call recorded for this hook's session (the PreToolUse record, project.BackgroundCalls)
// whose process still runs blocks the stop, the call's description and age named (T-1142). Only the agent's own
// processes count (RV-169 #1): the agent is the CLI that ran this hook (background.AgentOf, up from the hook's pid) and
// the call's process one of its descendants (background.Live), so another agent running the same command blocks
// nothing. Every agent's, the lead's too; stop_hook_active allows nothing here — a repeated block means the agent is
// waiting, and the block ends with the call's process. Best-effort: no session, no record, an unreadable file, no
// process table or no agent process found allows the stop.
func (h *stopHook) background() string {
	if h.ev.SessionID == "" || h.root == "" {
		return ""
	}
	pane, _ := project.Pane(h.env) // "" when this session has none: a call that names no agent is then not its own
	calls := background.Mine(project.BackgroundCalls(h.root), project.SelfAgent(h.cfg, h.role), h.ev.SessionID, pane)
	if len(calls) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	procs, err := stopProcs(ctx)
	if err != nil {
		return ""
	}
	self := stopSelf()
	agent := background.AgentOf(procs, self)
	if agent == 0 {
		return ""
	}
	others := procs[:0:0] // the hook itself is none of the agent's runs
	for _, p := range procs {
		if p.PID != self {
			others = append(others, p)
		}
	}
	c, ok := background.Live(calls, others, agent, time.Unix(h.now, 0))
	if !ok {
		return ""
	}
	return fmt.Sprintf("a background run you started is still running: %s (started %s ago); wait for it in this turn "+
		"(e.g. an until-loop on its output), or stop it; a turn that ends now looks finished to obor and its result "+
		"reaches nobody", background.Text(c), time.Duration(h.now-c.At)*time.Second)
}

// --- worker -----------------------------------------------------------------------------------------------------------

// workerDone: block while the lane's task awaits its task_done (its row's finished, read-only: serve may be down),
// once per stop loop: a stop within an active loop (stop_hook_active) is allowed, so a worker that asks instead
// can stop. A row whose blocked is set (the worker's Blocked: report, as serve's fold sees it) allows the stop.
// Nothing is written. Running background tasks allow the stop unless they look stale (T-167).
func (h *stopHook) workerDone() (string, error) {
	lane := h.role.Lane
	if lane == "" || h.ev.StopHookActive == true {
		return "", nil
	}
	row, ok, err := store.ReadLaneAt(h.cfg.Root, lane)
	if err != nil || !ok || row.Task == "" {
		return "", err // no row, or submitted
	}
	if row.Finished != 0 || row.Blocked {
		return "", nil // submitted, or the task's Blocked: report awaits the owner's answer
	}

	// Background tasks running: the worker may wait on them, unless they look stale (T-167) — nothing in the lane changed
	// for staleBg seconds and no `obor run` is alive.
	stale := ""
	if h.bgRunning > 0 {
		dir := filepath.Join(h.cfg.LanesDir(), lane)
		newest := project.NewestChange(dir)
		if h.now-newest <= staleBg || runAlive(dir) {
			return "", nil
		}
		stale = fmt.Sprintf("Your background tasks look stale: check them (/ps). (%d running, nothing in the lane "+
			"changed for %ds and no obor run is alive.)\n", h.bgRunning, h.now-newest)
	}
	return "obor (worker " + lane + "): not done yet — no task_done for " + row.Task + " yet.\n" + stale +
		"Before stopping, call the obor task_done tool with your final report as its report argument (" +
		strings.Join(config.ReportSections, ", ") + ": a `Name:` label each).\n" +
		"If you cannot finish, call task_done with a Blocked: report naming what you need, then stop and wait. Already sent one? Stop again.", nil
}

// runAlive: an `obor run` in the lane dir is alive (its marker <dir>/.task-run, internal/run.MarkerName, holds a live
// pid, T-192).
func runAlive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, ".task-run"))
	if err != nil {
		return false
	}
	first, _, _ := strings.Cut(string(b), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	return err == nil && pid > 0 && project.PIDAlive(pid)
}
