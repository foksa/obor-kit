package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/store"
)

// Specs live in the project's store (T-440); serve is their one writer. obor spec set is a request on the app socket,
// the lead role's only (a worker cannot unlock files by editing its own spec, T-238); show and list read the table
// read-only. A dispatch needs a stored spec, the work job's text carries it (a fix job's too when its rev changed
// since the worker's last job), and the gate writes the current rev into every candidate.

// MethodSpecSet is the app socket's spec request: Call.Spec stored as Call.Task's spec (store.SetSpec), the reply
// "SET <task> rev <n>" (or "UNCHANGED <task> rev <n>" when the text is the stored one).
const MethodSpecSet = "spec_set"

// specSet answers a spec request: a call id is required and a repeated one gets the stored reply. A new rev wakes the
// merge loop (a waiting task of it may dispatch now).
func (a *App) specSet(c Call) (string, error) {
	if c.CallID == "" || c.Task == "" {
		return "", errors.New(MethodSpecSet + ": a call id and a task are required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	key := verdictStep(MethodSpecSet, c.CallID)
	reply, stored := "", false
	if p != nil {
		reply, stored = p.replies[key]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodSpecSet, c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead": // the owner (no role) too, as for dispatch (DD-v0.11 A3); a worker never (T-238)
		return "", fmt.Errorf("spec set: only the lead or the owner sets a spec, not %s (role %s)", Dash(c.Agent),
			c.Role)
	}
	if _, ok := spec.Difficulty(c.Spec); !ok {
		return "", fmt.Errorf("spec set %s: the spec needs a \"Difficulty: <level>\" line right under its title (%s)", c.Task,
			strings.Join(spec.Levels, ", "))
	}
	if types := spec.Types(c.Spec); len(types) > 0 {
		cfg, err := config.LoadDaemon(p.root)
		if err != nil {
			return "", fmt.Errorf("spec set %s: %v", c.Task, err)
		}
		names := cfg.LaneTypeNames()
		for _, t := range types {
			if !slices.Contains(names, t) {
				return "", fmt.Errorf("spec set %s: \"Type: %s\" names a lane type this project does not have (lane types: %s)", c.Task, t,
					strings.Join(names, ", "))
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
	rev, changed, err := store.SetSpec(p.log.db, c.Task, c.Spec, by, a.opt.Now())
	if err != nil {
		return "", err
	}
	reply = fmt.Sprintf("SET %s rev %d", c.Task, rev)
	if !changed {
		reply = fmt.Sprintf("UNCHANGED %s rev %d", c.Task, rev)
	}
	a.mu.Lock()
	p.replies[key] = reply
	a.mu.Unlock()
	if changed {
		a.wake()
	}
	return reply, nil
}

// specOf is task's stored spec in p (ok false: none).
func specOf(p *proj, task string) (store.SpecRow, bool, error) { return store.ReadSpec(p.log.db, task) }

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
