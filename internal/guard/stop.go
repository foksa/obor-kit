package guard

// The Stop, SessionStart and UserPromptSubmit hooks (`obor hook stop|session-start|user-prompt-submit`, run through
// bin/obor; T-470: package stop moved here): the worker's task_done, the session's obor context, the handoff reminder
// and the wrong-tab guard. The lead and the integrator stop when they like (T-465: no nudges, no ledger).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	now  int64 // epoch seconds
	role project.Role

	bgRunning int64 // running background tasks
}

// staleBg is how long nothing in a worker's lane may change before its running background tasks look stale (T-167).
const staleBg = 1200

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
	h.role = project.DetectRole(repo, cfg, env)
	h.now = time.Now().Unix()
	for _, t := range h.ev.BackgroundTasks {
		if t.Status == "running" {
			h.bgRunning++
		}
	}

	// The role's verdict: a block reason, or "" (allow). Only a worker's (its task_done); no block for context size
	// (T-232): the handoff reminder rides on each prompt (handoffReminder).
	if h.role.Role == "worker" {
		return h.workerDone()
	}
	return "", nil
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
