package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"strconv"
	"strings"

	"obor/internal/config"
	"obor/internal/spec"
	"obor/internal/store"
)

// Specs live in the project's store; serve is their one writer. obor spec set is a request on the app socket,
// the lead role's only (a worker cannot unlock files by editing its own spec); show and list read the table
// read-only. A dispatch needs a stored spec, the work job's text carries it (a fix job's too when its rev changed
// since the worker's last job), and the gate writes the current rev into every candidate. A set while the task has a
// working (or dispatching) worker changes no message: its next task_done at the older rev is refused once.

// specSet answers a spec request: a call id is required and a repeated one gets the stored reply. A new rev wakes the
// merge loop (a waiting task of it may dispatch now); the reply is SET <task> rev <n>.
func (a *App) specSet(c wire.SpecSetReq) (string, error) {
	if c.CallID == "" || c.Task == "" {
		return "", errors.New(wire.MethodSpecSet + ": a call id and a task are required")
	}
	// The receipt lookup, the write and the receipt's record are one step under a.submitMu (task_done's lock; order
	// submitMu, then mu): a concurrent retry of the call id waits for the first call's receipt (SM-02).
	a.submitMu.Lock()
	defer a.submitMu.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	key := verdictStep(wire.MethodSpecSet, c.CallID)
	reply, stored := "", false
	if p != nil {
		reply, stored = p.folded.calls[key]
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodSpecSet, c.Root)
	case stored:
		return reply, nil
	}
	// The owner (no role) too, as for dispatch, and a custom role with the verb; a worker never.
	if err := leadOr(p.root, wire.MethodSpecSet, c.Envelope, fmt.Sprintf("spec set: only the lead or the owner sets a spec, not %s (role %s)",
		Dash(c.Agent), c.Role)); err != nil {
		return "", err
	}
	sp := spec.Parse(c.Spec)
	if err := spec.Validate(c.Task, sp); err != nil {
		return "", fmt.Errorf("spec set %s: %w", c.Task, err)
	}
	if len(sp.Models) > 0 {
		cfg, err := config.Load(p.root)
		if err != nil {
			return "", fmt.Errorf("spec set %s: %v", c.Task, err)
		}
		for _, t := range sp.Models { // `<model>[:<effort>]`
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
	default: // the lead, a custom role
		by = c.Role
	}
	rev, changed, err := store.SetSpec(p.log.DB(), c.Task, c.Spec, c.Changed, by, a.opt.Now())
	if err != nil {
		return "", err
	}
	reply = specSetReply(c.Task, rev, changed)
	// The receipt is a fact folded into folded.calls, so it outlives a restart. A crash between the store's commit and
	// this append makes the retry re-apply its text: UNCHANGED if nothing changed the spec meanwhile, else the old text
	// is written as a new rev (accepted).
	if !a.record(p, event.Record{Task: c.Task, Step: event.StepSpecSet, Kind: event.KindFact},
		event.SpecSet{Call: c.CallID, Reply: reply}) {
		return "", fmt.Errorf("spec set: the event log of %s refused it", p.name)
	}
	if changed {
		a.wake()
	}
	return reply, nil
}

// specRevLine is the task_done refusal of a job carried at an older rev, naming the rev and the set's note.
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

// titleOf is task's spec title in the store; "" when it has no spec or no title (not an error), and then
// subjectOf falls back to the report's summary. A read error is returned: the step fails, is retried and counted,
// never committed with the fallback subject.
func titleOf(p *proj, task string) (string, error) {
	row, ok, err := specOf(p, task)
	if err != nil || !ok {
		return "", err
	}
	return row.Spec.Title, nil
}

// specText is a worker job's spec part: the task's stored spec, its rev and its text.
func specText(row store.SpecRow) string {
	return fmt.Sprintf("\n\nThe spec of %s (rev %d, as obor serve stores it; obor spec show %s prints the current rev):\n\n%s",
		row.Task, row.Rev, row.Task, row.Text)
}

// foldSpecSet folds a spec_set's fact: its call's stored reply.
func (p *proj) foldSpecSet(r event.Record) {
	if r.Kind != event.KindFact || r.Step != event.StepSpecSet {
		return
	}
	var d event.SpecSet
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodSpecSet, d.Call)] = d.Reply
	}
	// A spec dropped before any dispatch is revived by a later rev: the task is a spec stored and
	// never dispatched again, with no loop record (queued, dispatchable, an --after on it waits).
	if mt := p.folded.loop[r.Task]; mt != nil && mt.state == stDropped && mt.droppedRev > 0 &&
		specSetRev(d.Reply) > mt.droppedRev {
		delete(p.folded.loop, r.Task)
	}
}

// specSetReply is spec_set's reply: SET <task> rev <n>, UNCHANGED <task> rev <n> when the text was the stored one.
func specSetReply(task string, rev int, changed bool) string {
	if !changed {
		return fmt.Sprintf("UNCHANGED %s rev %d", task, rev)
	}
	return fmt.Sprintf("SET %s rev %d", task, rev)
}

// specSetRev is the rev a spec_set's reply (specSetReply) names; 0 when it names none. The fact carries no rev of its
// own: its reply is the one record of it in every log.
func specSetRev(reply string) int {
	_, n, ok := strings.Cut(reply, " rev ")
	rev, err := strconv.Atoi(n)
	if !ok || err != nil {
		return 0
	}
	return rev
}
