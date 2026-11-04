package app

import (
	"errors"
	"fmt"
	"obor/internal/app/wire"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/store"
)

// Specs live in the project's store (T-440); serve is their one writer. obor spec set is a request on the app socket,
// the lead role's only (a worker cannot unlock files by editing its own spec, T-238); show and list read the table
// read-only. A dispatch needs a stored spec, the work job's text carries it (a fix job's too when its rev changed
// since the worker's last job), and the gate writes the current rev into every candidate. A set while the task has a
// working (or dispatching) worker changes no message: its next task_done at the older rev is refused once (T-819,
// T-824).

// specSet answers a spec request: a call id is required and a repeated one gets the stored reply. A new rev wakes the
// merge loop (a waiting task of it may dispatch now); the reply is SET <task> rev <n>.
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
	if !changed {
		reply = fmt.Sprintf("UNCHANGED %s rev %d", c.Task, rev)
	}
	a.submitMu.Unlock()
	a.mu.Lock()
	p.replies[key] = reply
	a.mu.Unlock()
	if changed {
		a.wake()
	}
	return reply, nil
}

// specRevLine is the task_done refusal of a job carried at an older rev, naming the rev and the set's note (T-819).
func specRevLine(task string, rev int, note string) string {
	line := fmt.Sprintf("spec is now rev %d", rev)
	if note = strings.TrimSpace(note); note != "" {
		line += ": " + note
	}
	return line + " — read obor spec show " + task + ", adjust, then submit again"
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
