package app

import (
	"cmp"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"time"
)

// The answer request (MethodAnswer, in internal/app/wire) is the lead's or the owner's answer to a worker's question (obor answer <T-xx> "<text>",
// docs/state-model.md row 16b): the worker's Blocked: report recorded needs-you{asks: …} (row 16a), which stays in
// force while the worker waits, whatever its pane does (the Stop hook's turn after the report is no answer, T-511).
// The answer is fact answered{to: the worker, line "[obor answer] <text>"} of the project's lines: its fold ends the
// question (the task's watchdog facts end, as a resumed fact ends them), and the worker gets the line as its kind
// takes one (deliver: posted to a Claude session's inbox, typed for the others), in the same attempt. Refused when the
// task has no question in force: a plain note to a worker has no path (obor sendback is the other). A driving task
// (T-761) takes an answer with no question: the drive agent gets the line as a worker does (answered{driver}); each
// answer is its own line (a newer one ends no older), and a land or a drop closes an undelivered one unsent. An answer result
// of an older log (answer.<call id>, a fix job of the next attempt) still folds as it did.

func answerReply(task, agent string) string {
	return fmt.Sprintf("ANSWERED %s: obor serve sends %s the answer", task, agent)
}

// answer answers the lead's or the owner's obor answer (under a.verdicts): sendbackDecide's refusals (the caller, an
// empty text, the task not open, a step of it running, no question in force), else fact answered. A repeated call id
// gets the stored reply.
func (a *App) answer(c wire.Call) (string, error) {
	a.mu.Lock()
	p := a.projAt(c.Root)
	mt, reply, err := a.sendbackDecide(p, wire.MethodAnswer, c)
	a.mu.Unlock()
	if err != nil || reply != "" {
		return reply, err
	}
	m := event.Message{To: mt.agent, From: cmp.Or(c.Agent, "owner"), Line: "[obor answer] " + c.Note, Call: c.CallID,
		Task: c.Task, Driver: mt.drive}
	m.Reply = answerReply(c.Task, mt.agent)
	if note := a.unnamed(p, mt.agent, ""); note != "" {
		m.Reply += " (" + note + ")"
	}
	if !a.record(p, event.Record{Task: event.MsgTask, Step: event.StepAnswered, Kind: event.KindFact}, m) {
		return "", fmt.Errorf("answer: the event log of %s refused it", p.name)
	}
	a.logf("%s %s by %s", p.name, m.Reply, Dash(c.Agent))
	a.wake()
	return m.Reply, nil
}

// unanswerable is why mt cannot be answered now (under mu; "": it can): its worker works on it (working or
// dispatched, not a needs-you of the merge loop) and asked a question that is in force; or it is driving (T-761: the
// lead answers a drive's line with no question open, each answer its own line to the drive agent).
func (p *proj) unanswerable(mt mtask) string {
	switch {
	case mt.drive && mt.state == stDriving:
		return ""
	case mt.state != stWorking && mt.state != stDispatched:
		return fmt.Sprintf("%s is %s, not waiting on an answer", mt.task, mt.state)
	case !p.asks(mt.task):
		return fmt.Sprintf("%s has no open question (a worker asks with a Blocked: report; obor sendback sends notes)",
			mt.task)
	}
	return ""
}

// answered ends task's question (an answer's fold): all its watchdog facts end, the question with them, and the stall
// timer starts afresh (a reset event: the time the worker waited on its question is no stall).
func (p *proj) answered(task string) {
	p.clear(task)
	if w := p.tasks[task]; w != nil {
		w.quietSince, w.seenWrite = time.Time{}, time.Time{}
	}
}
