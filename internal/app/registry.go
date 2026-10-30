// registry.go: which pane each agent of a project runs in, and which agent a pane runs (P5-3, T-504, redesigned after RV-53).
// One table per project, derived every pass from proofs (reconcile), never a fact:
// whereIs (agent → pane) and whoIs (pane → agent) are the app's only identity lookups. herdr's agent name is never read
// (herdr drops it at times; nothing renames it back).
package app

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
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
	Pane   Pane
	Source string
	Panes  []string
	Why    string
}

// The proofs that an agent runs in a pane, strongest first (rank).
const (
	srcSession = "session" // the pane's agent_session is the agent's recorded session (sessions.go, else <agent>.sid; its start's)
	srcStart   = "start"   // the pane's terminal is the one serve's own start placed the agent in
	srcLabel   = "label"   // the pane's label is the agent's name (launch's mark)
)

func rank(src string) int {
	switch src {
	case srcSession:
		return 3
	case srcStart:
		return 2
	}
	return 1
}

// startRow is serve's own start of an agent (agent.start's or start.<job>'s result, foldStart): the pane it placed the
// agent in, that pane's terminal id ("": a result from before T-504, until its pane is seen), the kind and session.
type startRow struct {
	Pane, Terminal, Kind, Session string
}

// owner is the agent a terminal runs by the last reconcile, its proof and the pane id it was seen at.
type owner struct {
	agent, source, pane string
}

// termOf is pane's key in the table: herdr's terminal id (kept across pane.move), else its pane id.
func termOf(pane Pane) string {
	if pane.Terminal != "" {
		return pane.Terminal
	}
	return "pane " + pane.ID
}

// notConnected is whereIs's Why while serve is not connected to herdr: nothing is known, not "no agent".
const notConnected = "not connected"

// whereIs is agent's pane in p now (under mu): the only agent → pane lookup.
func (a *App) whereIs(p *proj, agent string) Where {
	if !a.connected {
		return Where{State: Unavailable, Why: notConnected}
	}
	if ids := p.ambiguous[agent]; len(ids) > 0 {
		return Where{State: Ambiguous, Panes: ids}
	}
	if t, ok := p.agents[agent]; ok {
		ow := p.owners[t]
		if o := a.terminal(p, t, ow.pane); o != nil && o.pane.Agent != "" {
			return Where{State: Identified, Pane: o.pane, Source: ow.source}
		}
	}
	return Where{State: Unavailable, Why: a.why(p, agent)}
}

// whoIs is the agent pane runs by p's table (under mu): the only pane → agent lookup.
func (a *App) whoIs(p *proj, pane Pane) (string, bool) {
	ow, ok := p.owners[termOf(pane)]
	return ow.agent, ok
}

// shownAs is the name obor status and the watchdog facts give pane: its agent, else its label (" (not identified)"
// when two panes claim that agent), for a pane no proof holds (under mu).
func (a *App) shownAs(p *proj, pane Pane) string {
	if agent, ok := a.whoIs(p, pane); ok {
		return agent
	}
	if pane.Label != "" && len(p.ambiguous[pane.Label]) > 0 {
		return pane.Label + " (not identified)"
	}
	return pane.Label
}

// notIdentified is the line that names an ambiguity: "panes w1:p1 and w1:p3 run pj-o-01 (close or relabel one)".
func notIdentified(agent string, w Where) string {
	return "not identified: panes " + strings.Join(w.Panes, " and ") + " run " + agent + " (close or relabel one)"
}

// terminal is the observed pane of p with terminal key t, looked for at pane id first (nil: none; under mu).
func (a *App) terminal(p *proj, t, id string) *seen {
	if o := a.obs[id]; o != nil && termOf(o.pane) == t && a.projectOf(o.pane) == p {
		return o
	}
	for _, o := range a.obs {
		if termOf(o.pane) == t && a.projectOf(o.pane) == p {
			return o
		}
	}
	return nil
}

// why is why no pane proves agent (under mu): its start's pane, or a pane labelled agent, is at its shell; its start's
// pane is gone; or nothing proves it.
func (a *App) why(p *proj, agent string) string {
	if row, ok := p.starts[agent]; ok {
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

// claim is one proof that agent runs in pane (terminal key term).
type claim struct {
	agent, term, src string
	pane             Pane
}

// reconcile rebuilds p's table from the proofs that hold now (under mu): every
// candidate agent × agent pane of p, each pair's strongest proof, accepted strongest first. A claim is accepted when
// neither its agent nor its terminal is taken; an agent whose strongest free claims name two or more terminals is
// ambiguous and takes none; two agents with equal proof of one terminal take it neither, and no weaker claim takes it
// in this pass (RV-53-verify V1: it is owned again once the tie ends). The result does
// not depend on any iteration order. A start row whose terminal another agent now owns is retired. Each change is
// logged once.
func (a *App) reconcile(p *proj) {
	if !a.connected {
		return
	}
	var mine []Pane
	for _, o := range a.obs {
		if o.pane.Agent != "" && a.projectOf(o.pane) == p {
			mine = append(mine, o.pane)
		}
	}
	for agent, row := range p.starts { // a start from before T-504: its pane's terminal, once seen
		if o := a.obs[row.Pane]; row.Terminal == "" && o != nil && o.pane.Terminal != "" && a.projectOf(o.pane) == p {
			row.Terminal = o.pane.Terminal
			p.starts[agent] = row
		}
	}
	best := map[[2]string]claim{} // by agent and terminal: the strongest proof
	for _, agent := range a.candidates(p, mine) {
		sid := a.recordedSid(p, agent) // read once per candidate, not per pane (DD-v0.13 A6)
		for _, pane := range mine {
			p.crossCheck(a, agent, sid, pane)
			if src := p.proof(agent, sid, pane); src != "" {
				k := [2]string{agent, termOf(pane)}
				if c, ok := best[k]; !ok || rank(src) > rank(c.src) {
					best[k] = claim{agent: agent, term: k[1], src: src, pane: pane}
				}
			}
		}
	}
	owners, agents, ambiguous := map[string]owner{}, map[string]string{}, map[string][]string{}
	done, contested := map[string]bool{}, map[string]bool{} // contested: equal proofs tied on it, unowned this pass
	for r := 3; r >= 1; r-- {
		free := map[string][]claim{}
		for _, c := range best {
			if rank(c.src) == r && !done[c.agent] && owners[c.term].agent == "" && !contested[c.term] {
				free[c.agent] = append(free[c.agent], c)
			}
		}
		want := map[string][]claim{} // by terminal
		for agent, cs := range free {
			if len(cs) > 1 {
				ids := make([]string, len(cs))
				for i, c := range cs {
					ids[i] = c.pane.ID
				}
				sort.Strings(ids)
				ambiguous[agent], done[agent] = ids, true
				continue
			}
			want[cs[0].term] = append(want[cs[0].term], cs[0])
		}
		for t, cs := range want {
			if len(cs) > 1 {
				contested[t] = true
				continue
			}
			owners[t] = owner{agent: cs[0].agent, source: cs[0].src, pane: cs[0].pane.ID}
			agents[cs[0].agent], done[cs[0].agent] = t, true
		}
	}
	a.logOwners(p, owners, agents, ambiguous, best)
	p.owners, p.agents, p.ambiguous = owners, agents, ambiguous
	for agent, row := range p.starts {
		t := row.Terminal
		if t == "" {
			t = "pane " + row.Pane
		}
		if ow, ok := owners[t]; ok && ow.agent != agent {
			delete(p.starts, agent)
			a.logf("%s: %s's start row retired: pane %s runs %s now (%s)", p.name, agent, ow.pane, ow.agent, ow.source)
		}
	}
}

// logOwners logs what changed in p's table: an agent identified (or at another pane), no longer identified, an
// ambiguity begun, and a label on a pane its agent is not proven in (each once).
func (a *App) logOwners(p *proj, owners map[string]owner, agents map[string]string, ambiguous map[string][]string,
	best map[[2]string]claim) {
	var lines []string
	for agent, t := range agents {
		ow := owners[t]
		if old, ok := p.agents[agent]; !ok || old != t || p.owners[old].pane != ow.pane {
			lines = append(lines, "agent "+agent+" identified at pane "+ow.pane+" ("+ow.source+")")
		}
	}
	for agent := range p.agents {
		if _, ok := agents[agent]; !ok && len(ambiguous[agent]) == 0 {
			lines = append(lines, "agent "+agent+" no longer identified")
		}
	}
	for agent, ids := range ambiguous {
		if !slices.Equal(p.ambiguous[agent], ids) {
			lines = append(lines, "agent "+agent+" "+notIdentified(agent, Where{Panes: ids}))
		}
	}
	strays := map[string]bool{}
	for _, c := range best {
		if t, ok := agents[c.agent]; ok && c.src == srcLabel && t != c.term {
			k := c.pane.ID + " " + c.agent
			strays[k] = true
			if !p.strays[k] {
				lines = append(lines, "pane "+c.pane.ID+" labelled "+c.agent+" too, not its "+owners[t].source)
			}
		}
	}
	p.strays = strays
	sort.Strings(lines)
	for _, l := range lines {
		a.logf("%s: %s", p.name, l)
	}
}

// recordedSid is agent's recorded session id (under mu): its current session fact (sessions.go, T-577), else
// Options.Sid (the hook's .sid file, else its inbox's); "": none.
func (a *App) recordedSid(p *proj, agent string) string {
	if s, ok := p.session(agent); ok {
		return s.ID
	}
	if a.opt.Sid != nil {
		return a.opt.Sid(agent)
	}
	return ""
}

// crossCheck logs, once per value, that herdr reports another session than agent's recorded sid for pane, the pane
// agent's start or label names (under mu): herdr's agent_session is a cross-check only (T-577), never adopted.
func (p *proj) crossCheck(a *App, agent, sid string, pane Pane) {
	row, started := p.starts[agent]
	ours := pane.Label == agent || started && (row.Terminal != "" && row.Terminal == pane.Terminal || row.Pane == pane.ID)
	if sid == "" || pane.Session == "" || !ours || sameSession(pane.Session, sid) || p.herdrSession[agent] == pane.Session {
		return
	}
	p.herdrSession[agent] = pane.Session
	a.logf("%s: herdr reports session %s for %s at pane %s; its recorded session is %s (kept)", p.name, pane.Session,
		agent, pane.ID, sid)
}

// proof is the strongest proof that agent runs in pane ("": none; under mu); sid is agent's recorded session
// (Options.Sid).
func (p *proj) proof(agent, sid string, pane Pane) string {
	row, started := p.starts[agent]
	switch {
	case sameSession(pane.Session, sid),
		started && sameSession(pane.Session, row.Session):
		return srcSession
	case started && (row.Terminal != "" && row.Terminal == pane.Terminal || row.Terminal == "" && row.Pane == pane.ID):
		return srcStart
	case pane.Label == agent:
		return srcLabel
	}
	return ""
}

// candidates is the agents a pane of p may run, sorted: its roles, its lanes' and loop's agents, the agents serve
// started or has lines for, and the labels of mine (so a hand-started pane is named).
func (a *App) candidates(p *proj, mine []Pane) []string {
	set := map[string]bool{p.lead: true, p.integrator: true}
	for _, l := range p.lanes {
		set[l.Agent] = true
	}
	for _, mt := range p.loop {
		set[mt.agent] = true
	}
	for agent := range p.starts {
		set[agent] = true
	}
	for agent := range p.briefs {
		set[agent] = true
	}
	for _, q := range p.msgs {
		set[q.msg.To] = true
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

// sameSession: herdr's session s (an id, or a transcript path <dir>/<id>.jsonl) is session id sid.
func sameSession(s, sid string) bool {
	return s != "" && sid != "" && (s == sid || strings.TrimSuffix(filepath.Base(s), filepath.Ext(s)) == sid)
}
