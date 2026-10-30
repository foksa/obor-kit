package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The project's pause (T-465; docs/state-model.md row 46): `obor pause [--for <dur>] "<why>"` records fact
// paused{why, until} on the project's task key pause; while it holds (no until, or until still ahead) a dispatch is
// refused with its reason and a waiting task stays waiting (both through dispatchTask). Running gates, accepts and
// merges finish. `obor pause --clear` records fact unpaused; a pause past its until ends by itself.

// MethodPause is the app socket's pause request: Call.Note the why, Call.Until its end (0: until cleared); Call.Clear
// ends the pause in force.
const MethodPause = "pause"

// PauseTask is the task key of a project's pause facts.
const PauseTask = "pause"

const (
	StepPaused   = "paused"   // fact: the project is paused (data: Pause)
	StepUnpaused = "unpaused" // fact: obor pause --clear ended it (data: Pause, the call's)
)

// Pause is a pause fact's data, and the pause in force in the status: why, until when (epoch seconds, 0: until
// cleared), who paused and since when.
type Pause struct {
	Why    string `json:"why,omitempty"`
	Until  int64  `json:"until,omitempty"`
	Caller string `json:"caller,omitempty"`
	Since  string `json:"since,omitempty"`
	Call   string `json:"call,omitempty"`
	Reply  string `json:"reply"`
}

// foldPause folds r, a record of the project's pause: the pause in force, and the reply of its call.
func (p *proj) foldPause(r Record) {
	if r.Task != PauseTask || r.Kind != KindFact || r.Step != StepPaused && r.Step != StepUnpaused {
		return
	}
	var d Pause
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.calls[verdictStep(MethodPause, d.Call)] = d.Reply
	}
	p.pause = nil
	if r.Step == StepPaused {
		d.Since = r.TS
		p.pause = &d
	}
}

// paused is the pause in force in p at now (under mu; nil: none).
func (p *proj) paused(now time.Time) *Pause {
	if p.pause == nil || p.pause.Until != 0 && now.Unix() >= p.pause.Until {
		return nil
	}
	return p.pause
}

// pausedErr is why a dispatch is refused while ps holds.
func pausedErr(task string, ps *Pause) error {
	return fmt.Errorf("dispatch %s: paused: %s (until %s; obor pause --clear ends it)", task, ps.Why, untilText(ps.Until))
}

// untilText is a pause's end as local HH:MM, "cleared" for none.
func untilText(until int64) string {
	if until == 0 {
		return "cleared"
	}
	return time.Unix(until, 0).Format("15:04")
}

// pauseCall answers the socket's pause request (the lead or the owner): a call id is required and a repeated one gets
// the stored reply. Pause: PAUSED until HH:MM|cleared (a new pause replaces the one in force); clear: PAUSE CLEARED,
// or NOT PAUSED when none holds (nothing recorded).
func (a *App) pauseCall(c Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(MethodPause + ": a call id is required")
	}
	a.verdicts.Lock() // as dispatchCall's admission: an admission in flight ends before PAUSED, later ones see it
	defer a.verdicts.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	key := verdictStep(MethodPause, c.CallID)
	reply, stored := "", false
	var held *Pause
	if p != nil {
		reply, stored = p.calls[key]
		held = p.paused(a.opt.Now())
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodPause, c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("pause: the lead or the owner pauses, not %s (role %s)", Dash(c.Agent), c.Role)
	case c.Clear && held == nil:
		return "NOT PAUSED", nil
	case !c.Clear && c.Note == "":
		return "", errors.New("pause: a reason is required")
	case !c.Clear && c.Until != 0 && c.Until <= a.opt.Now().Unix():
		return "", errors.New("pause: its end is not ahead")
	}
	d := Pause{Why: c.Note, Until: c.Until, Caller: c.Agent, Call: c.CallID, Reply: "PAUSED until " + untilText(c.Until)}
	if d.Caller == "" {
		d.Caller = "owner"
	}
	step := StepPaused
	if c.Clear {
		step, d.Why, d.Until, d.Reply = StepUnpaused, "", 0, "PAUSE CLEARED"
	}
	if !a.record(p, Record{Task: PauseTask, Step: step, Kind: KindFact}, d) {
		return "", fmt.Errorf("pause: the event log of %s refused it", p.name)
	}
	if c.Clear {
		a.wake() // a waiting task may dispatch now
	}
	return d.Reply, nil
}
