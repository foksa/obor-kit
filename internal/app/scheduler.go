package app

import "sort"

// The scheduler's rule for serve's worktree (the scratch), in one place (T-669, RV-75): the pass's order (passOrder),
// who holds the scratch (scratchHolder) and what a task's scratch step waits on (scratchWait). pickGate, pickAccepted
// and advance decide with them, and obor status reports the same (scratchQueue): the view copies it, never predicts.

// Why a task holds the scratch, or what a task waiting for it waits on (ScratchStatus.Why, ScratchWait.Why).
const (
	ScratchGating     = "gating"     // its gate runs
	ScratchPublishing = "publishing" // its publish runs
	ScratchConflict   = stConflict   // its merge conflicts there: the resolve job is not sent yet
	ScratchResolving  = stResolving  // its resolve job is out
	ScratchResolved   = stResolved   // the integrator committed the merge there: its gate is next
	ScratchHeld       = "held"       // a needs-you keeps one of the three above: the owner decides
)

// ScratchStatus is the task that holds the scratch and why (obor status; nil: the scratch is free).
type ScratchStatus struct {
	Task string `json:"task"`
	Why  string `json:"why"`
}

// ScratchWait is one task whose next step needs the scratch (a gate or a publish), in the pass's order: WaitsOn the
// task it waits for and Why (scratchWait: that task's ScratchStatus.Why, or gating / publishing for its step). Both
// empty: nothing serve knows holds the scratch; no task is predicted to take it (T-673: the scheduler's rules are
// more than status can copy).
type ScratchWait struct {
	Task    string `json:"task"`
	WaitsOn string `json:"waits_on,omitempty"`
	Why     string `json:"why,omitempty"`
}

// passOrder is the order a pass drives p's tasks in (advance): by id as strings (T-10 before T-9), the waiting ones
// last (a task they wait for may merge in the same pass). Under mu.
func (p *proj) passOrder() []string {
	var tasks, waiting []string
	for task, mt := range p.folded.loop {
		if mt.state == stWaiting {
			waiting = append(waiting, task)
		} else {
			tasks = append(tasks, task)
		}
	}
	sort.Strings(tasks)
	sort.Strings(waiting)
	return append(tasks, waiting...)
}

// scratchHolder is the task of p other than except whose merge is open in the scratch (rule: one merge there at a
// time) and why: a conflict, its resolve job, the merge resolved, also while a needs-you pauses that work (held,
// RV-38 #9). It is released when the task abandons it (reject, regate, a new attempt) or merges. "": none. Under mu.
func (p *proj) scratchHolder(except string) (task, why string) {
	for _, t := range p.passOrder() {
		mt := p.folded.loop[t]
		if t == except || !states[mt.kept()].scratch {
			continue
		}
		if mt.state == stNeedsYou {
			return t, ScratchHeld
		}
		return t, mt.state
	}
	return "", ""
}

// scratchWait is what mt's scratch step (its state's uses) waits on: the gate or publish that runs (p.running), or,
// for a gate of a task not holding the scratch itself, another task's merge open there (scratchHolder). "": it
// takes the scratch now, or its state needs none. Under mu.
func (p *proj) scratchWait(mt mtask) (task, why string) {
	use := states[mt.state].uses
	if use == "" {
		return "", ""
	}
	if use == ScratchGating && !states[mt.state].scratch {
		if t, why := p.scratchHolder(mt.task); t != "" {
			return t, why
		}
	}
	if p.running.Task != "" {
		return p.running.Task, runningUse(p.running.Step)
	}
	return "", ""
}

// runs: why names a gate or publish running, not a merge open in the scratch.
func runs(why string) bool { return why == ScratchGating || why == ScratchPublishing }

// runningUse is what the running scratch step does: gating or publishing.
func runningUse(step string) string {
	if base, _ := stepN(step); base == StepPublish {
		return ScratchPublishing
	}
	return ScratchGating
}

// scratchQueue is the scheduler's state (obor status): who holds the scratch and why (nil: free) and, in the pass's
// order, each task whose next step needs it with what it waits for (scratchWait). A task nothing known holds says
// nothing more: the queue names no task as next (T-673: the admission rules are the scheduler's, and copying them
// was a second, partial copy that went stale). Under mu.
func (p *proj) scratchQueue() (*ScratchStatus, []ScratchWait) {
	var owner *ScratchStatus
	if p.running.Task != "" {
		owner = &ScratchStatus{Task: p.running.Task, Why: runningUse(p.running.Step)}
	} else if t, why := p.scratchHolder(""); t != "" {
		owner = &ScratchStatus{Task: t, Why: why}
	}
	var out []ScratchWait
	for _, task := range p.passOrder() {
		mt := p.folded.loop[task]
		if states[mt.state].uses == "" || task == p.running.Task || p.ended[task] {
			continue
		}
		w := ScratchWait{Task: task}
		w.WaitsOn, w.Why = p.scratchWait(*mt)
		out = append(out, w)
	}
	return owner, out
}

// scratchCommand is the command the running gate runs now (Scratch.Gate reports each as it starts and "" as it ends:
// the class's, the guard, the full check, lane_setup); "" when none runs now, between two commands too (T-673). Under
// mu.
func (p *proj) scratchCommand(k Key) string {
	if base, _ := stepN(k.Step); k != p.running || base != StepGate {
		return ""
	}
	return p.runCmd
}
