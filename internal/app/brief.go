// brief.go: the brief lifecycle: a brief recorded, the work serve sees on it, the finished brief's line, its line in
// obor status.
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"obor/internal/app/describe"
	"obor/internal/app/event"
	"obor/internal/app/identity"
	"obor/internal/app/outbox"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/store"
)

// sawWork notes pane's status for an open brief of the agent whoIs finds in it (no label read): working
// after the brief's line was delivered (or with no line) is the work the brief asked for (outbox.SawWork; under mu,
// after reconcile).
func (a *App) sawWork(p *proj, pane port.Pane) {
	agent, ok := p.reg.WhoIs(pane)
	if !ok || pane.Agent == "" || Ready(pane.Status) {
		return
	}
	p.out.SawWork(agent)
}

// logWorked records fact worked for each open brief of p whose work was seen and is not in the log yet (not under mu).
// Each brief is claimed under mu (logging) while its fact is recorded: two passes at once (the merge loop's and the
// watchdog's) never both record it; a refused record leaves it for the next pass (logged is the fact's fold's).
func (a *App) logWorked(p *proj) {
	type claim struct {
		w outbox.Worked
		b *outbox.Brief
	}
	a.mu.Lock()
	var worked []claim
	for agent, b := range p.out.Briefs() {
		if b.Worked() && !b.Logged() && !b.Logging {
			b.Logging = true
			worked = append(worked, claim{outbox.Worked{To: agent, Brief: b.Seq()}, b})
		}
	}
	a.mu.Unlock()
	sort.Slice(worked, func(i, j int) bool { return worked[i].w.To < worked[j].w.To })
	for _, c := range worked {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepWorked, Kind: event.KindFact}, c.w)
		a.mu.Lock()
		c.b.Logging = false
		a.mu.Unlock()
	}
}

// briefsDone is the finished briefs' pass over p (a step of advance's list): where each open brief's agent is seen
// noted (noteSeen, on every pass), and each finished brief's line to the lead recorded, in agent order (idle, or its
// agent gone, not while a restore is pending or running; none while a stop is in force; briefDone). A finished
// reviewer's line names its review file (finishedReview), read outside mu.
func (a *App) briefsDone(p *proj, fx *Loop, now time.Time) {
	a.mu.Lock()
	var done []event.Message
	var reviewed map[string]string // the finished agents' review files (finishedReview): by agent, the review id + "[-verify]"
	for agent, b := range p.out.Briefs() {
		w := a.whereIs(p, agent)
		noteSeen(b, w, now) // noted on every pass: where its agent was last seen
		if a.briefDone(p, agent, b, w, now) {
			done = append(done, event.Message{To: p.out.Lead(), From: "obor", Ends: agent,
				Line: fmt.Sprintf("[obor] %s finished (briefed %s ago)", agent, describe.Age(now.Sub(b.At())))})
			if file := p.folded.rv.BriefFile(agent, b.Seq()); file != "" {
				if reviewed == nil {
					reviewed = map[string]string{}
				}
				reviewed[agent] = file
			}
		}
	}
	a.mu.Unlock()
	if len(reviewed) > 0 { // the review files are read outside mu, once, as the lines are composed
		if s, err := ReviewConfig(fx.Cfg); err == nil {
			for i, m := range done {
				if file := reviewed[m.Ends]; file != "" && m.Ends == s.Agent {
					done[i].Line = finishedReview(p.root, s.Reviews, file, m.Ends)
				}
			}
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Ends < done[j].Ends })
	for _, m := range done {
		a.record(p, event.Record{Task: event.MsgTask, Step: event.StepMessage, Kind: event.KindFact}, m)
	}
}

// goneAfter is how long a worked brief's agent may stay unidentified with its pane still running an agent before its
// brief ends: a short reconnect, or a relabel after a serve restart, does not end it.
const goneAfter = time.Minute

// noteSeen notes in brief b where its agent, resolved now as w, is seen (b.Term, b.PaneID; b.Lost: since when no pane
// identifies it while serve is connected), as gone reads it.
func noteSeen(b *outbox.Brief, w Where, now time.Time) {
	switch {
	case w.State == Identified:
		b.Term, b.PaneID, b.Lost = identity.TermOf(w.Pane), w.Pane.ID, time.Time{}
	case w.State == Ambiguous || w.Why == notConnected:
		b.Lost = time.Time{}
	case b.Lost.IsZero():
		b.Lost = now
	}
}

// gone: agent of brief b, resolved now as w, is gone (under mu): its last pane runs no agent (at its shell, or
// closed), its only labelled pane is at its shell (a brief replayed from the log: no pane seen yet), or no pane has
// identified it for goneAfter while serve is connected (b.Lost, noteSeen's note). Two panes claiming it (Ambiguous) are
// no gone agent.
func (a *App) gone(p *proj, agent string, b *outbox.Brief, w Where, now time.Time) bool {
	if w.State == Identified || w.State == Ambiguous || w.Why == notConnected {
		return false
	}
	if b.Term != "" {
		if o := a.terminal(p, b.Term, b.PaneID); o == nil || o.pane.Agent == "" {
			return true
		}
	}
	if _, ok := a.labelledShell(p, agent); ok {
		return true
	}
	return !b.Lost.IsZero() && now.Sub(b.Lost) >= goneAfter
}

// briefDone: agent's brief b, its agent resolved now as w, is done (under mu): worked, no stop in force, and its pane
// identified and ready, or its agent gone while no restore is pending or running (a gone agent's pane may be one the
// restore restarts). briefsDone records "<agent> finished" for it, which closes it; status shows it as wire.BriefDone.
func (a *App) briefDone(p *proj, agent string, b *outbox.Brief, w Where, now time.Time) bool {
	if !b.Worked() || b.Stop() != nil {
		return false
	}
	return w.State == Identified && Ready(w.Pane.Status) || a.gone(p, agent, b, w, now) && !a.restoring()
}

// briefStatus is p's open briefs as obor status lists them (under mu): each with its stage (briefStage) and the head
// of its line (clipBrief).
func (a *App) briefStatus(p *proj, now time.Time) []wire.BriefStatus {
	bs := p.out.BriefStatus()
	for i := range bs {
		if b := p.out.Brief(bs[i].To); b != nil {
			bs[i].Stage = a.briefStage(p, bs[i].To, b, bs[i].Delivered, now)
		}
		clipBrief(&bs[i])
	}
	return bs
}

// briefStage is where agent's open brief b is (under mu): its line still queued (not delivered), done by briefDone's
// rule, its agent's pane blocked at a prompt (waiting), else working: a pane at work or about to start, and an agent
// no pane identifies now, are never done before that rule says so.
func (a *App) briefStage(p *proj, agent string, b *outbox.Brief, delivered bool, now time.Time) string {
	w := a.whereIs(p, agent)
	switch {
	case !delivered:
		return wire.BriefNotDelivered
	case a.briefDone(p, agent, b, w, now):
		return wire.BriefDone
	case w.State == Identified && w.Pane.Status == "blocked":
		return wire.BriefWaiting
	}
	return wire.BriefWorking
}

// clipBrief cuts bs's text to wire.BriefTextMax bytes at a rune boundary, marking a cut text clipped.
func clipBrief(bs *wire.BriefStatus) {
	if len(bs.Text) > wire.BriefTextMax {
		bs.Text, bs.TextClipped = clip(bs.Text, wire.BriefTextMax), true
	}
}

// briefCall answers the socket's brief request (the lead or the owner): BriefReq.To, a role agent, is briefed; with a
// text, its line is delivered to that agent when its pane is idle (posted or typed as its kind takes it, deliver).
// The new brief ends the agent's undelivered brief lines (its fact's supersedes) and says so in its
// reply. The lead hears once the agent has finished. A brief to an idle deep dive moves its worktree to the
// integration branch's head first (briefMove; a publish running does not hold it); that git failing:
// refused, nothing recorded.
// BriefReq.Fresh (obor brief --fresh): the brief, once recorded, starts the agent fresh with its line as the
// first prompt, stopping it first when it runs (routeOf, deliverLine); a refused call stops nothing.
func (a *App) briefCall(c wire.BriefReq) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodBrief + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodBrief, c.Envelope)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodBrief, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	}
	fx, ferr := a.loopOf(p) // the call's one read of the config
	if err := leadOr(loaded(cfgOf(fx), ferr), wire.MethodBrief, "briefs", c.Envelope); err != nil {
		return "", err
	}
	if ferr != nil {
		return "", fmt.Errorf("brief: %v", ferr)
	}
	switch {
	case c.To == "":
		return "", errors.New("brief: the role agent is required")
	case c.To == p.out.Lead():
		return "", fmt.Errorf("brief: %s is the lead: obor send reaches another project's lead", c.To)
	case c.Fresh && strings.TrimSpace(c.Text) == "":
		return "", errors.New("brief: --fresh takes a text: the fresh session's first prompt")
	}
	from := c.Agent
	if from == "" {
		from = "owner"
	}
	a.mu.Lock()
	wtRole, restartable := p.wtRole(fx.Cfg, c.To), !c.Fresh || p.roleAgentOf(fx.Cfg, c.To)
	a.mu.Unlock()
	if !restartable {
		return "", fmt.Errorf("brief: %s is no role agent serve starts fresh (not the integrator or a worker)", c.To)
	}
	if wtRole != "" { // the reviewer or the deep dive
		ctx, cancel := context.WithTimeout(context.Background(), verifyGitWait)
		err := a.briefMove(ctx, p, fx, wtRole, c.To)
		cancel()
		if err != nil {
			return "", fmt.Errorf("brief: %s's worktree: %v", c.To, err)
		}
	}

	reply, ok := a.recordBrief(p, fx.Cfg, c.To, from, c.Text, c.CallID, c.Prompt, c.Fresh, nil)
	if !ok {
		return "", fmt.Errorf("brief: the event log of %s refused it", p.name)
	}
	a.wake() // a fresh brief's stop and start are the delivery's (deliverLine), in the merge loop's pass
	return reply, nil
}

// recordBrief records fact briefed of the role agent to from from (note "": the restart's or a review's fresh start
// carried the brief; call "": no call; prompt: that start's first prompt, its text when there is no line): its reply,
// false when the log refused it. The new brief ends the agent's undelivered brief lines (its fact's supersedes).
// Without a note, a pane already identified as the agent's may be working on it: that work is noted at once.
// The text sent is a row of the project's prompts table: the line for a brief, the start's prompt for a start.
// fresh: its line starts the agent fresh, the running agent stopped first. v: a verify's brief (nil: none;
// reviewverify.go): its tasks and head in the fact (Task, Reviewed), and not recorded (false) while the agent has an
// open brief, decided in the a.mu section that records it.
func (a *App) recordBrief(p *proj, cfg *config.Config, to, from, note, call, prompt string, fresh bool, v *verifyBrief) (string, bool) {
	// A newer brief makes an older undelivered brief obsolete: the agent's undelivered brief lines are named
	// in the new brief's fact, whose fold retires them (one append, so a crash or a refused append cannot
	// bring one back). A line whose delivery is claimed (sending: it may have landed) stays; a lead line is
	// never superseded. The choice and the append are one a.mu section: no delivery claims a chosen line meanwhile.
	a.mu.Lock()
	if v != nil && p.out.Brief(to) != nil { // a brief recorded since the verify's check: the next pass looks again
		a.mu.Unlock()
		return "", false
	}
	var old []int64
	for _, q := range p.out.Queued() {
		if q.IsBrief() && q.Msg().To == to && q.Msg().To != p.out.Lead() && q.State() == outbox.Waiting {
			old = append(old, q.Seq())
		}
	}
	m := event.Message{To: to, From: from, Call: call, Supersedes: old, Fresh: fresh && note != "",
		Reply: "BRIEF RECORDED for " + to + " (" + a.unnamedIn(p, cfg, to, "the lead hears when it finishes") + ")"}
	if v != nil {
		m.Task, m.Reviewed = strings.Join(v.tasks, ","), v.head
	}
	if note != "" {
		m.Line = "[obor " + from + "] " + note
		how := "delivered when its pane is idle; the lead hears when it finishes"
		if m.Fresh {
			how = "serve stops it and starts it fresh with this brief; the lead hears when it finishes"
		}
		m.Reply = "BRIEFED " + to + " (" + a.unnamedIn(p, cfg, to, how) + ")"
	}
	if len(old) > 0 {
		briefs := "briefs"
		if len(old) == 1 {
			briefs = "brief"
		}
		m.Reply += fmt.Sprintf(" (replaces %d undelivered %s)", len(old), briefs)
	}
	seq, ok := a.recordIn(p, event.Record{Task: event.MsgTask, Step: event.StepBriefed, Kind: event.KindFact}, m)
	a.mu.Unlock()
	if !ok {
		return "", false
	}
	if m.Line != "" {
		a.prompt(p, seq, store.PromptBrief, to, "", "", m.Line, 0)
	} else {
		a.prompt(p, seq, store.PromptStart, to, "", "", prompt, 0)
	}
	if m.Line == "" { // the restart's start carried the brief: the agent may be working on it already
		a.mu.Lock()
		if w := a.whereIs(p, to); w.State == Identified {
			a.sawWork(p, w.Pane)
		}
		a.mu.Unlock()
		a.logWorked(p)
	}
	return m.Reply, true
}

// unnamed is a reply's note on a line to agent: when serve, connected to herdr, cannot identify agent's pane in p
// (none runs it, or two claim it), that its line is queued until it can; else note.
func (a *App) unnamed(p *proj, cfg *config.Config, agent, note string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.unnamedIn(p, cfg, agent, note)
}

// unnamedIn is unnamed under mu.
func (a *App) unnamedIn(p *proj, cfg *config.Config, agent, note string) string {
	switch w := a.whereIs(p, agent); {
	case w.State == Ambiguous:
		return "queued: " + notIdentified(agent, w)
	case w.State == Unavailable && agent != p.integrator && a.stoppedAt(p, cfg, agent):
		return "stopped: serve starts it fresh with this brief; the lead hears when it finishes"
	case a.connected && w.State == Unavailable:
		return "queued: no pane runs " + agent + "; `obor restart " + agent + "` starts it"
	}
	return note
}
