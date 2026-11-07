// stopstep.go: the stop of a task's worker as a step of the merge loop (T-852). A move (obor dispatch --model) and a
// drop (obor drop) record their decision and answer at once; the worker's stop is then the intent worker.stop of the
// task's next pass, before the step that needs its lane (the redo's lane.prepare and agent.start, the drop's
// lane.free). A done stop (stopped, not running) tells the lead nothing: its own command's reply said it (chatter census,
// T-943); a stop that fails is the needs-you's line, which obor resume retries (a drop's that needs --force: obor drop
// --force). A restart replays it: a pending intent runs again, a result is never run twice.
// A drop is recorded before its lane steps (fact dropping): a step that fails after it is retried by the loop, or is
// one needs-you, and the task stays dropping, its lane row its own, until fact dropped: never a half-done drop.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/port"

	"obor/internal/config"
)

// causeStop is the needs-you of a worker.stop that failed: obor resume retries it.
const causeStop = "stop"

// halt is the stop of a task's worker its move or drop asked for (mtask.halt): agent "" none; force: a working agent
// is stopped too (a move's always, a drop's with --force); n the last worker.stop.N with a result (the next is n+1, a
// pending intent's key); done once a result says stopped or gone; refused a result's refusal (the needs-you's why;
// obor resume clears it).
type halt struct {
	agent   string
	force   bool
	n       int
	done    bool
	refused string
}

// ask is a new stop of agent (a new decision), under the next key.
func (h *halt) ask(agent string, force bool) { *h = halt{agent: agent, force: force, n: h.n} }

// pending: a stop is asked for and not done (running, due, or refused).
func (h halt) pending() bool { return h.agent != "" && !h.done }

// stopOut is worker.stop's result: why it was not stopped and how to retry (Refused, final: a needs-you). A done stop
// records just that; its line and the agent gone are not read (T-943), so they are not written (T-960).
type stopOut struct {
	Refused string `json:"refused,omitempty"`
}

// foldStop folds worker.stop's result (attempt mt's).
func (mt *mtask) foldStop(n int, raw json.RawMessage) {
	var d stopOut
	_ = json.Unmarshal(raw, &d)
	h := &mt.halt
	h.n = n
	if d.Refused != "" {
		h.refused = d.Refused
	} else {
		h.done = true
	}
}

// stopKey is the key of the attempt's worker.stop.n.
func (mt *mtask) stopKey(n int) event.Key {
	return event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepWorkerStop, n)}
}

// pickStop is mt's stop step while its halt needs one (under mu; nil: none, the state's next step goes on): the stop,
// then for a refused stop its needs-you (its own lead line, "[obor] needs-you T-xx (L-nn): <agent> not stopped: <why> →
// …", is the one); a done stop sends no line.
func (a *App) pickStop(p *proj, mt mtask) step {
	h := mt.halt
	switch {
	case h.agent == "":
		return nil
	case !h.done && h.refused == "":
		return func(ctx context.Context, fx *Loop) bool { return a.stopStep(ctx, p, fx, mt) }
	case !h.done:
		return func(context.Context, *Loop) bool {
			return a.needsYou(p, &mt, causeStop, h.agent+" not stopped: "+h.refused)
		}
	}
	return nil
}

// stopStep stops mt's worker (halt.agent, with halt.force) through serve's identity of it (stopAgent; a stop that says
// not found while the agent is still identified is no success, RV-93 #1): result stopped, gone (no agent there), or
// refused with the retry (any stop that fails: a working agent without force, a quit timeout, two panes claiming it,
// herdr refusing the stop). Cut by serve's own stop, or serve not connected to herdr (a reconnect's gap: nothing is
// known of the agent): no result, the intent pending, tried again on the next pass (failed: uncounted while cut off).
func (a *App) stopStep(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	h := mt.halt
	in := event.Intent{Key: mt.stopKey(h.n + 1), Class: event.ClassStart,
		Data: stopData{Lane: mt.lane, Agent: h.agent, Force: h.force}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		_, _, err := a.stopAgent(ctx, p, fx, h.agent, h.force, false)
		switch {
		case err != nil && (cutByServe(ctx, err) || errors.Is(err, errCutOff) || a.where(p, h.agent).Why == notConnected):
			return nil, err
		case err != nil && mt.state == stDropping && !h.force && port.NeedsForce(err):
			return stopOut{Refused: fmt.Sprintf("%v; obor drop %s --force stops it", err, mt.task)}, nil
		case err != nil:
			return stopOut{Refused: fmt.Sprintf("%v; obor resume %s retries", err, mt.task)}, nil
		}
		return stopOut{}, nil
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// dropData is fact dropping's data (T-852): obor drop's decision on a task that holds a lane. Worker is the agent to
// stop first ("": none known), with Force; Discard frees a dirty lane; Agent who dropped it, Reason why; Call and Reply
// the call's id and its stored reply.
type dropData struct {
	Lane    string `json:"lane"`
	Worker  string `json:"worker,omitempty"`
	Agent   string `json:"agent,omitempty"`
	Reason  string `json:"reason"`
	Force   bool   `json:"force,omitempty"`
	Discard bool   `json:"discard,omitempty"`
	Call    string `json:"call,omitempty"`
	Reply   string `json:"reply"`
}

// freeOut is lane.free's result: the adapter's line, or its refusal (final: a needs-you).
type freeOut struct {
	Line    string `json:"line,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// dropReplyOf is fact dropping's reply: DROPPING <task> from <lane> (<why>; stopping <worker>).
func dropReplyOf(task string, d dropData) string {
	tail := oneLine(d.Reason)
	if d.Worker != "" {
		tail += "; stopping " + d.Worker
	}
	return fmt.Sprintf("DROPPING %s from %s (%s)", task, d.Lane, tail)
}

// foldDropping folds fact dropping: the task is dropping, its needs-you ended; a new decision (a retry with --force or
// --discard) asks for its worker's stop again unless it is stopped already, and frees its lane anew.
func (mt *mtask) foldDropping(r event.Record) {
	var d dropData
	_ = json.Unmarshal(r.Data, &d)
	again := mt.state == stDropping
	mt.unblock()
	mt.state, mt.drop, mt.fails = stDropping, d, nil
	mt.refused = ""
	if !again || !mt.halt.done || mt.halt.agent != d.Worker {
		mt.halt.ask(d.Worker, d.Force)
	}
}

// pickDropping is a dropping task's next step (under mu): its worker's stop (pickStop), then lane.free (a refusal: the
// needs-you, obor drop --discard or obor resume retries), then fact dropped.
func (a *App) pickDropping(p *proj, mt mtask) step {
	if s := a.pickStop(p, mt); s != nil || mt.halt.pending() {
		return s
	}
	switch {
	case mt.refused != "":
		return func(context.Context, *Loop) bool { return a.needsYou(p, &mt, causeLane, mt.refused) }
	case !mt.freed:
		return func(ctx context.Context, fx *Loop) bool { return a.freeStep(ctx, p, fx, mt) }
	}
	return func(context.Context, *Loop) bool {
		d := mt.drop
		a.logf("%s DROPPED %s from %s (%s)%s", p.name, mt.task, d.Lane, oneLine(d.Reason), lineTail(mt.freeLine))
		return a.record(p, event.Record{Task: mt.task, Attempt: mt.attempt, Step: event.StepDropped, Kind: event.KindFact},
			event.FactData{Lane: d.Lane, Machine: Machine, Agent: d.Agent, Reason: d.Reason, Merge: true})
	}
}

// lineTail is ": <line>" on one line, "" for none.
func lineTail(line string) string {
	if line == "" {
		return ""
	}
	return ": " + oneLine(line)
}

// freeStep gives a dropping task's lane back (lane.free, G; T-439): the lane adapter's Free with the lane's dir, the
// task branch and the target read now, refused for uncommitted changes or unmerged commits unless --discard (its
// worker stopped already), else the lane detached at the target and the task branch deleted.
func (a *App) freeStep(ctx context.Context, p *proj, fx *Loop, mt mtask) bool {
	d := mt.drop
	in := event.Intent{Key: event.Key{Task: mt.task, Attempt: mt.attempt, Step: nth(event.StepFree, mt.frees+1)},
		Class: event.ClassGit, Data: port.FreeInput{Dispatch: port.Dispatch{Lane: d.Lane}, Discard: d.Discard}}
	if _, err := a.do(ctx, p, in, func(ctx context.Context) (any, error) {
		if fx.Lane == nil || fx.Specs == nil {
			return nil, fmt.Errorf("lane adapters off for %s", p.name)
		}
		cfg, err := config.LoadDaemon(p.root)
		if err != nil {
			return nil, err
		}
		target, _, err := fx.Specs.Target(ctx, mt.task)
		if err != nil {
			return nil, err
		}
		fin := in.Data.(port.FreeInput)
		fin.Branch, fin.Target = cfg.TaskBranch(mt.task), target
		for _, l := range cfg.Lanes() {
			if l.Name == d.Lane {
				fin.Dir = l.Dir
			}
		}
		line, err := fx.Lane.Free(ctx, fin)
		var refused port.Refused
		if errors.As(err, &refused) {
			return freeOut{Refused: refused.Line}, nil
		}
		if err != nil {
			return nil, err
		}
		return freeOut{Line: line}, nil
	}); err != nil {
		return a.failed(ctx, p, mt, in.Step, err)
	}
	return true
}

// foldFree folds lane.free's result: freed, or its refusal (the needs-you's why, naming --discard).
func (mt *mtask) foldFree(n int, raw json.RawMessage) {
	var d freeOut
	_ = json.Unmarshal(raw, &d)
	mt.frees, mt.freeLine = n, d.Line
	if d.Refused != "" {
		mt.refused = fmt.Sprintf("lane: %s (obor drop %s --discard frees it)", oneLine(d.Refused), mt.task)
		return
	}
	mt.freed = true
}
