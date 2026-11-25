// roledone.go: serve's reminder to the lead for a role agent done with no open work (a rule, rules.go), and obor
// status' hint of the command that stops it. A reminder, never a stop: the lead may still want a follow-up in that
// session.
package app

import (
	"context"
	"obor/internal/app/event"
	"obor/internal/config"
	"sort"
	"time"

	"obor/internal/app/describe"
	"obor/internal/app/port"
)

// roleDoneAfter is how long a role agent's pane may sit idle or done, with no open work, before the lead hears of it:
// a constant, no config key.
const roleDoneAfter = time.Hour

// ruleRoleDone is the done reminder's rule (rules.go); its lines are keyed Of{roleDone(agent), the stretch's start},
// not by the rule's name.
const ruleRoleDone = "role.done"

// roleDoneRule is the done reminder: a role agent done for roleDoneAfter with no open work (doneRoles) whose line for
// this done stretch is not recorded gets the lead's line, keyed Of{roleDone(agent), the stretch's start} (the rule's
// key that start). One agent a pass, in name order: its recorded line wakes the loop, whose next rule pass lines the
// next. None without a lead, nor on a pass that cannot post lines (no Prompter and no Inbox: the line steps of
// advance's list do not run on it either).
func roleDoneRule(_ context.Context, a *App, p *proj, fx *Loop, now time.Time) (int64, action, bool) {
	if fx == nil || fx.Cfg == nil || (fx.Prompter == nil && fx.Inbox == nil) {
		return 0, action{}, false
	}
	stretches := a.doneRoles(fx.Cfg, now) // before the lock: it reads the panes' transcripts
	agents := make([]string, 0, len(stretches))
	for agent := range stretches {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.out.Lead() == "" {
		return 0, action{}, false
	}
	for _, agent := range agents {
		since := stretches[agent]
		of := event.Of{Kind: roleDone(agent), Seq: since.Unix()}
		if _, did := p.folded.did[of]; did {
			continue
		}
		return of.Seq, action{line: roleDoneLine(agent, now.Sub(since)), of: of}, true
	}
	return 0, action{}, false
}

// roleDone is the line's Of kind for agent: one kind per agent, its Seq the start of its done stretch (Unix seconds),
// so the line goes once per done stretch and the log keeps it (folded.did: a replay sends none for the same stretch).
func roleDone(agent string) string { return "role.done." + agent }

// roleDoneLine is the lead's line for agent, done for age with no open work.
func roleDoneLine(agent string, age time.Duration) string {
	return "[obor] " + agent + " done " + describe.Age(age) + ", no open work — obor restart " + agent + " --stop (the next " +
		"obor brief starts it fresh)"
}

// roleStopHint is the command that stops agent: obor status ends its line with it (wire.AgentStatus.Hint).
func roleStopHint(agent string) string { return "obor restart " + agent + " --stop" }

// roleDonePane is one of p's role agents' panes a done stretch may start in: the agent, its pane (overlaid with its
// recorded session's transcript, when any) and the kind that reads it.
type roleDonePane struct {
	agent string
	pane  port.Pane
	kind  string
}

// roleDonePanes is the panes a done stretch may start in now (under mu): a pane of p that is ready (idle or done),
// which the identity table has for one agent (two are no agent), and that agent is a role agent of no lane, not the
// lead, with nothing open for it (roleAgents, openWork). While serve is not connected to the terminal there are none:
// its panes are not known now, and the next connect decides afresh.
func (a *App) roleDonePanes(p *proj, cfg *config.Config) []roleDonePane {
	if !a.connected {
		return nil
	}
	var cands []roleDonePane
	for _, o := range a.obs {
		if a.projectOf(o.pane) != p || !Ready(o.pane.Status) {
			continue
		}
		if agent, ok := p.reg.WhoIs(o.pane); ok {
			cands = append(cands, roleDonePane{agent: agent, pane: o.pane})
		}
	}
	if len(cands) == 0 {
		return nil
	}
	roles := p.roleAgents(cfg)
	var out []roleDonePane
	for _, c := range cands {
		if role, ok := roles[c.agent]; ok && !p.openWork(c.agent, role) {
			c.pane, c.kind = p.sessionPane(c.pane)
			out = append(out, c)
		}
	}
	return out
}

// doneRoles is the served project's role agents done for roleDoneAfter now, each with the start of its done stretch:
// the agent's own last activity (activity.go), the transcript's last write, which outlives serve — a fresh serve dates
// the same stretch the same, so the line's dedup key (roleDone, Seq = the stretch's start) matches across a restart and
// a restart neither repeats nor postpones a reminder. Unknown activity (a kind whose transcripts obor cannot read) is
// unknown, not inactive: no entry. Not under mu: it reads the panes' transcripts between its mu sections. While serve
// is not connected to the terminal there is nothing: no hint and no line from a stale pane. obor status' hint and the
// lead's line are this one condition.
func (a *App) doneRoles(cfg *config.Config, now time.Time) map[string]time.Time {
	a.mu.Lock()
	var cands []roleDonePane
	p := a.p
	if p != nil {
		cands = a.roleDonePanes(p, cfg)
	}
	a.mu.Unlock()
	out := map[string]time.Time{}
	for _, c := range cands {
		last, known := a.activity.Activity(p.kind(c.kind), c.pane)
		if known && now.Sub(last) >= roleDoneAfter {
			out[c.agent] = last
		}
	}
	return out
}
