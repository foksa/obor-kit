// registry.go: which pane each agent of a project runs in, and which agent a pane runs.
// The table and its rules are package identity's (proj.reg); here are the lookups that read the observed panes:
// whereIs (agent → pane) and the table's WhoIs (pane → agent) are the app's only identity lookups, and reconcile
// collects the proofs the table accepts. whereCall is both lookups as the socket's where request: a caller outside serve
// asks the table, never a copy of its proofs.
package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"obor/internal/app/identity"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
)

// State is what whereIs found of an agent.
type State int

const (
	Identified  State = iota // exactly one pane proves the agent: act on it
	Ambiguous                // two or more panes claim it with equal proof: act on none, say which
	Unavailable              // no pane proves it now: not started, exited, at its shell, not connected
)

// Where is an agent's pane, resolved now: Pane (Identified: as last observed, its ID the address of every call) and
// the proof that holds it, the panes that claim it (Ambiguous, sorted), or why there is none (Unavailable).
type Where struct {
	State  State
	Pane   port.Pane
	Source string
	Panes  []string
	Why    string
}

// notConnected is whereIs's Why while serve is not connected to herdr: nothing is known, not "no agent".
const notConnected = "not connected"

// whereIs is agent's pane in p now (under mu): the only agent → pane lookup.
func (a *App) whereIs(p *proj, agent string) Where {
	if !a.connected {
		return Where{State: Unavailable, Why: notConnected}
	}
	e := p.reg.Lookup(agent)
	if len(e.Panes) > 0 {
		return Where{State: Ambiguous, Panes: e.Panes}
	}
	if e.Term != "" {
		if o := a.terminal(p, e.Term, e.Pane); o != nil && o.pane.Agent != "" {
			return Where{State: Identified, Pane: o.pane, Source: e.Source}
		}
	}
	return Where{State: Unavailable, Why: a.why(p, agent)}
}

// shownAs is the name obor status and the watchdog facts give pane: its agent, else its label (" (not identified)"
// when two panes claim that agent), for a pane no proof holds (under mu).
func (a *App) shownAs(p *proj, pane port.Pane) string {
	if agent, ok := p.reg.WhoIs(pane); ok {
		return agent
	}
	if pane.Label != "" && len(p.reg.Lookup(pane.Label).Panes) > 0 {
		return pane.Label + " (not identified)"
	}
	return pane.Label
}

// notIdentified is the line that names an ambiguity: "panes w1:p1 and w1:p3 run pj-o-01 (close or relabel one)".
func notIdentified(agent string, w Where) string {
	return identity.NotIdentified(agent, w.Panes)
}

// terminal is the observed pane of p with terminal key t, looked for at pane id first (nil: none; under mu).
func (a *App) terminal(p *proj, t, id string) *seen {
	if o := a.obs[id]; o != nil && identity.TermOf(o.pane) == t && a.projectOf(o.pane) == p {
		return o
	}
	for _, o := range a.obs {
		if identity.TermOf(o.pane) == t && a.projectOf(o.pane) == p {
			return o
		}
	}
	return nil
}

// why is why no pane proves agent (under mu): its start's pane, or a pane labelled agent, is at its shell; its start's
// pane is gone; or nothing proves it.
func (a *App) why(p *proj, agent string) string {
	if row, ok := p.reg.Start(agent); ok {
		o := a.obs[row.Pane]
		if row.Terminal != "" {
			o = a.terminal(p, row.Terminal, row.Pane)
		}
		switch {
		case o != nil && o.pane.Agent == "" && a.projectOf(o.pane) == p:
			return "at its shell"
		case o == nil:
			return "exited"
		}
	}
	for _, o := range a.obs {
		if o.pane.Label == agent && o.pane.Agent == "" && a.projectOf(o.pane) == p {
			return "at its shell"
		}
	}
	return "no proof"
}

// reconcile rebuilds p's table from the proofs that hold now (under mu): every candidate agent × agent pane of p, each
// pair's strongest proof (identity's Reconcile accepts them). The config is read once per pass, not per pane: names is
// whether it names an agent (a start row or a recorded session of an agent it does not name is no proof), and its
// agents are candidates whatever other state holds of them.
func (a *App) reconcile(p *proj) {
	if !a.connected {
		return
	}
	var mine []port.Pane
	for _, o := range a.obs {
		if o.pane.Agent != "" && a.projectOf(o.pane) == p {
			mine = append(mine, o.pane)
		}
	}
	logp := func(line string) { a.logf("%s: %s", p.name, line) }
	configured, names := namedAgents(p.root)
	best := identity.Claims{} // by agent and terminal: the strongest proof
	for _, agent := range a.candidates(p, mine, configured, names) {
		sid := a.recordedSid(p, agent) // read once per candidate, not per pane
		named := names(agent)
		for _, pane := range mine {
			p.reg.CrossCheck(agent, sid, pane, named, logp)
			if src := p.reg.Proof(agent, sid, pane, named); src != "" {
				best.Add(agent, pane, src)
			}
		}
	}
	p.reg.Reconcile(best, names, logp)
}

// namedAgents is the agents the current config names (config.Load, as serve reads it elsewhere: its lead, integrator,
// roles and lanes' workers, Config.Agents) and whether it names an agent, by the name rules launch resolves
// (AgentLookup). A config that does not load names no agent to list and every agent asked about: a broken file drops
// no proof.
func namedAgents(root string) ([]string, func(string) bool) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, func(string) bool { return true }
	}
	return cfg.Agents(), func(agent string) bool {
		_, ok := cfg.AgentLookup(agent, "")
		return ok
	}
}

// recordedSid is agent's recorded session id (under mu): its current session fact (sessions.go); "": none.
func (a *App) recordedSid(p *proj, agent string) string {
	s, _ := p.session(agent)
	return s.ID
}

// candidates is the agents a pane of p may run, sorted: every agent the config names (configured), its roles, its
// lanes' and loop's agents, the agents serve started or has lines for, and the labels of mine (so a hand-started pane
// is named). Each pane is reconciled against all of them at once, so whatever agent is asked about, the table names
// each pane's owner. The start row of an agent the config does not name is no candidate (named): it gives no proof.
func (a *App) candidates(p *proj, mine []port.Pane, configured []string, named func(string) bool) []string {
	set := map[string]bool{p.out.Lead(): true, p.integrator: true}
	for _, agent := range configured {
		set[agent] = true
	}
	for _, l := range p.lanes {
		set[l.Agent] = true
	}
	for _, mt := range p.folded.loop {
		set[mt.agent] = true
	}
	for _, agent := range p.reg.StartedAgents() {
		if named(agent) {
			set[agent] = true
		}
	}
	for agent := range p.out.Briefs() {
		set[agent] = true
	}
	for _, q := range p.out.Queued() {
		set[q.Msg().To] = true
	}
	for _, pane := range mine {
		set[pane.Label] = true
	}
	delete(set, "")
	out := make([]string, 0, len(set))
	for agent := range set {
		out = append(out, agent)
	}
	sort.Strings(out)
	return out
}

// whereCall answers where: c.Name's pane in c's project and the agent each pane of c.Panes runs, from the table rebuilt
// now (under mu: reconcile, whereIs, WhoIs). A pane the request names that is not one of the project's observed panes
// runs "" (no agent the table knows of). Not connected to the terminal is its own state, with no panes' agents: nothing
// is known.
func (a *App) whereCall(c wire.WhereReq) (string, error) {
	if c.Name == "" {
		return "", errors.New("where: no agent named")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.projAt(c.Root)
	if p == nil {
		return "", fmt.Errorf("where: no served project at %s", c.Root)
	}
	if !a.connected {
		return whereJSON(wire.WhereReply{State: wire.WhereNotConnected})
	}
	a.reconcile(p)
	var r wire.WhereReply
	switch w := a.whereIs(p, c.Name); w.State {
	case Identified:
		r = wire.WhereReply{State: wire.WhereIdentified, Pane: w.Pane.ID, Source: w.Source, Label: w.Pane.Label}
	case Ambiguous:
		r = wire.WhereReply{State: wire.WhereAmbiguous, Panes: w.Panes, Why: notIdentified(c.Name, w)}
	default:
		r = wire.WhereReply{State: wire.WhereNone, Why: w.Why}
	}
	for _, id := range c.Panes {
		if r.Runs == nil {
			r.Runs = map[string]string{}
		}
		r.Runs[id] = ""
		if o := a.obs[id]; o != nil && a.projectOf(o.pane) == p {
			r.Runs[id], _ = p.reg.WhoIs(o.pane)
		}
	}
	return whereJSON(r)
}

// whereJSON is r as where's reply.
func whereJSON(r wire.WhereReply) (string, error) {
	b, err := json.Marshal(r)
	return string(b), err
}
