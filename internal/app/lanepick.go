// lanepick.go: the lane a dispatch takes (held, freeFor, pickLane, pausedModels) and the replies that say why a lane was
// not taken or none was free (idleNote, typeNote, effortNote, unknownNote, laneWhy, noLane).
package app

import (
	"fmt"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/config"
)

// held is task's loop record (zero: none) and the lanes other tasks' open dispatches hold (lane → task).
func (a *App) held(p *proj, task string) (mtask, map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	held := map[string]string{}
	var mt mtask
	newest := map[string]int64{} // a lane two dispatches name: the newest dispatch's task holds it
	for _, t := range p.loopTasks() {
		if m := p.folded.loop[t]; t == task {
			mt = *m
		} else if _, ok := held[m.lane]; m.dispatching() && (!ok || m.dispSeq > newest[m.lane]) {
			held[m.lane], newest[m.lane] = t, m.dispSeq
		}
	}
	return mt, held
}

// freeFor: lane l can take a new dispatch: the one free rule (port.FreeState, the lane table's state: its row's task
// landed) says it is free (force: dirty-free too) and no other task's dispatch holds it (held, a.held's).
func freeFor(l port.Lane, held map[string]string, force bool) bool {
	return held[l.Name] == "" && (l.State == port.LaneFree || force && l.State == port.LaneDirtyFree)
}

// idleNote is a dispatch reply's tail "; idle: <LANE>, …": the lanes of ls still free after it (not taken, freeFor
// without force: dirty-free is not idle); "" when none.
func idleNote(ls []port.Lane, taken string, held map[string]string) string {
	var idle []string
	for _, l := range ls {
		if l.Name != taken && freeFor(l, held, false) {
			idle = append(idle, l.Name)
		}
	}
	if len(idle) == 0 {
		return ""
	}
	return "; idle: " + strings.Join(idle, ", ")
}

// typeNote is a dispatch reply's " as <model>" (the model its dispatch picked); "" for none.
func typeNote(typ string) string {
	if typ == "" {
		return ""
	}
	return " as " + typ
}

// pausedModels is the models of types (model refs) that obor pause --model holds now, each with its reason
// "<model> paused: <why> (until HH:MM|cleared)": dispatch skips them, a level's next model is used.
func (a *App) pausedModels(p *proj, types []string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]string{}
	for _, t := range types {
		typ, _ := config.ParseModelRef(t)
		if ps := p.modelPaused(typ, a.opt.Now()); ps != nil && out[typ] == "" {
			out[typ] = fmt.Sprintf("%s paused: %s (until %s)", typ, ps.Why, untilText(ps.Until))
		}
	}
	return out
}

// effortNote is a dispatch reply's " effort <effort>"; "" for none.
func effortNote(effort string) string {
	if effort == "" {
		return ""
	}
	return " effort " + effort
}

// pickLane is c's lane in ls and the model ref its dispatch runs (every lane is pooled): the lane it names
// (c's first model), else the lane that holds c.Task (working or blocked on it, and the task not finished: a redo,
// a redo) with ran, the model ref the task's dispatch ran (effort kept), unless c's explicit models (no level)
// name another (c's first), else for each model of c in order, skipped while paused, the first lane in config order
// that is free (dirty-free with force) and not held for another task's dispatch; else why not. A redo that changes the
// model it runs is held to the new model's pause like a fresh dispatch of it.
func pickLane(ls []port.Lane, c wire.DispatchReq, held map[string]string, paused map[string]string, ran string,
	finished bool) (port.Lane, string, string) {
	first := ""
	for _, t := range c.Types {
		if typ, _ := config.ParseModelRef(t); paused[typ] == "" {
			first = t
			break
		}
	}
	if c.Lane != "" {
		for _, l := range ls {
			if l.Name == c.Lane {
				if t := held[l.Name]; t != "" {
					return port.Lane{}, "", "lane " + l.Name + " is dispatched to " + t
				}
				if first == "" {
					return port.Lane{}, "", noLane(paused, c.Types)
				}
				l.Dir = filepath.Clean(l.Dir)
				return l, first, ""
			}
		}
		return port.Lane{}, "", "unknown lane " + c.Lane + " (not in the config's lanes)"
	}
	for _, l := range ls {
		if l.Task == c.Task && !finished && held[l.Name] == "" && l.State == port.LaneWorking {
			l.Dir = filepath.Clean(l.Dir)
			explicit := c.Level == "" && len(c.Types) > 0
			ref := ran
			if explicit || ran == "" {
				ref = first
			}
			ranType, _ := config.ParseModelRef(ran)
			if typ, _ := config.ParseModelRef(ref); ref == "" || typ != ranType && paused[typ] != "" {
				return port.Lane{}, "", noLane(paused, c.Types)
			}
			return l, ref, ""
		}
	}
	if first != "" {
		for _, l := range ls {
			if freeFor(l, held, c.Force) {
				l.Dir = filepath.Clean(l.Dir)
				return l, first, ""
			}
		}
	}
	return port.Lane{}, "", noLane(paused, c.Types) + unknownNote(ls)
}

// unknownNote is a lane refusal's tail naming the lanes whose state is unknown: dispatch never picks one, and
// its reply says which lane and why. "" when none.
func unknownNote(ls []port.Lane) string {
	var out []string
	for _, l := range ls {
		if l.State == port.LaneUnknown {
			out = append(out, l.Name+" unknown: "+l.Reason)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "; " + strings.Join(out, "; ")
}

// laneWhy is a lane's state for a refusal: its state, and for an unknown one why.
func laneWhy(l port.Lane) string {
	if l.State == port.LaneUnknown {
		return "unknown: " + l.Reason
	}
	return Dash(l.State)
}

// noLane is pickLane's refusal when none of types gets a lane: the models, and those paused.
func noLane(paused map[string]string, types []string) string {
	var at []string
	for _, t := range types {
		if typ, _ := config.ParseModelRef(t); paused[typ] != "" && !slices.Contains(at, paused[typ]) {
			at = append(at, paused[typ])
		}
	}
	why := "no free lane for the models " + strings.Join(types, ",")
	if len(at) > 0 {
		why += " (" + strings.Join(at, ", ") + ")"
	}
	return why
}
