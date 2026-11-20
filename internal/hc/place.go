package hc

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"obor/internal/app/port"
	"obor/internal/herdrapp"
	"obor/internal/project"
)

// place.go: the part of port.TerminalSetup's Place over hc and herdr-app. The layout is herdr's (internal/herdr/setup.go): a space
// named after the project in the project root's folder, in it a `lead` tab for the lead alone, `lanes` tabs for lanes
// and `roles` tabs for roles, MaxPanes terminals to a tab. herdr-app shows a terminal in the tab whose ids it carries
// as labels (docs/research/hc-probes.md P7). Two APIs meet here: herdr-app's `space.new` and `tab.new` start the new
// tab's first terminal on hc (labelled with the tab's ids), and that terminal is the placed pane (Place labels it and
// keeps it); a pane into a tab that is there is a terminal.run hc makes (a login shell, kept) with the tab's labels. A
// tab's terminals are the live hc terminals labelled with its id, the app's own included: herdr-app's `ls` lists
// tabs, not their terminals.

// Place makes a terminal for p in the tab the layout rules give it and returns its address (the pane id): p.Group →
// a new tab of that name; p.In → that tab (refused when it holds the lead, and for a lead when it holds any pane;
// full: a new tab of p's kind); none → the first tab of p's kind with room, else a new one. A new tab's pane is the
// terminal herdr-app started in it (its cwd the project root: launch's env line changes to p.Cwd and sets the env).
// With no window (herdr-app closed, or no App) a placement with Group or In is refused; any other is made hidden, with
// the obor labels only, and Say says so: the next Place (or Adopt) places it once the window answers. The app is
// never started.
func (cl *Client) Place(ctx context.Context, p port.Placement) (string, error) {
	recs, err := cl.terminals(ctx)
	if err != nil {
		return "", err
	}
	if cl.App != nil {
		if _, err := cl.adopt(ctx, recs, p.Project, p.Say); err != nil && !port.NotRunning(err) {
			say(p, "terminals made while herdr-app was closed stay hidden: %v", err)
		}
	}
	labels := map[string]string{LabelProject: p.Project.Name, LabelPlace: string(p.Kind)}
	var sp herdrapp.Space
	var tab herdrapp.Tab
	var made bool
	if cl.App == nil {
		err = &herdrapp.Error{Op: "ls", Msg: "no herdr-app client"}
	} else {
		sp, tab, made, err = cl.spot(ctx, recs, p)
	}
	switch {
	case err == nil:
		for k, v := range herdrapp.Labels(sp.ID, tab.ID) {
			labels[k] = v
		}
	case (cl.App == nil || port.NotRunning(err)) && (p.Group != "" || p.In != ""):
		return "", project.Failf("herdr-app is not running: open it, or place without --tab/--new-tab")
	case cl.App == nil || port.NotRunning(err):
		say(p, "herdr-app is not running: the terminal is made hidden and is placed in the %s tab once the window is open",
			kindLabel(p.Kind))
	default:
		return "", err
	}
	if made {
		addr, err := cl.take(ctx, recs, tab.ID, labels)
		if err != nil {
			return "", err
		}
		if addr != "" {
			say(p, "tab %s: terminal %s", tab.Name, addr)
			return addr, nil
		}
	}
	addr, err := cl.run(ctx, p, labels)
	if err != nil {
		return "", err
	}
	if tab.ID != "" {
		say(p, "tab %s: terminal %s", tab.Name, addr)
	}
	return addr, nil
}

// Adopt places every live terminal of the project that has no herdr.space label (one made while the window was
// closed) by the same rules as Place, one after the other; it returns how many it placed. A closed window is a
// port.NotRunning error with nothing placed. A placed terminal is never moved again.
func (cl *Client) Adopt(ctx context.Context, pr port.PlaceProject) (int, error) {
	if cl.App == nil {
		return 0, nil
	}
	recs, err := cl.terminals(ctx)
	if err != nil {
		return 0, err
	}
	return cl.adopt(ctx, recs, pr, nil)
}

func (cl *Client) adopt(ctx context.Context, recs []Record, pr port.PlaceProject, sayf func(string)) (int, error) {
	n := 0
	for i, r := range recs {
		if r.Labels[LabelProject] != pr.Name || r.Labels[herdrapp.LabelSpace] != "" {
			continue
		}
		p := port.Placement{Kind: placeKind(r, pr), Project: pr, Say: sayf}
		sp, tab, made, err := cl.spot(ctx, recs, p)
		if err != nil {
			return n, err
		}
		l := herdrapp.Labels(sp.ID, tab.ID)
		if err := cl.label(ctx, r.Address, l); err != nil {
			return n, err
		}
		if made { // the adopted terminal is the tab's pane: the shell the app started in it goes
			extra, err := cl.appMade(ctx, recs, tab.ID)
			if err != nil {
				return n, err
			}
			for _, e := range extra {
				if err := cl.call(ctx, nil, "terminal.kill", map[string]any{"terminal": e.Address}); err != nil {
					return n, err
				}
			}
		}
		for k, v := range l {
			recs[i].Labels[k] = v
		}
		say(p, "terminal %s placed in tab %s", r.Address, tab.Name)
		n++
	}
	return n, nil
}

// terminals is every live terminal, labels never nil.
func (cl *Client) terminals(ctx context.Context) ([]Record, error) {
	var r struct {
		Terminals []Record `json:"terminals"`
	}
	if err := cl.call(ctx, &r, "terminal.list", nil); err != nil {
		return nil, err
	}
	var out []Record
	for _, t := range r.Terminals {
		if t.State != "live" {
			continue
		}
		if t.Labels == nil {
			t.Labels = map[string]string{}
		}
		out = append(out, t)
	}
	return out, nil
}

// run is terminal.run of a login shell for p: cwd, env, kept, with no parent that ends it.
func (cl *Client) run(ctx context.Context, p port.Placement, labels map[string]string) (string, error) {
	env := map[string]string{}
	for _, kv := range p.Env {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	params := map[string]any{"keep": true, "no_parent": true, "labels": labels, "env": env}
	if p.Cwd != "" {
		params["cwd"] = p.Cwd
	}
	var r struct {
		Address string `json:"address"`
	}
	if err := cl.call(ctx, &r, "terminal.run", params); err != nil {
		return "", err
	}
	if r.Address == "" {
		return "", &Error{Op: "terminal.run", Msg: "no address in the answer"}
	}
	return r.Address, nil
}

// take makes the terminal herdr-app started in the tab it just made (one not among before) the placed pane: labels
// set, kept when the app did not keep it. "" when the app started none there (terminal.run makes the pane then).
func (cl *Client) take(ctx context.Context, before []Record, tab string, labels map[string]string) (string, error) {
	made, err := cl.appMade(ctx, before, tab)
	if err != nil || len(made) == 0 {
		return "", err
	}
	r := made[0]
	if err := cl.label(ctx, r.Address, labels); err != nil {
		return "", err
	}
	if !r.Keep {
		if err := cl.call(ctx, nil, "terminal.keep", map[string]any{"terminal": r.Address, "keep": true}); err != nil {
			return "", err
		}
	}
	return r.Address, nil
}

// appMade is the live terminals in tab (by its herdr.tab label) that before did not list: what herdr-app started in
// a tab it made since.
func (cl *Client) appMade(ctx context.Context, before []Record, tab string) ([]Record, error) {
	now, err := cl.terminals(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range inTab(now, tab) {
		if !slices.ContainsFunc(before, func(b Record) bool { return b.Address == r.Address }) {
			out = append(out, r)
		}
	}
	return out, nil
}

// spot is where p goes: the project's space and the tab in it, and whether this call made that tab (space.new's
// first tab, or tab.new), its first terminal herdr-app's.
func (cl *Client) spot(ctx context.Context, recs []Record, p port.Placement) (herdrapp.Space, herdrapp.Tab, bool, error) {
	sp, tab, err := cl.spotTab(ctx, recs, p)
	return sp, tab, err == nil && tab.Made, err
}

func (cl *Client) spotTab(ctx context.Context, recs []Record, p port.Placement) (herdrapp.Space, herdrapp.Tab, error) {
	pr := p.Project
	sp, err := cl.App.EnsureSpace(ctx, pr.Name, pr.Root)
	if err != nil {
		return herdrapp.Space{}, herdrapp.Tab{}, err
	}
	newSpace := sp.Made
	if sp, err = cl.space(ctx, sp.ID); err != nil {
		return herdrapp.Space{}, herdrapp.Tab{}, err
	}
	// first is the tab space.new made with the space, its terminal herdr-app's: the first new tab is that one renamed.
	first := func(name string) (herdrapp.Tab, error) {
		tab, err := cl.App.RenameTab(ctx, sp.Tabs[0].ID, name)
		tab.Made = true
		return tab, err
	}
	fresh := newSpace && len(sp.Tabs) == 1
	switch {
	case p.Group != "" && fresh:
		tab, err := first(p.Group)
		return sp, tab, err
	case p.Group != "":
		tab, err := cl.App.NewTab(ctx, sp.ID, p.Group, pr.Root)
		return sp, tab, err
	case p.In != "":
		for _, t := range sp.Tabs {
			if t.ID != p.In && t.Name != p.In {
				continue
			}
			held := inTab(recs, t.ID)
			if p.Kind == port.KindLead && len(held) > 0 {
				return sp, herdrapp.Tab{}, project.Failf("tab %s holds %d pane(s): the lead keeps its tab alone: pass "+
					"--new-tab <label>", p.In, len(held))
			}
			for _, r := range held {
				if r.Labels[LabelProject] == pr.Name && placeKind(r, pr) == port.KindLead {
					return sp, herdrapp.Tab{}, project.Failf("tab %s holds the lead's pane, which keeps its tab alone: pass "+
						"--new-tab <label> (or --pane <id>)", p.In)
				}
			}
			if len(held) >= pr.MaxPanes {
				say(p, "tab %s is full (%d of %d panes)", p.In, len(held), pr.MaxPanes)
				break
			}
			return sp, t, nil
		}
		if !hasTab(sp, p.In) {
			return sp, herdrapp.Tab{}, project.Failf("tab %s is not in the %s space", p.In, sp.Name)
		}
	default:
		if t, ok := kindTab(sp, recs, p); ok {
			return sp, t, nil
		}
	}
	label := kindLabel(p.Kind)
	if fresh {
		tab, err := first(label)
		return sp, tab, err
	}
	var tab herdrapp.Tab
	if hasName(sp, label) {
		tab, err = cl.App.NewTab(ctx, sp.ID, label, pr.Root)
	} else {
		tab, err = cl.App.EnsureTab(ctx, sp.ID, label, pr.Root)
	}
	return sp, tab, err
}

// space is the window's space id, tabs included.
func (cl *Client) space(ctx context.Context, id string) (herdrapp.Space, error) {
	spaces, err := cl.App.Spaces(ctx)
	if err != nil {
		return herdrapp.Space{}, err
	}
	for _, s := range spaces {
		if s.ID == id {
			return s, nil
		}
	}
	return herdrapp.Space{}, project.Failf("herdr-app space %s is gone", id)
}

// kindTab is the first tab of sp, in the window's order, a placement of kind p.Kind with no place named goes to: no
// lead's terminal in it (the lead keeps its tab alone), every terminal of the project in it of that kind (and, with
// none, only a tab named for the kind: a tab made for it), and fewer than MaxPanes terminals. A lead never shares.
func kindTab(sp herdrapp.Space, recs []Record, p port.Placement) (herdrapp.Tab, bool) {
	pr, k := p.Project, p.Kind
	for _, t := range sp.Tabs {
		held := inTab(recs, t.ID)
		mine, lead := 0, false
		other := false
		for _, r := range held {
			if r.Labels[LabelProject] != pr.Name {
				continue
			}
			mine++
			lead = lead || placeKind(r, pr) == port.KindLead
			other = other || placeKind(r, pr) != k
		}
		switch {
		case len(held) >= pr.MaxPanes, lead, other:
		case k == port.KindLead && mine > 0:
		case mine == 0 && t.Name != kindLabel(k):
		default:
			return t, true
		}
	}
	return herdrapp.Tab{}, false
}

// inTab is the live terminals carrying tab as their herdr.tab label.
func inTab(recs []Record, tab string) []Record {
	var out []Record
	for _, r := range recs {
		if r.Labels[herdrapp.LabelTab] == tab {
			out = append(out, r)
		}
	}
	return out
}

func hasTab(sp herdrapp.Space, idOrName string) bool {
	for _, t := range sp.Tabs {
		if t.ID == idOrName || t.Name == idOrName {
			return true
		}
	}
	return false
}

func hasName(sp herdrapp.Space, name string) bool {
	for _, t := range sp.Tabs {
		if t.Name == name {
			return true
		}
	}
	return false
}

// placeKind is what r is to placement: the kind Place labelled it with, else the kind of its agent label.
func placeKind(r Record, pr port.PlaceProject) port.PaneKind {
	if k := r.Labels[LabelPlace]; k != "" {
		return port.PaneKind(k)
	}
	if pr.KindOf != nil {
		return pr.KindOf(r.Labels[LabelAgent])
	}
	return ""
}

// kindLabel is the name of a new tab for a terminal of kind k: "lanes", "roles", "lead" (herdr's).
func kindLabel(k port.PaneKind) string {
	switch k {
	case port.KindLane:
		return "lanes"
	case port.KindRole:
		return "roles"
	}
	return string(k)
}

func say(p port.Placement, format string, a ...any) {
	if p.Say != nil {
		p.Say(fmt.Sprintf(format, a...))
	}
}
