package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"time"
)

// The project's pause (T-465; docs/state-model.md row 46): `obor pause [--for <dur>] "<why>"` records fact
// paused{why, until} on the project's task key pause; while it holds (no until, or until still ahead) a dispatch is
// refused with its reason and a waiting task stays waiting (both through dispatchTask). Running gates, accepts and
// merges finish. `obor pause --clear` records fact unpaused; a pause past its until ends by itself.

// foldPause folds r, a record of the project's pause: the pause in force, and the reply of its call.
func (p *proj) foldPause(r event.Record) {
	if r.Task != event.PauseTask || r.Kind != event.KindFact || r.Step != event.StepPaused && r.Step != event.StepUnpaused {
		return
	}
	var d event.Pause
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodPause, d.Call)] = d.Reply
	}
	p.folded.pause = nil
	if r.Step == event.StepPaused {
		d.Since = r.TS
		p.folded.pause = &d
	}
}

// paused is the pause in force in p at now (under mu; nil: none).
func (p *proj) paused(now time.Time) *event.Pause {
	if p.folded.pause == nil || p.folded.pause.Until != 0 && now.Unix() >= p.folded.pause.Until {
		return nil
	}
	return p.folded.pause
}

// pausedErr is why a dispatch is refused while ps holds.
func pausedErr(task string, ps *event.Pause) error {
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
func (a *App) pauseCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodPause + ": a call id is required")
	}
	a.verdicts.Lock() // as dispatchCall's admission: an admission in flight ends before PAUSED, later ones see it
	defer a.verdicts.Unlock()
	a.mu.Lock()
	p := a.projAt(c.Root)
	key := verdictStep(wire.MethodPause, c.CallID)
	reply, stored := "", false
	var held *event.Pause
	if p != nil {
		reply, stored = p.folded.calls[key]
		held = p.paused(a.opt.Now())
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodPause, c.Root)
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
	d := event.Pause{Why: c.Note, Until: c.Until, Caller: c.Agent, Call: c.CallID, Reply: "PAUSED until " + untilText(c.Until)}
	if d.Caller == "" {
		d.Caller = "owner"
	}
	step := event.StepPaused
	if c.Clear {
		step, d.Why, d.Until, d.Reply = event.StepUnpaused, "", 0, "PAUSE CLEARED"
	}
	if !a.record(p, event.Record{Task: event.PauseTask, Step: step, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("pause: the event log of %s refused it", p.name)
	}
	if c.Clear {
		a.wake() // a waiting task may dispatch now
	}
	return d.Reply, nil
}
