// Package identity is a project's identity table, its own package so a reader takes it without package app:
// which terminal each agent runs in, derived every pass from proofs (Reconcile), never a fact. Its maps are unexported,
// so only this package names them: package app reads it through WhoIs, Lookup, Start and StartedAgents. herdr's agent
// name is never read (herdr drops it at times; nothing renames it back).
//
// No lock: a Registry is part of package app's per-project state and is used under App.mu like the rest of it.
// It imports only the leaf port, never package app.
package identity

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"obor/internal/app/port"
)

// The proofs that an agent runs in a pane, strongest first (rank).
const (
	srcSession = "session" // the pane's agent_session is the agent's recorded session (app's sessions.go; its start's)
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

// Registry is one project's identity table (app's proj.reg): starts, serve's own starts by agent (folded,
// RecordStart); owners, by terminal key the agent its pane runs, and agents its inverse; ambiguous, the agents two or
// more panes claim with equal proof (their pane ids); strays, the labels logged as not their agent's pane;
// herdrSession, the herdr session last logged as differing from an agent's recorded one, by agent (logged once). All
// but starts and herdrSession are rebuilt by Reconcile. It has no lock of its own: under App.mu.
type Registry struct {
	starts       map[string]StartRow
	owners       map[string]owner
	agents       map[string]string
	ambiguous    map[string][]string
	strays       map[string]bool
	herdrSession map[string]string
}

// New is an empty table.
func New() Registry {
	return Registry{starts: map[string]StartRow{}, herdrSession: map[string]string{}}
}

// RecordStart records serve's start of agent in row's pane (app's foldStart): a row without a terminal keeps the one
// known for the same pane; another agent's row on the same pane or terminal goes (the start replaced what ran there).
func (r *Registry) RecordStart(agent string, row StartRow) {
	if old := r.starts[agent]; row.Terminal == "" && old.Pane == row.Pane {
		row.Terminal = old.Terminal
	}
	for other, old := range r.starts {
		if other != agent && (old.Pane == row.Pane || old.Terminal != "" && old.Terminal == row.Terminal) {
			delete(r.starts, other)
		}
	}
	r.starts[agent] = row
}

// Start is serve's own start row of agent (ok false: none).
func (r *Registry) Start(agent string) (StartRow, bool) {
	row, ok := r.starts[agent]
	return row, ok
}

// StartedAgents is the agents with a start row, sorted.
func (r *Registry) StartedAgents() []string {
	out := make([]string, 0, len(r.starts))
	for agent := range r.starts {
		out = append(out, agent)
	}
	sort.Strings(out)
	return out
}

// StartRow is serve's own start of an agent (agent.start's or start.<job>'s result, app's foldStart): the pane it
// placed the agent in, that pane's terminal id ("": none known yet), the kind, the session, the verdicts folded at its
// start ("a verdict came since its start", rolestop.go) and Ref, the model ref an agent.start ran: its attempt's
// dispatch's (dispatchData.Type, read at the fold, not stored again; "": start.<job>'s, or unknown), and Config, the
// config revision the start was launched under (startOut.Config; "": a start.<job>, which names none), what a keep
// lane's reuse checks (startFits), and Task, the task key of the record it was folded from: the nightly prune keeps that
// task whole while the row is its agent's (deleting the record would drop the agent's identity proof).
type StartRow struct {
	Pane, Terminal, Kind, Session, Ref, Config, Task string
	Verdicts                                         int
}

// owner is the agent a terminal runs by the last Reconcile, its proof and the pane id it was seen at.
type owner struct {
	agent, source, pane string
}

// TermOf is pane's key in the table: herdr's terminal id (kept across pane.move), else its pane id.
func TermOf(pane port.Pane) string {
	if pane.Terminal != "" {
		return pane.Terminal
	}
	return "pane " + pane.ID
}

// Entry is what the table holds of an agent (Lookup): Panes, the panes that claim it with equal proof (ambiguous,
// sorted); else Term, the terminal key it owns ("": none), the pane id it was seen at and the proof that holds it.
type Entry struct {
	Panes              []string
	Term, Pane, Source string
}

// Lookup is the table's entry of agent: the one query behind app's whereIs.
func (r *Registry) Lookup(agent string) Entry {
	if ids := r.ambiguous[agent]; len(ids) > 0 {
		return Entry{Panes: ids}
	}
	t, ok := r.agents[agent]
	if !ok {
		return Entry{}
	}
	ow := r.owners[t]
	return Entry{Term: t, Pane: ow.pane, Source: ow.source}
}

// WhoIs is the agent pane runs by the table: the only pane → agent lookup.
func (r *Registry) WhoIs(pane port.Pane) (string, bool) {
	ow, ok := r.owners[TermOf(pane)]
	return ow.agent, ok
}

// NotIdentified is the line that names an ambiguity: "panes w1:p1 and w1:p3 run pj-o-01 (close or relabel one)".
func NotIdentified(agent string, panes []string) string {
	return "not identified: panes " + strings.Join(panes, " and ") + " run " + agent + " (close or relabel one)"
}

// claim is one proof that agent runs in pane (terminal key term).
type claim struct {
	agent, term, src string
	pane             port.Pane
}

// Claims is the strongest proof of each agent and terminal (app's reconcile collects them, Reconcile accepts them).
type Claims map[[2]string]claim

// Add records that src proves agent runs in pane, kept when it is stronger than the proof held for that agent and
// terminal.
func (cs Claims) Add(agent string, pane port.Pane, src string) {
	k := [2]string{agent, TermOf(pane)}
	if c, ok := cs[k]; !ok || rank(src) > rank(c.src) {
		cs[k] = claim{agent: agent, term: k[1], src: src, pane: pane}
	}
}

// Reconcile rebuilds the table from best, the strongest proof of each agent and terminal, accepted strongest first;
// log takes each line it logs; named is whether the config names an agent. A claim is accepted when
// neither its agent nor its terminal is taken; an agent whose strongest free claims name two or more terminals is
// ambiguous and takes none; two agents with equal proof of one terminal take it neither, and no weaker claim takes it
// in this pass (it is owned again once the tie ends). The result does
// not depend on any iteration order. A start row whose terminal another agent now owns is retired, as is the row of an
// agent the config does not name (that retirement is silent: nothing acts on it and it repeats at every
// start). Each change is logged once.
func (r *Registry) Reconcile(best Claims, named func(string) bool, log func(string)) {
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
	r.logOwners(owners, agents, ambiguous, best, log)
	r.owners, r.agents, r.ambiguous = owners, agents, ambiguous
	for agent, row := range r.starts {
		if !named(agent) {
			delete(r.starts, agent)
			continue
		}
		t := row.Terminal
		if t == "" {
			t = "pane " + row.Pane
		}
		if ow, ok := owners[t]; ok && ow.agent != agent {
			delete(r.starts, agent)
			log(fmt.Sprintf("%s's start row retired: pane %s runs %s now (%s)", agent, ow.pane, ow.agent, ow.source))
		}
	}
}

// logOwners logs (to log) what changed in the table: an agent identified (or at another pane), no longer identified,
// an ambiguity begun, and a label on a pane its agent is not proven in (each once).
func (r *Registry) logOwners(owners map[string]owner, agents map[string]string, ambiguous map[string][]string,
	best Claims, log func(string)) {
	var lines []string
	for agent, t := range agents {
		ow := owners[t]
		if old, ok := r.agents[agent]; !ok || old != t || r.owners[old].pane != ow.pane {
			lines = append(lines, "agent "+agent+" identified at pane "+ow.pane+" ("+ow.source+")")
		}
	}
	for agent := range r.agents {
		if _, ok := agents[agent]; !ok && len(ambiguous[agent]) == 0 {
			lines = append(lines, "agent "+agent+" no longer identified")
		}
	}
	for agent, ids := range ambiguous {
		if !slices.Equal(r.ambiguous[agent], ids) {
			lines = append(lines, "agent "+agent+" "+NotIdentified(agent, ids))
		}
	}
	strays := map[string]bool{}
	for _, c := range best {
		if t, ok := agents[c.agent]; ok && c.src == srcLabel && t != c.term {
			k := c.pane.ID + " " + c.agent
			strays[k] = true
			if !r.strays[k] {
				lines = append(lines, "pane "+c.pane.ID+" labelled "+c.agent+" too, not its "+owners[t].source)
			}
		}
	}
	r.strays = strays
	sort.Strings(lines)
	for _, l := range lines {
		log(l)
	}
}

// CrossCheck logs, once per value, that herdr reports another session than agent's recorded sid for pane, the pane
// agent's start or label names (log takes the line): herdr's agent_session is a cross-check only, never
// adopted. A start row of an agent the config does not name (named false) is no cross-check: it is being retired.
func (r *Registry) CrossCheck(agent, sid string, pane port.Pane, named bool, log func(string)) {
	row, started := r.Start(agent)
	ours := pane.Label == agent || named && started && (row.Terminal != "" && row.Terminal == pane.Terminal || row.Pane == pane.ID)
	if sid == "" || pane.Session == "" || !ours || SameSession(pane.Session, sid) || r.herdrSession[agent] == pane.Session {
		return
	}
	r.herdrSession[agent] = pane.Session
	log(fmt.Sprintf("herdr reports session %s for %s at pane %s; its recorded session is %s (kept)", pane.Session,
		agent, pane.ID, sid))
}

// Proof is the strongest proof that agent runs in pane ("": none); sid is agent's recorded session; named is whether
// the config names agent: a start row or a recorded session of an agent it does not name is no proof.
func (r *Registry) Proof(agent, sid string, pane port.Pane, named bool) string {
	row, started := r.Start(agent)
	switch {
	case named && (SameSession(pane.Session, sid) || started && SameSession(pane.Session, row.Session)):
		return srcSession
	case named && started && (row.Terminal != "" && row.Terminal == pane.Terminal || row.Terminal == "" && row.Pane == pane.ID):
		return srcStart
	case pane.Label == agent:
		return srcLabel
	}
	return ""
}

// SameSession: herdr's session s (an id, or a transcript path <dir>/<id>.jsonl) is session id sid.
func SameSession(s, sid string) bool {
	return s != "" && sid != "" && (s == sid || strings.TrimSuffix(filepath.Base(s), filepath.Ext(s)) == sid)
}
