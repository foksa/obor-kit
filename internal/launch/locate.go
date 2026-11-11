package launch

// locate.go: the one decision before a start or a stop of an agent touches a pane (Locate). Serve's identity table
// answers which pane runs the agent (the where request: session, start row or label); the pane list read now, the
// terminal's holder of the agent's name (port.PaneControl.Named, the weakest claim) and the config add what serve
// cannot see. The answer is where the agent runs (a start there is a restart in place, any other target is refused),
// that it is ambiguous (refused), or that nothing proves it (free: its home pane, the one labelled with its name at its
// shell, or the caller's target). Serve cannot tell (not running, not connected to the terminal, not serving the
// project): labels and the terminal's name alone, and a start that adds an agent goes only while every other live
// agent pane of the project is labelled for another agent the config names. The decision (decide) is a pure function;
// Locate is its I/O, under the agent's restart lock.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"obor/internal/app/identity"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/project"
)

// whereWait bounds Locate's where request to serve (as restart's restart_job request).
const whereWait = 3 * time.Second

// proofName is the proof of a pane whose live agent the terminal holds under the agent's name; serve's proofs are its
// table's sources (session, start, label).
const proofName = "name"

// Located is Locate's answer, the core's type (port.Starter), so setup asks Locate without importing launch.
type Located = port.Located

// locateIn is what decide decides from.
type locateIn struct {
	agent  string
	target string // the start's or stop's pane (--pane, a dispatch's pane); "": none named
	adds   bool   // a start (it adds an agent where none runs); false: a stop
	root   string // the project's main checkout (a foreign holder's line)
	panes  []port.Pane
	// serve is serve's where answer; nil: serve cannot tell, down says why ("obor serve is not running").
	serve *wire.WhereReply
	down  string
	// holder is the pane whose live agent the terminal holds under agent's name ("": none); holderErr: that lookup
	// failed.
	holder    string
	holderErr error
	ours      func(p port.Pane) bool // p's cwd is this project's, by the agent's ownership rule (inProject's)
	project   func(p port.Pane) bool // p's cwd is inside this project (any agent's)
	named     func(label string) bool
}

// decide is Locate's decision, with no I/O. Serve's answer counts only for a pane that runs an agent in the list read
// now. The agent runs in Q: serve identifies Q; else one live pane of the project labelled with its name that serve
// gives to no other agent; else the terminal holds its name in a live pane of the project (a holder whose cwd is
// another project's is that project's namesake, refused as a label there is). Two of those disagreeing is ambiguous. A
// target other than Q is refused, naming Q, its proof and the command that works. Free: the target, else the home pane;
// a target that is not the home while the home exists, that serve gives to another agent or that is labelled for
// another agent is refused. A refusal is port.Refused (a final one: nothing was touched).
func decide(in locateIn) (Located, error) {
	x := in.agent
	if in.holderErr != nil {
		return Located{}, port.Refused{Line: fmt.Sprintf("cannot tell whether a live agent holds the name %s: the "+
			"terminal's lookup failed (%v): nothing touched", x, in.holderErr)}
	}
	byID := map[string]port.Pane{}
	for _, p := range in.panes {
		byID[p.ID] = p
	}
	live := func(id string) bool { return byID[id].Agent != "" }
	runs := func(id string) string { // the agent serve's table gives pane id
		if in.serve == nil {
			return ""
		}
		return in.serve.Runs[id]
	}
	var labelled []string
	var foreign port.Pane
	for _, p := range in.panes {
		if p.Label != x || p.Agent == "" {
			continue
		}
		if r := runs(p.ID); r != "" && r != x {
			continue
		}
		if !in.ours(p) {
			if foreign.ID == "" {
				foreign = p
			}
			continue
		}
		labelled = append(labelled, p.ID)
	}
	q, proof := "", ""
	if s := in.serve; s != nil {
		switch s.State {
		case wire.WhereIdentified:
			if live(s.Pane) {
				q, proof = s.Pane, s.Source
			}
		case wire.WhereAmbiguous:
			if slices.ContainsFunc(s.Panes, live) {
				return Located{}, port.Refused{Line: s.Why}
			}
		}
	}
	if q == "" {
		switch len(labelled) {
		case 0:
		case 1:
			q, proof = labelled[0], "label"
		default:
			slices.Sort(labelled)
			return Located{}, port.Refused{Line: identity.NotIdentified(x, labelled)}
		}
	}
	if h := in.holder; h != "" && live(h) {
		switch {
		case !in.ours(byID[h]) && h != in.target: // another project's agent of that name: no proof, the foreign line
			// below (a target outside the project is resolve's to refuse, with the pane's evidence)
			if foreign.ID == "" {
				foreign = byID[h]
			}
		case q == "":
			q, proof = h, proofName
		case h != q:
			both := []string{q, h}
			slices.Sort(both)
			return Located{}, port.Refused{Line: identity.NotIdentified(x, both)}
		}
	}
	if q != "" {
		if in.target != "" && in.target != q {
			return Located{}, port.Refused{Line: runsLine(x, q, proof, byID[q].Label)}
		}
		return Located{Pane: q, Runs: true, Proof: proof, Label: byID[q].Label}, nil
	}
	if foreign.ID != "" {
		return Located{}, port.Refused{Line: fmt.Sprintf("%s runs in pane %s with cwd %s, outside %s (another project's "+
			"agent of that name? the config's prefix names every agent: obor setup rename changes it)", x, foreign.ID,
			foreign.Cwd, in.root)}
	}
	home := homeOf(in)
	target := in.target
	switch {
	case target == "":
		target = home
	case home != "" && home != target:
		return Located{}, port.Refused{Line: fmt.Sprintf("pane %s is already labelled %s (obor restart %s there)", home, x, x)}
	}
	if target != "" {
		if r := runs(target); r != "" && r != x {
			return Located{}, port.Refused{Line: fmt.Sprintf("pane %s runs %s, not %s", target, r, x)}
		}
		if l := byID[target].Label; l != "" && l != x {
			return Located{}, port.Refused{Line: fmt.Sprintf("pane %s is labelled %s, not %s", target, l, x)}
		}
	}
	if in.serve == nil && in.adds {
		for _, p := range in.panes {
			if p.ID == target || p.Agent == "" || !in.project(p) || p.Label != "" && p.Label != x && in.named(p.Label) {
				continue
			}
			label := p.Label
			if label == "" {
				label = "none"
			}
			return Located{}, port.Refused{Line: fmt.Sprintf("cannot tell whether the %s in pane %s (label %s) is %s: %s. "+
				"obor serve restart, then retry; or obor restart %s --pane %s to replace it", p.Agent, p.ID, label, x,
				in.down, x, p.ID)}
		}
	}
	return Located{Pane: target, Label: byID[target].Label}, nil
}

// homeOf is the agent's home: a pane of the project labelled with its name at its shell, one whose cwd is known first.
func homeOf(in locateIn) string {
	home := ""
	for _, p := range in.panes {
		if p.Label != in.agent || p.Agent != "" || !in.ours(p) {
			continue
		}
		if p.Cwd != "" {
			return p.ID
		}
		if home == "" {
			home = p.ID
		}
	}
	return home
}

// runsLine is the refusal of a start or stop elsewhere while the agent runs in pane (by proof, its label label): the
// pane, its proof and the command that works there.
func runsLine(agent, pane, proof, label string) string {
	if label == "" {
		label = "none"
	}
	return fmt.Sprintf("%s already runs in pane %s (proof: %s; label %s): obor restart %s --pane %s restarts it there, "+
		"or quit it there first", agent, pane, proof, label, agent, pane)
}

// Locate is the decision before a start (adds) or a stop of agent touches a pane (decide), pane the caller's target
// ("": none). It takes the agent's restart lock (RestartLock: the lane the config gives it, else the agent's) and
// returns holding it: release, after the caller's last step; a refusal or an error releases it here. The where request
// is Where (nil: serve's socket at Cfg.Root, within whereWait); the terminal's holder of the name is H.Named.
func (l *Launcher) Locate(ctx context.Context, agent, pane string, adds bool) (Located, func(), error) {
	var info config.AgentInfo
	known := false
	if l.Cfg != nil {
		info, known = l.Cfg.AgentLookup(agent, "")
	}
	end, err := l.begin(ctx, agent, info.Lane)
	if err != nil {
		return Located{}, nil, err
	}
	loc, err := l.locate(ctx, agent, pane, adds, info, known)
	if err != nil {
		end()
		return Located{}, nil, err
	}
	switch {
	case loc.Runs:
		l.say("pane %s (%s runs there: %s)", loc.Pane, agent, loc.Proof)
	case loc.Pane != "" && loc.Pane == pane:
		l.say("pane %s (from --pane)", loc.Pane)
	case loc.Pane != "":
		l.say("pane %s (from label)", loc.Pane)
	}
	return loc, end, nil
}

// locate is Locate's I/O: the pane list, serve's answer, the terminal's holder of the name; then decide.
func (l *Launcher) locate(ctx context.Context, agent, pane string, adds bool, info config.AgentInfo, known bool) (Located, error) {
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return Located{}, project.Failf("terminal pane list failed (is the terminal running?)")
	}
	in := locateIn{agent: agent, target: pane, adds: adds, panes: panes,
		ours: func(port.Pane) bool { return true }, project: func(port.Pane) bool { return false },
		named: func(string) bool { return false }}
	ask := []string{}
	if pane != "" {
		ask = append(ask, pane)
	}
	for _, p := range panes {
		if p.Label == agent && p.ID != pane {
			ask = append(ask, p.ID)
		}
	}
	in.serve, in.down = l.where(ctx, agent, ask)
	if l.Cfg != nil {
		in.root = l.Cfg.Root
		in.ours = func(p port.Pane) bool { return !known || l.ours(agent, info, p.Cwd) }
		in.project = func(p port.Pane) bool { return p.Cwd != "" && l.ours("", config.AgentInfo{}, p.Cwd) }
		in.named = func(label string) bool { _, ok := l.Cfg.AgentLookup(label, ""); return ok }
	}
	switch h, err := l.H.Named(ctx, agent); {
	case err == nil:
		in.holder = h.ID
	case !port.NotFound(err):
		in.holderErr = err
	}
	return decide(in)
}

// ours: a pane with cwd is the project's, by inProject's rule: no cwd known, or its cwd in the main checkout, a
// lanes.dir worktree, the agent's configured cwd or another worktree of this repository.
func (l *Launcher) ours(agent string, info config.AgentInfo, cwd string) bool {
	if cwd == "" || project.LabelOurs(l.Cfg, agent, cwd, "") ||
		info.Cwd != "" && project.LabelOurs(l.Cfg, agent, cwd, info.Cwd) {
		return true
	}
	r, err := project.Find(cwd)
	return err == nil && r.Root == project.Physical(l.Cfg.Root)
}

// where is serve's where answer for agent and panes (Where, else the request to Cfg.Root's serve); nil and why when
// serve cannot tell: no project, not running, no answer, not connected to the terminal.
func (l *Launcher) where(ctx context.Context, agent string, panes []string) (*wire.WhereReply, string) {
	if l.Cfg == nil {
		return nil, "no obor project"
	}
	ask := l.Where
	if ask == nil {
		ask = func(ctx context.Context, req wire.WhereReq) (wire.WhereReply, error) {
			ctx, cancel := context.WithTimeout(ctx, whereWait)
			defer cancel()
			var r wire.WhereReply
			raw, err := wire.Once(ctx, wire.SocketPath(l.Cfg.Root), &req)
			if err == nil {
				err = json.Unmarshal([]byte(raw), &r)
			}
			return r, err
		}
	}
	r, err := ask(ctx, wire.WhereReq{Envelope: wire.Envelope{Root: l.Cfg.Root, CallID: wire.NewCallID()}, Name: agent,
		Panes: panes})
	switch {
	case err != nil && wire.Absent(err):
		return nil, "obor serve is not running"
	case err != nil:
		return nil, fmt.Sprintf("obor serve did not answer (%v)", err)
	case r.State == wire.WhereNotConnected:
		return nil, "obor serve is not connected to the terminal"
	}
	return &r, ""
}
