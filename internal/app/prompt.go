package app

import (
	"context"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// Prompt delivery (docs/state-model.md "Prompts"). Every prompt of the loop (the worker's work, work.N and fix jobs,
// the integrator's review and resolve jobs, their retry.<job> and nudge.<job>, an owner's reprompt) is a job's wake
// (fetch.go): one constant line, harmless typed twice, lost or eaten by a dialog. send sends it under its key; the
// proof of delivery is the agent's job_next (fact delivered), and the delivery timers send it again until then. A
// pending prompt intent (a crash, a refusal) is sent again by the next pass that reaches its step.
//
// A job's wake to an agent whose pane's kind has an inbox (agents.Caps.Inbox: Claude) and whose session has an inbox
// record is posted there, "[obor job <id>] <text>" as typed: nothing typed. The text cannot land in a
// dialog: a session behind an open permission dialog reads it when its turn continues. Any other kind, or a session
// with no inbox record, is typed, its input line cleared first (an earlier wake a dialog ate may sit there): the record
// is missing only for a session its SessionStart hook did not register, and a wake refused there would leave the
// worker without its job until a restart (lines instead stay queued: deliver).

// promptData is every prompt intent's data: the agent prompted and its job; a review or resolve job's patch (folded
// as JobData), a fix job's note.
type promptData struct {
	Agent string `json:"agent,omitempty"`
	Job   string `json:"job,omitempty"`
	Patch string `json:"patch,omitempty"`
	Note  string `json:"note,omitempty"`
}

// send sends text, job's wake (with a nudge's lead before it), to data.Agent under key (the job's own, or a retry's or
// a nudge's) and is true once key has its result. A key with a result: nothing sent. Refused before a byte went out
// (herdr's refusal, no connection, a refused dial): an error, counted (the failure rule), sent again under its key on
// a later pass. A byte went out, then an error: result "sent: …" (the delivery timers catch a lost wake).
//
// The wake goes to the agent's pane as whereIs resolves it right before the send (never by herdr's agent name, which
// herdr drops at times). Not identified (two panes claim it, or none proves it): nothing is sent or recorded, the key
// stays pending and a later pass sends it.
//
// what names the prompt in a counted failure ("work job T-1/1/work not sent to pj-o-01").
func (a *App) send(ctx context.Context, p *proj, fx *Loop, mt mtask, key event.Key, job string, data promptData,
	text, what string) bool {
	data.Job = job
	a.mu.Lock()
	w := a.whereIs(p, data.Agent)
	post, inboxed := a.via(fx, p, w, data.Agent)
	a.mu.Unlock()
	if w.State != Identified { // nothing sent, nothing recorded: the key stays pending (reconcile logs an ambiguity)
		return false
	}
	target := w.Pane.ID
	if post != "" && inboxed {
		target = data.Agent
	} else {
		post = "" // no inbox record: typed (see above)
	}
	began := false
	_, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassPrompt, Data: data}, func(ctx context.Context) (any, error) {
		var err error
		began, err = fx.transmit(ctx, post, target, jobLine(job, text), true)
		return sentResult(began, err)
	})
	switch {
	case err == nil:
		return true
	case began: // sent; only its result is not in the log (App.do writes it on the next pass)
		return false
	}
	return a.failed(ctx, p, mt, key.Step, fmt.Errorf("%s: %w", what, err))
}

// via is how a wake or a line reaches agent, identified at w (under mu): posted to its session's inbox as kind post
// (w's pane's kind has one: posted) or typed into w's pane (post ""). inboxed: post's inbox can take it (fx.Inbox, and
// agent's record is the session live in w's pane (liveSession) or names none); false for a typed
// one when fx has no Typer.
func (a *App) via(fx *Loop, p *proj, w Where, agent string) (post string, inboxed bool) {
	if w.State == Identified && posted(w.Pane.Agent) {
		return w.Pane.Agent, fx.Inbox != nil && a.inboxedAt(p, agent, w)
	}
	return "", fx.Typer != nil
}

// transmit sends text to target: posted to agent target's session through kind post's inbox, or, post "", typed into
// pane target (clear: its input line cleared first). began: a byte of it went out.
func (fx *Loop) transmit(ctx context.Context, post, target, text string, clear bool) (began bool, err error) {
	if post != "" {
		return fx.Inbox.Post(ctx, post, target, text)
	}
	return fx.Typer.Type(ctx, target, text, clear)
}

// sentStart is the result of a delivery an agent's start carried: a job's wake as its first prompt (startJob), or a
// brief's line as a stopped role agent's.
const sentStart = "sent (start prompt)"

// sentResult is a send's result: "sent"; an error after a byte went out: "sent: <error>" (sent; the delivery timers or
// the reader catch a lost one); an error before: no result (sent again).
func sentResult(began bool, err error) (any, error) {
	switch {
	case err == nil:
		return "sent", nil
	case began:
		return "sent: " + err.Error(), nil
	}
	return nil, err
}

// jobLine is text as job's prompt: the job tag before it.
func jobLine(job, text string) string { return wire.JobLine(job, text) }
