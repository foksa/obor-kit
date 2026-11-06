package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"obor/internal/config"
	"strings"
	"time"
)

// The project's pause (T-465; docs/state-model.md row 46): `obor pause [--for <dur>] "<why>"` records fact
// paused{why, until} on the project's task key pause; while it holds (no until, or until still ahead) a dispatch is
// refused with its reason and a waiting task stays waiting (both through dispatchTask). Running gates, accepts and
// merges finish. `obor pause --clear` records fact unpaused; a pause past its until ends by itself.
//
// A model's pause (T-765): `obor pause --model <name> [--for <dur>] "<why>"` records the same fact with its model;
// while it holds, dispatch skips that model and a level's next one is used (all paused: the task waits). `obor pause
// --model <name> --clear` ends it.

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
	if d.Model != "" {
		if p.folded.models == nil {
			p.folded.models = map[string]*event.Pause{}
		}
		delete(p.folded.models, d.Model)
		if r.Step == event.StepPaused {
			d.Since = r.TS
			p.folded.models[d.Model] = &d
		}
		return
	}
	p.folded.pause = nil
	if r.Step == event.StepPaused {
		d.Since = r.TS
		p.folded.pause = &d
	}
}

// modelPaused is model's pause in force in p at now (under mu; nil: none).
func (p *proj) modelPaused(model string, now time.Time) *event.Pause {
	ps := p.folded.models[model]
	if ps == nil || ps.Until != 0 && now.Unix() >= ps.Until {
		return nil
	}
	return ps
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
// or NOT PAUSED when none holds (nothing recorded). With c.Model the same for that model's pause: PAUSED <model> …,
// PAUSE CLEARED <model>, NOT PAUSED <model>; a model the config does not know is refused. The config is read with no
// lock; the decision and the record are one mu section, the one every dispatch decides in: a dispatch recorded
// before it is in when PAUSED returns, one after it sees the pause.
func (a *App) pauseCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodPause + ": a call id is required")
	}
	key := verdictStep(wire.MethodPause, c.CallID)
	a.mu.Lock()
	p := a.projAt(c.Root)
	stored := false
	if p != nil {
		_, stored = p.folded.calls[key]
	}
	a.mu.Unlock()
	what := ""
	if c.Model != "" && p != nil && !stored {
		what = " " + c.Model
		cfg, err := config.LoadDaemon(p.root)
		if err != nil {
			return "", fmt.Errorf("pause: %v", err)
		}
		if _, ok := cfg.Model(c.Model); !ok {
			return "", fmt.Errorf("pause: %s is not a model (models: %s)", c.Model, strings.Join(cfg.ModelNames(), ", "))
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reply := ""
	var held *event.Pause
	if p != nil {
		reply, stored = p.folded.calls[key]
		held = p.paused(a.opt.Now())
		if c.Model != "" {
			held = p.modelPaused(c.Model, a.opt.Now())
		}
	}
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodPause, c.Root)
	case stored:
		return reply, nil
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("pause: the lead or the owner pauses, not %s (role %s)", Dash(c.Agent), c.Role)
	case c.Clear && held == nil:
		return "NOT PAUSED" + what, nil
	case !c.Clear && c.Note == "":
		return "", errors.New("pause: a reason is required")
	case !c.Clear && c.Until != 0 && c.Until <= a.opt.Now().Unix():
		return "", errors.New("pause: its end is not ahead")
	}
	d := event.Pause{Model: c.Model, Why: c.Note, Until: c.Until, Caller: c.Agent, Call: c.CallID,
		Reply: "PAUSED" + what + " until " + untilText(c.Until)}
	if d.Caller == "" {
		d.Caller = "owner"
	}
	step := event.StepPaused
	if c.Clear {
		step, d.Why, d.Until, d.Reply = event.StepUnpaused, "", 0, "PAUSE CLEARED"+what
	}
	if !a.recordLocked(p, event.Record{Task: event.PauseTask, Step: step, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("pause: the event log of %s refused it", p.name)
	}
	if c.Clear {
		a.wake() // a waiting task may dispatch now
	}
	return d.Reply, nil
}
