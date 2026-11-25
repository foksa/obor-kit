// deliver.go: the delivery pass: each queued line to its agent, posted to its session or typed into its pane, and the
// route each line takes.
package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"obor/internal/app/describe"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
)

// Poster posts a line into agent's session through its inbox socket, as kind posts it (its pane's, B1): the
// lead's lines, and the lines and job wakes of an agent whose kind has an inbox. began: a byte of it went out (an error after that may have
// delivered it); an error with began false (no record, a refused dial) delivered nothing.
type Poster interface {
	Post(ctx context.Context, kind, agent, line string) (began bool, err error)
}

// deliver delivers p's queued lines (a step of advance's list): the lead's lines posted as one per target when its
// inbox is recorded, then each role agent's oldest line delivered when its pane is idle (posted or typed as its kind
// takes it).
func (a *App) deliver(ctx context.Context, p *proj, fx *Loop) {
	a.mu.Lock()
	var oldest []outbox.Line // each agent's oldest line
	var targets []string
	leadLines := map[string][]outbox.Line{} // the lines queued for the lead by their target (one post per target)
	taken := map[string]bool{}
	for _, q := range p.out.Queued() {
		if q.IsLead() || q.Msg().To == p.out.Lead() {
			if leadLines[q.Msg().To] == nil {
				targets = append(targets, q.Msg().To)
			}
			leadLines[q.Msg().To] = append(leadLines[q.Msg().To], *q)
		} else if !taken[q.Msg().To] {
			taken[q.Msg().To] = true
			oldest = append(oldest, *q)
		}
	}
	a.mu.Unlock()
	for _, to := range targets { // the target of the oldest line first
		a.mu.Lock()
		post, target, ok := a.route(p, fx, leadLines[to][0])
		a.mu.Unlock()
		if ok {
			a.deliverLead(ctx, p, fx, leadLines[to], post, target)
		}
	}
	for _, q := range oldest {
		// Resolved right before its own delivery: an earlier delivery of this pass may have moved,
		// duplicated or busied its agent.
		a.mu.Lock()
		post, target, ok := a.route(p, fx, q)
		a.mu.Unlock()
		if ok {
			a.deliverLine(ctx, p, fx, q, post, target)
		}
	}
}

// closeSettled closes unsent every settled open delivery of p: a line whose question was settled
// while its own delivery's intent is in the log with no result (a serve restart cut it) gets result outbox.RetiredResult,
// whatever its agent's pane (busy, not identified, ambiguous, its lane released) and whatever route it would take;
// it then leaves obor status and its needs-you. Through App.do: a result this process kept unwritten (the effect
// ran) is written instead, and a delivery whose effect runs now (its claim before its intent, or its transmit) is
// left to its outcome. A close the log refuses is tried again at the next pass.
func (a *App) closeSettled(ctx context.Context, p *proj) {
	a.mu.Lock()
	var keys []event.Key
	for _, q := range p.out.Queued() {
		k := event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10)}
		if q.State() == outbox.SendingSettled && q.Post() == q.Seq() && p.exec[k].IsZero() && p.log.Open(k) {
			keys = append(keys, k)
		}
	}
	a.mu.Unlock()
	for _, k := range keys {
		_, _ = a.do(ctx, p, event.Intent{Key: k, Class: event.ClassPrompt},
			func(context.Context) (any, error) { return outbox.RetiredResult, nil })
	}
}

// route is how q's line goes out now (under mu): posted to its agent's session (post: the kind whose inbox takes it;
// a lead line, or a ready agent whose kind has an inbox and whose inbox record is its pane's live session's), or
// typed into its ready agent's pane (post "", target: the pane's address, never herdr's name): via. A ready
// agent whose kind has an inbox but whose record cannot take the line gets it typed, as its job wakes are (a
// line to an idle worker is delivered, never held on a session mismatch); so does one whose last post of the line was
// refused before a byte went out (a refused dial of a dead socket is not tried again and again). ok false: held, no intent, nothing typed
// (delivered already, a lead line whose inbox is not recorded, its agent not identified (two panes claim it, or none
// proves it) or not ready); the line's held notes why (obor status, its needs-you).
func (a *App) route(p *proj, fx *Loop, q outbox.Line) (post, target string, ok bool) {
	m := p.out.Line(q.Seq())
	if m == nil {
		return "", "", false
	}
	post, target, m.Held = a.routeOf(p, fx, q)
	return post, target, m.Held == ""
}

// routeOf is route's choice for q (under mu): its post and target, or why it is held ("": it goes out).
func (a *App) routeOf(p *proj, fx *Loop, q outbox.Line) (post, target, held string) {
	to := q.Msg().To
	lead := q.IsLead() || to == p.out.Lead() // a lead line, or a brief whose agent has since become the lead
	w := a.whereIs(p, to)
	if lead {
		if why := a.hookHeld(p); why != "" { // its hooks ask for its lines (leadLinesCall)
			return "", "", why
		}
	}
	switch {
	case lead && (fx.Inbox == nil || !a.inboxed(to)):
		return "", "", "no inbox recorded for " + to + ": its SessionStart hook records one"
	case lead && w.State != Identified: // a lead line goes out whatever its pane: through the lead's configured kind
		return p.out.LeadKind(), to, ""
	case lead: // its record is its own latest SessionStart, fresher than herdr's view: no pane check
		return w.Pane.Agent, to, ""
	case q.IsBrief() && q.Msg().Fresh && w.State == Identified:
		return a.freshRoute(p, fx, q, w)
	case q.IsBrief() && to != p.integrator && fx.Agents != nil && w.State == Unavailable:
		if pane, ok := a.shellPane(p, fx.Cfg, to); ok { // a stopped role agent: started fresh on the brief
			return startRoute, pane.ID, ""
		}
		return "", "", to + " is not running and no shell pane of it is there to start it in"
	case w.State == Ambiguous:
		return "", "", notIdentified(to, w)
	case w.State != Identified:
		return "", "", to + " is not identified: " + Dash(w.Why)
	case !Ready(w.Pane.Status):
		return "", "", busyHeld(to, w.Pane.ID)
	}
	post, inboxed := a.via(fx, p, w, to)
	switch {
	case post != "" && inboxed && !(q.RefusedPost() && fx.Prompter != nil): // after a refused post: typed
		return post, to, ""
	case fx.Prompter != nil:
		return "", w.Pane.ID, ""
	case post != "":
		return "", "", fmt.Sprintf("no inbox recorded for the session in %s's pane %s, and serve types no lines", to,
			w.Pane.ID)
	}
	return "", "", "serve types no lines"
}

// busyHeld is routeOf's hold of a line to agent, busy in pane.
func busyHeld(agent, pane string) string {
	return fmt.Sprintf("%s is busy in pane %s (delivered once it is idle)", agent, pane)
}

// freshRoute is routeOf's choice for q, a fresh brief's line to its running agent w (under mu): stopped, then
// started fresh on the line in its pane (deliverLine), busy or idle. Held while its last stop failed less than
// roleStopRetry ago (a newer fresh brief is a line never refused: not held), or with no agent starts.
func (a *App) freshRoute(p *proj, fx *Loop, q outbox.Line, w Where) (post, target, held string) {
	to := q.Msg().To
	switch {
	case fx.Agents == nil:
		return "", "", "serve starts no agents: " + to + "'s fresh brief waits"
	case !p.roleAgentOf(fx.Cfg, to):
		return "", "", to + " is no role agent serve starts fresh"
	case q.Refused() != "" && a.opt.Now().Sub(p.roleFail[to]) < roleStopRetry:
		return "", "", fmt.Sprintf("its stop failed: tried again %s after the failure", describe.Age(roleStopRetry))
	}
	return freshStart, w.Pane.ID, ""
}

// freshStart is route's post for a fresh brief whose role agent runs (freshRoute): stopped (stopRoleNow), then started
// as a stopped one is (startRoute).
const freshStart = "fresh"

// posted: a running agent of kind (herdr's, its pane's) gets serve's lines and job wakes posted to its session's inbox
// (its bridge's Deliver posts, agents.Caps.Inbox), not typed into its pane.
func (p *proj) posted(kind string) bool {
	k := p.kind(kind)
	return k != nil && k.Caps().Inbox
}

// inboxed: agent's session inbox is recorded (Options.Inboxed; nil: assumed, the post says otherwise).
func (a *App) inboxed(agent string) bool {
	return a.inboxedAt(nil, agent, Where{})
}

// inboxedAt: agent's session inbox is recorded and, when w identifies agent's pane, the record's session is that
// pane's or names none, or herdr reports none for the pane (a record another session of that name wrote
// is not this pane's; an unknown session cannot tell).
func (a *App) inboxedAt(p *proj, agent string, w Where) bool {
	if a.opt.Inboxed == nil {
		return true
	}
	session, ok := a.opt.Inboxed(agent)
	if !ok || session == "" || w.State != Identified || w.Pane.ID == "" { // Where{}: no pane to check (a lead line)
		return ok
	}
	live := p.liveSession(agent, w.Pane)
	return live == "" || identity.SameSession(live, session)
}

// liveSession is the session running in pane, agent's (under mu): its recorded current session when that
// session was reported from pane (its SessionStart hook ran there: first-hand, and fresher than herdr's view), else
// herdr's agent_session for pane ("": unknown). herdr's value can be stale (it can name a session
// with no transcript while the pane runs the one its hook recorded), so it decides only when the record is not pane's.
func (p *proj) liveSession(agent string, pane port.Pane) string {
	if s, ok := p.session(agent); ok && s.Pane != "" && s.Pane == pane.ID {
		return s.ID
	}
	return pane.Session
}

// sameSession: a and b, each a session as liveSession gives it (a recorded id, or herdr's value: an id or its
// transcript's path), name one session. An unknown one ("") is no session: never the same.
func sameSession(a, b string) bool {
	return identity.SameSession(a, b) || identity.SameSession(b, a)
}

// deliverLine posts q's line to target's session through kind post's inbox (the lead, or an agent whose kind has
// one) or, post "", types it into target (its pane) under its delivery key (P, transmit). Refused before a byte went
// out: fact failed, no result, delivered again on a later pass; a byte went out and then an error: result "sent: …".
// A fresh start held by a trust dialog (port.TrustDialog) carried the line: sent, then the trust needs-you.
// A post cut before a byte (serve's own stop, or the post's own cancellation): nothing recorded, the line still
// queued, and while serve runs not posted again before a delay (uncounted). sent: its delivery's
// result is in the log and it went out (not closed unsent); a line another deliverLine is sending now is left to it.
func (a *App) deliverLine(ctx context.Context, p *proj, fx *Loop, q outbox.Line, post, target string) (sent bool) {
	key := event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(q.Seq(), 10)}
	// The claim: a brief recorded since route superseded the line: nothing is sent; else its claim (sending)
	// keeps any brief from superseding it from here on (the line may reach the pane).
	a.mu.Lock()
	m := p.out.Line(q.Seq())
	closing := false
	if m != nil && (m.Sending || p.cutWaits(key)) { // obor spec set's delivery and the pass's at once: one sends it; a cut post waits
		m = nil
	}
	if m != nil {
		closing = m.State() == outbox.SendingSettled // settled while a delivery was open (a crash cut it): closed unsent
		p.out.Move(m, outbox.Claimed)
		m.Sending = true
	}
	a.mu.Unlock()
	if m == nil {
		return false
	}
	defer func() {
		a.mu.Lock()
		m.Sending = false
		a.mu.Unlock()
	}()
	if post == freshStart && !closing { // the running agent stopped first (serve's role stop), then started
		if err := a.freshStop(ctx, p, fx, q, key); err != nil {
			return false
		}
		post = startRoute
	}
	began := false
	var trust *trustHold
	_, err := a.do(ctx, p, event.Intent{Key: key, Class: event.ClassPrompt, Data: outbox.LineData{To: q.Msg().To}},
		func(ctx context.Context) (any, error) {
			if closing {
				return outbox.RetiredResult, nil
			}
			if post == startRoute { // the stopped agent's fresh start in its pane carries the line
				st, err := fx.Agents.Launch(ctx, port.Launch{Agent: q.Msg().To, Pane: target, Fresh: true, Prompt: q.Msg().Line})
				if trust, err = trustHeld(st, err); err != nil { // held: started, the line is taken once the dialog is answered
					return nil, fmt.Errorf("%s not started on its brief: %w", q.Msg().To, err)
				}
				return sentStart, nil
			}
			var err error
			began, err = fx.transmit(ctx, post, target, q.Msg().Line, false)
			return sentResult(began, err)
		})
	if trust != nil { // after the delivery's result: the trust needs-you of the start the dialog holds
		a.trustNeeds(p, q.Msg().To, trust)
	}
	if err != nil && !began { // nothing went out: the next pass delivers it
		if cutByServe(ctx, err) { // the post was cut, not refused
			a.uncounted(ctx, p, key, event.MsgTask+" "+event.StepFailed+"{"+key.Step+": "+err.Error()+"}")
			return false
		}
		via := "" // a refused post: the next attempt types it
		if post != "" && post != startRoute {
			via = outbox.ViaInbox
		}
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + err.Error(), Step: key.Step, Via: via})
	}
	return err == nil && !closing
}

// freshStop stops q's agent before its fresh brief's start (deliverLine; q claimed): serve's one stop decision
// (stopAgent; the pane resolved now, the agent interrupted if it works). A failed stop starts the agent's retry clock
// (roleFail, freshRoute) and records fact failed for the line (key: its delivery), so the brief stays queued, its
// needs-you raised (msgNeedsYou) and the stop tried again; nothing is started.
func (a *App) freshStop(ctx context.Context, p *proj, fx *Loop, q outbox.Line, key event.Key) error {
	line, _, err := a.stopAgent(ctx, p, fx, q.Msg().To, false, true)
	if err == nil {
		a.logf("%s: %s stopped for its fresh brief: %s", p.name, q.Msg().To, line)
		return nil
	}
	if !cutByServe(ctx, err) {
		a.mu.Lock()
		p.roleFail[q.Msg().To] = a.opt.Now()
		a.mu.Unlock()
	}
	switch {
	case cutByServe(ctx, err):
		a.shutDown(event.MsgTask + " " + event.StepFailed + "{" + key.Step + ": " + err.Error() + "}")
	default:
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
			event.FactData{Machine: Machine, Reason: key.Step + ": " + q.Msg().To + " not stopped for its fresh brief: " +
				err.Error(), Step: key.Step})
	}
	return err
}

// deliverLead posts qs, the lines queued for one lead target (oldest first), as one message, one line per row, in
// order. A post's intent names its members and its target; its result settles exactly those members
// (the fold), so a line queued later waits for its own post. A post still open (its intent pending: refused before a
// byte, or cut by a crash; or its result kept unwritten in this process) goes again with exactly its members, or its
// kept result is written without a transmit, before any new post of the target (a recovery that transmitted nothing
// lets the rest go out in the same pass). A post refused before a byte went out: fact failed for each member, posted
// again as one at the next pass. Its members are claimed while it goes (claimLines): a hook call handing the lead's
// lines meanwhile leaves them to it, and the post waits for a line such a call is handing. Once the post's result is
// in the log each other member gets its own delivery result "sent" (the log's per-line record; its fold settles
// nothing more).
func (a *App) deliverLead(ctx context.Context, p *proj, fx *Loop, qs []outbox.Line, post, target string) {
	key := lineKey
	for {
		head, lines, recovered := a.leadPost(p, qs, key)
		if len(lines) == 0 {
			return
		}
		a.mu.Lock()
		waits := p.cutWaits(key(head))
		claimed := !waits && claimLines(p, lines, true)
		a.mu.Unlock()
		if !claimed { // its last post was cut: posted again after the delay (uncounted); or a hook call hands them now
			return
		}
		members := make([]int64, len(lines))
		text := make([]string, len(lines))
		for i, q := range lines {
			members[i], text[i] = q.Seq(), q.Msg().Line
		}
		began := false
		_, err := a.do(ctx, p, event.Intent{Key: key(head), Class: event.ClassPrompt, Data: outbox.LineData{To: lines[0].Msg().To, Lines: members}},
			func(ctx context.Context) (any, error) {
				var err error
				began, err = fx.transmit(ctx, post, target, strings.Join(text, "\n"), false)
				return sentResult(began, err)
			})
		if err != nil && !began && !recovered { // nothing went out: the next pass posts them again, as one
			if cutByServe(ctx, err) { // the post was cut, not refused
				what := event.MsgTask + " " + event.StepFailed + "{" + key(head).Step + ": " + err.Error() + "}"
				if len(lines) > 1 { // one line is named as its own record; a batched post says how many it was
					what += fmt.Sprintf(" for %d lines", len(lines))
				}
				a.uncounted(ctx, p, key(head), what)
			} else {
				for _, q := range lines {
					a.record(p, event.Record{Task: event.MsgTask, Step: event.StepFailed, Kind: event.KindFact},
						event.FactData{Machine: Machine, Reason: key(q.Seq()).Step + ": " + err.Error(), Step: key(q.Seq()).Step})
				}
			}
		}
		if err == nil {
			a.membersSent(ctx, p, lines, head)
		}
		a.mu.Lock()
		claimLines(p, lines, false)
		a.mu.Unlock()
		if err != nil || !recovered { // the post's result is not in the log: its members wait with it
			return
		}
	}
}

// claimLines claims lines for one delivery (on, under mu): none of them is being sent by another (a hook call handing
// the lead's lines, leadLinesCall) and each is marked Sending; false, nothing marked, when one is. on false releases
// them.
func claimLines(p *proj, lines []outbox.Line, on bool) bool {
	for _, q := range lines {
		if m := p.out.Line(q.Seq()); on && m != nil && m.Sending {
			return false
		}
	}
	for _, q := range lines {
		if m := p.out.Line(q.Seq()); m != nil {
			m.Sending = on
		}
	}
	return true
}

// lineKey is the delivery key of the line of fact seq (outbox.StepDeliver).
func lineKey(seq int64) event.Key {
	return event.Key{Task: event.MsgTask, Step: outbox.StepDeliver + strconv.FormatInt(seq, 10)}
}

// membersSent gives each line of lines but head, the members of head's post whose result is in the log, its own
// delivery result "sent" (the log's per-line record; its fold settles nothing more).
func (a *App) membersSent(ctx context.Context, p *proj, lines []outbox.Line, head int64) {
	for _, q := range lines {
		if q.Seq() != head {
			_, _ = a.do(ctx, p, event.Intent{Key: lineKey(q.Seq()), Class: event.ClassPrompt, Data: outbox.LineData{To: q.Msg().To}},
				func(context.Context) (any, error) { return "sent", nil })
		}
	}
}

// leadPost is the post deliverLead makes next of qs (one target's lines), its members claimed (sending): the open post
// of a line of qs (its key's seq, its members still queued), else every line of qs not delivered yet under the
// oldest's seq. recovered: the open post's result is kept unwritten (App.do writes it, nothing is transmitted).
func (a *App) leadPost(p *proj, qs []outbox.Line, key func(int64) event.Key) (head int64, lines []outbox.Line, recovered bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range qs {
		if m := p.out.Line(q.Seq()); m != nil && m.Post() != 0 {
			head = m.Post()
			break
		}
	}
	if head != 0 {
		for _, seq := range p.out.Post(head) {
			if m := p.out.Line(seq); m != nil && m.Post() == head {
				lines = append(lines, *m)
			}
		}
		_, recovered = p.unwritten[key(head)]
	} else {
		for _, q := range qs {
			if m := p.out.Line(q.Seq()); m != nil {
				lines = append(lines, *m)
			}
		}
		if len(lines) > 0 {
			head = lines[0].Seq()
		}
	}
	for _, q := range lines {
		p.out.Move(p.out.Line(q.Seq()), outbox.Claimed)
	}
	return head, lines, recovered
}

// merges: p has a merge loop, the only place its lines are delivered (design "Only for projects with a merge loop").
func (a *App) merges(p *proj) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return p.merging
}
