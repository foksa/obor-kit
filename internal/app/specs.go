package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/store"
)

// Specs live in the project's store (T-440); serve is their one writer. obor spec set is a request on the app socket,
// the lead role's only (a worker cannot unlock files by editing its own spec, T-238); show and list read the table
// read-only. A dispatch needs a stored spec, the work job's text carries it (a fix job's too when its rev changed
// since the worker's last job), and the gate writes the current rev into every candidate. A set while the task has a
// working (or dispatching) worker sends it one line and its next task_done at the older rev is refused once (T-819).
// The line is in one of three states (T-821): queued (its fact spec.line recorded), delivered (it reached the worker's
// session) and seen (task_done's refusal showed the rev, or the worker fetched a job at it: the line retires). The
// task_done check is the guarantee; the line is the courtesy that saves the worker a wasted report.

// specSet answers a spec request: a call id is required and a repeated one gets the stored reply. A new rev wakes the
// merge loop (a waiting task of it may dispatch now) and, with a working (or dispatching) worker, queues that worker
// one line (specLine) and delivers it at once where it can (tellNow; T-819, T-821): the reply ends "(worker told)" when it was
// delivered, "(queued: <why>)" when it waits for a later delivery pass (its worker busy: "busy").
func (a *App) specSet(c wire.Call) (string, error) {
	if c.CallID == "" || c.Task == "" {
		return "", errors.New(wire.MethodSpecSet + ": a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	key := verdictStep(wire.MethodSpecSet, c.CallID)
	reply, stored := "", false
	if p != nil {
		reply, stored = p.replies[key]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodSpecSet, c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead": // the owner (no role) too, as for dispatch (DD-v0.11 A3); a worker never (T-238)
		return "", fmt.Errorf("spec set: only the lead or the owner sets a spec, not %s (role %s)", Dash(c.Agent),
			c.Role)
	}
	if spec.Title(c.Spec) == "" {
		return "", fmt.Errorf("spec set %s: the spec needs a title: `# %s — <title>`", c.Task, c.Task)
	}
	if _, ok := spec.Difficulty(c.Spec); !ok {
		return "", fmt.Errorf("spec set %s: the spec needs a \"Difficulty: <level>\" line right under its title (%s)", c.Task,
			strings.Join(spec.Levels, ", "))
	}
	if spec.OldTypeLine(c.Spec) {
		return "", fmt.Errorf("spec set %s: the \"Type:\" line is now \"Model: <model>[:<effort>], …\" (T-765): rename it", c.Task)
	}
	if refs := spec.Models(c.Spec); len(refs) > 0 {
		cfg, err := config.LoadDaemon(p.root)
		if err != nil {
			return "", fmt.Errorf("spec set %s: %v", c.Task, err)
		}
		for _, t := range refs { // `<model>[:<effort>]` (T-695)
			if err := cfg.CheckModelRef(t); err != nil {
				return "", fmt.Errorf("spec set %s: \"Model: %s\": %v", c.Task, t, err)
			}
		}
	}
	by := c.Agent
	switch {
	case by != "":
	case c.Role == "":
		by = "owner"
	default:
		by = "lead"
	}
	// task_done's submission decides under a.submitMu: one order of "the spec changed" and "the work against it was
	// submitted" (T-819).
	a.submitMu.Lock()
	rev, changed, err := store.SetSpec(p.log.DB(), c.Task, c.Spec, c.Note, by, a.opt.Now())
	if err != nil {
		a.submitMu.Unlock()
		return "", err
	}
	reply = fmt.Sprintf("SET %s rev %d", c.Task, rev)
	var line int64
	if !changed {
		reply = fmt.Sprintf("UNCHANGED %s rev %d", c.Task, rev)
	} else {
		line = a.specLine(p, c, rev)
	}
	a.submitMu.Unlock()
	if line != 0 { // delivered outside submitMu: task_done's rev check needs only the stored rev
		if why := a.tellNow(p, line); why != "" {
			reply += " (queued: " + why + ")"
		} else {
			reply += " (worker told)"
		}
	}
	a.mu.Lock()
	p.replies[key] = reply
	a.mu.Unlock()
	if changed {
		a.wake()
	}
	return reply, nil
}

// specLine queues task's working (or dispatching) worker one line through the worker-line delivery (T-819, as obor
// resume's answer) that its spec is rev now: its fact's seq (0: no worker to tell, or the log refused it).
func (a *App) specLine(p *proj, c wire.Call, rev int) int64 {
	a.mu.Lock()
	var mt mtask
	if m := p.folded.loop[c.Task]; m != nil {
		mt = *m
	}
	to := mt.agent
	if to == "" && mt.lane != "" { // a dispatching attempt before its agent's name is in the lane row
		to = p.laneAgent(mt.lane)
	}
	a.mu.Unlock()
	if to == "" || mt.state != stWorking && mt.state != stDispatched {
		return 0
	}
	m := event.Message{To: to, From: cmp.Or(c.Agent, "owner"), Line: "[obor] " + specRevLine(c.Task, rev, c.Note),
		Task: c.Task}
	seq, _ := a.recordSeq(p, event.Record{Task: event.MsgTask, Step: event.StepSpecLine, Kind: event.KindFact}, m)
	return seq
}

// tellWait bounds obor spec set's own delivery of its line (tellNow): a post or a typing is quick; one that hangs is
// left to the delivery pass.
const tellWait = 5 * time.Second

// tellNow delivers the queued line of fact seq now, as a delivery pass would (route, deliverLine; T-821): "" when it
// was delivered, else why it waits for a later pass ("busy": its worker's pane works and its kind's inbox cannot
// reach a working session, or has no record of it).
func (a *App) tellNow(p *proj, seq int64) string {
	fx, err := a.loopOf(p)
	if err != nil {
		return err.Error()
	}
	if fx.Typer == nil && fx.Inbox == nil {
		return "serve delivers no lines"
	}
	a.mu.Lock()
	m := p.out.msgs[seq]
	if m == nil {
		a.mu.Unlock()
		return "its line retired" // the worker saw the rev already (a fetch or a refusal since the record)
	}
	q := *m
	post, target, ok := a.route(p, fx, q)
	why := m.held
	if w := a.whereIs(p, q.msg.To); !ok && w.State == Identified && !Ready(w.Pane.Status) {
		why = "busy"
	}
	a.mu.Unlock()
	if !ok {
		return why
	}
	ctx, cancel := context.WithTimeout(a.life, tellWait)
	defer cancel()
	if a.deliverLine(ctx, p, fx, q, post, target) {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if m := p.out.msgs[seq]; m != nil && m.why() != "" {
		return m.why()
	}
	return "its delivery did not complete: the next delivery pass tries again"
}

// specRevLine is what a new spec rev says to a working worker, and task_done's refusal of a job carried at an older
// rev names (T-819).
func specRevLine(task string, rev int, note string) string {
	line := fmt.Sprintf("%s spec is now rev %d", task, rev)
	if note = strings.TrimSpace(note); note != "" {
		line += ": " + note
	}
	return line + " — read obor spec show " + task + " and adjust; say in your report what you redid."
}

// specOf is task's stored spec in p (ok false: none).
func specOf(p *proj, task string) (store.SpecRow, bool, error) {
	return store.ReadSpec(p.log.DB(), task)
}

// titleOf is task's spec title in the store (T-440); "" when it has no spec or no title (not an error), and then
// subjectOf falls back to the report's summary. A read error is returned: the step fails, is retried and counted
// (T-367), never committed with the fallback subject.
func titleOf(p *proj, task string) (string, error) {
	row, ok, err := specOf(p, task)
	if err != nil || !ok {
		return "", err
	}
	return spec.Title(row.Text), nil
}

// specText is a worker job's spec part: the task's stored spec, its rev and its text.
func specText(row store.SpecRow) string {
	return fmt.Sprintf("\n\nThe spec of %s (rev %d, as obor serve stores it; obor spec show %s prints the current rev):\n\n%s",
		row.Task, row.Rev, row.Task, row.Text)
}
