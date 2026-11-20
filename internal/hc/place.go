package hc

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/layout"
	"obor/internal/app/port"
	"obor/internal/herdrapp"
	"obor/internal/project"
)

// place.go: the part of port.TerminalSetup's Place over hc and herdr-app. The layout is herdr's (internal/herdr/setup.go): a space
// named after the project in the project root's folder, in it a `lead` tab for the lead alone, `lanes` tabs for lanes
// and `roles` tabs for roles, MaxPanes terminals to a tab. herdr-app shows a terminal in the tab whose ids it carries
// as labels (docs/research/hc-probes.md P7); a tab's terminals are the live hc terminals labelled with its id, the
// app's own and a person's included (herdr-app's `ls` lists tabs, not their terminals).
//
// One decision places a terminal, for Place and for the adoption of a hidden terminal alike: internal/app/layout's
// (the rules are there). hc gathers the facts (the space's tabs from herdr-app, each tab's terminals and their labels
// from hc), asks it (spot) and carries out the answer: the tab, or a new tab of the name it gives.
//
// A new tab is the one `space.new` made with the space when this call made the space (renamed), else `tab.new`'s;
// herdr-app starts its first terminal and prints that terminal's address, and the address is how obor knows the
// terminal is its own: Place makes it the placed pane, an adoption kills it (the adopted terminal takes the tab). A
// terminal obor did not make that way is never killed.

// readyBound bounds each wait of a terminal's readying: for its shell, then for its env line's mark; readyPoll is
// how often the terminal is read meanwhile.
const (
	readyBound = 10 * time.Second
	readyPoll  = 100 * time.Millisecond
)

// spot is where a placement goes: the space and the tab, Tab.Terminal the terminal herdr-app started in it when this
// call made the tab ("" for a tab that was there) and Folder the cwd that terminal started in.
type spot struct {
	Space  string
	Tab    herdrapp.Tab
	Folder string
}

// Place makes a terminal for p in the tab the rules give it (spot) and returns its address (the pane id). In a new
// tab the pane is the terminal herdr-app started there, labelled and kept; in a tab that was there it is a
// terminal.run hc makes (a login shell, kept, no parent) with p's cwd and env. Either is ready as Place returns: the
// app's terminal starts in p.Cwd (tab.new's folder; space.new's is the root) and, unless p.Starts (the start types
// it), gets p's env and cwd by the env line typed at its shell (project.EnvLine), its shell waited for first; a line
// that fails is Place's error. With no window (herdr-app closed, or no App) a placement with Group or In is refused;
// any other is made hidden by terminal.run, with the obor labels only, and Say says so: the next Place (or Adopt)
// places it once the window answers. The app is never started.
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
	at, err := cl.spot(ctx, recs, p)
	switch {
	case err == nil:
	case closed(err) && (p.Group != "" || p.In != ""):
		return "", project.Failf("herdr-app is not running: open it, or place without --tab/--new-tab")
	case closed(err):
		say(p, "herdr-app is not running: the terminal is made hidden and is placed in the %s tab once the window is open",
			layout.Label(p.Kind))
		return cl.run(ctx, p, labels)
	default:
		return "", err
	}
	maps.Copy(labels, herdrapp.Labels(at.Space, at.Tab.ID))
	var addr string
	if at.Tab.Terminal != "" {
		addr, err = cl.take(ctx, at, labels, p)
	} else {
		addr, err = cl.run(ctx, p, labels)
	}
	if err != nil {
		return "", err
	}
	say(p, "tab %s: terminal %s", at.Tab.Name, addr)
	return addr, nil
}

// Adopt places every live terminal of the project that has no herdr.space label and has a kind (placeKind: one Place
// made while the window was closed) by the same rules as Place, one after the other; it returns how many it placed. A closed window is a
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

// adopt is Adopt over recs, whose labels it updates as it places. A terminal adopted into a tab this call made takes
// it: the terminal herdr-app started there (the address it printed) is killed, the tab's one terminal the adopted one.
func (cl *Client) adopt(ctx context.Context, recs []Record, pr port.PlaceProject, sayf func(string)) (int, error) {
	n := 0
	for i, r := range recs {
		if r.Labels[LabelProject] != pr.Name || r.Labels[herdrapp.LabelSpace] != "" {
			continue
		}
		p := port.Placement{Kind: placeKind(r, pr), Project: pr, Say: sayf}
		if p.Kind == "" {
			continue
		}
		at, err := cl.spot(ctx, recs, p)
		if err != nil {
			return n, err
		}
		l := herdrapp.Labels(at.Space, at.Tab.ID)
		if err := cl.label(ctx, r.Address, l); err != nil {
			return n, err
		}
		if at.Tab.Terminal != "" {
			if err := cl.call(ctx, nil, "terminal.kill", map[string]any{"terminal": at.Tab.Terminal}); err != nil {
				return n, err
			}
		}
		maps.Copy(recs[i].Labels, l)
		say(p, "terminal %s placed in tab %s", r.Address, at.Tab.Name)
		n++
	}
	return n, nil
}

// TakeLead makes the caller's terminal p.Pane the lead's home (port.TerminalSetup) when it qualifies (leadCaller).
// Taken: labelled as Place labels a lead's terminal (the project, the lead's kind, p.Agent) and kept; its tab named
// lead; its space named after the project.
func (cl *Client) TakeLead(ctx context.Context, p port.LeadPane) (port.LeadTaken, error) {
	c, why, err := cl.leadCaller(ctx, p)
	if err != nil || why != "" {
		return port.LeadTaken{Line: why}, err
	}
	labels := map[string]string{LabelProject: p.Project.Name, LabelPlace: string(port.KindLead), LabelAgent: p.Agent}
	if err := cl.label(ctx, p.Pane, labels); err != nil {
		return port.LeadTaken{}, err
	}
	if !c.me.Keep {
		if err := cl.call(ctx, nil, "terminal.keep", map[string]any{"terminal": p.Pane, "keep": true}); err != nil {
			return port.LeadTaken{}, err
		}
	}
	if _, err := cl.App.RenameTab(ctx, c.tab, layout.Label(port.KindLead)); err != nil {
		return port.LeadTaken{}, err
	}
	did := fmt.Sprintf("terminal %s labelled %s, tab %s named %s", p.Pane, p.Agent, c.tab, layout.Label(port.KindLead))
	if line, err := cl.nameSpace(ctx, c.space, p.Project.Name); err != nil {
		return port.LeadTaken{}, err
	} else if line != "" {
		did += ", " + line
	}
	return port.LeadTaken{Taken: true, Line: did}, nil
}

// LeadSpace makes the caller's space the project's before anything is placed (port.TerminalSetup) when the caller's
// terminal qualifies (leadCaller): named after the project, so Place's EnsureSpace finds it and puts the project's
// tabs there. The terminal and its tab are not touched.
func (cl *Client) LeadSpace(ctx context.Context, p port.LeadPane) (port.LeadTaken, error) {
	c, why, err := cl.leadCaller(ctx, p)
	if err != nil || why != "" {
		return port.LeadTaken{Line: why}, err
	}
	line, err := cl.nameSpace(ctx, c.space, p.Project.Name)
	if err != nil {
		return port.LeadTaken{}, err
	}
	return port.LeadTaken{Taken: true, Line: cmp.Or(line, fmt.Sprintf("space %s is the project's", c.space.Name))}, nil
}

// caller is the caller's terminal that qualifies as the lead's home: its record, its tab and its space.
type caller struct {
	me    Record
	tab   string
	space herdrapp.Space
}

// leadCaller is the caller's terminal p.Pane when it qualifies as the lead's home (layout.Home over the facts read
// here), else why it does not. herdr-app closed (or no App): not qualified, the tab cannot be named.
func (cl *Client) leadCaller(ctx context.Context, p port.LeadPane) (caller, string, error) {
	recs, err := cl.terminals(ctx)
	if err != nil {
		return caller{}, "", err
	}
	c := layout.Caller{Pane: p.Pane, Project: p.Project.Name, Grouped: true}
	var me Record
	for _, r := range recs {
		if r.Address == p.Pane {
			me, c.Found = r, true
		}
	}
	var spaces []herdrapp.Space
	down := ""
	if c.Found {
		tab := me.Labels[herdrapp.LabelTab]
		if me.Status != nil {
			c.Agent = cmp.Or(me.Status.App, me.Status.State)
		}
		if me.programRuns() && (p.Caller == 0 || me.Foreground.PID != p.Caller) {
			c.Program = cmp.Or(me.Foreground.Name, strconv.Itoa(me.Foreground.PID))
		}
		c.Tabbed = tab != ""
		if c.Tabbed {
			c.TabMates = len(inTab(recs, tab)) - 1
		}
		for _, r := range recs {
			if pr := r.Labels[LabelProject]; c.Foreign == "" && r.Labels[herdrapp.LabelSpace] == me.Labels[herdrapp.LabelSpace] &&
				pr != "" && pr != p.Project.Name {
				c.Foreign, c.ForeignOf = r.Address, pr
			}
		}
		if cl.App == nil {
			down = "no herdr-app client to name its tab"
		} else if spaces, err = cl.App.Spaces(ctx); port.NotRunning(err) {
			down = "herdr-app is not running: its tab cannot be named"
		} else if err != nil {
			return caller{}, "", err
		}
	}
	var mine herdrapp.Space
	if down == "" && c.Found && c.Tabbed {
		c.Grouped = false
		for _, s := range spaces {
			if s.Machine == "" && s.ID == me.Labels[herdrapp.LabelSpace] {
				mine, c.Grouped, c.Group = s, true, s.Name
			}
		}
		for _, s := range spaces {
			if s.Machine == "" && s.Name == p.Project.Name && s.ID != mine.ID {
				c.Other = true
			}
		}
	}
	if why := layout.Home(c); why != "" {
		return caller{}, why, nil
	} else if down != "" {
		return caller{}, down, nil
	}
	return caller{me: me, tab: me.Labels[herdrapp.LabelTab], space: mine}, "", nil
}

// nameSpace names space sp name unless it is named so: what it did ("" when nothing).
func (cl *Client) nameSpace(ctx context.Context, sp herdrapp.Space, name string) (string, error) {
	if sp.Name == name {
		return "", nil
	}
	if err := cl.App.NameSpace(ctx, sp.ID, name); err != nil {
		return "", err
	}
	return fmt.Sprintf("space %s named %s", sp.Name, name), nil
}

// errNoApp is spot's error for a client with no App: no window, as with herdr-app closed.
var errNoApp error = &herdrapp.Error{Op: "ls", Msg: "no herdr-app client"}

// closed: err says there is no window to lay out in (herdr-app not running, or no App).
func closed(err error) bool { return err == errNoApp || port.NotRunning(err) }

// spot is where p goes by layout's decision over the project's space (made when there is none) and its tabs: the tab
// the answer names, or a new one. Its refusals are project failures.
func (cl *Client) spot(ctx context.Context, recs []Record, p port.Placement) (spot, error) {
	if cl.App == nil {
		return spot{}, errNoApp
	}
	pr := p.Project
	sp, err := cl.App.EnsureSpace(ctx, pr.Name, pr.Root)
	if err != nil {
		return spot{}, err
	}
	ask := layout.Ask{Kind: p.Kind, Group: p.Group, In: p.In, Home: fmt.Sprintf("the %s space", sp.Name), MaxPanes: pr.MaxPanes}
	for _, t := range sp.Tabs {
		lt := layout.Tab{ID: t.ID, Name: t.Name}
		for _, r := range inTab(recs, t.ID) {
			// a terminal of another project or of nobody in the project's tab is a person's: it holds a place
			// and blocks no kind
			lt.Panes = append(lt.Panes, layout.Pane{Kind: placeKind(r, pr), Aside: r.Labels[LabelProject] != pr.Name})
		}
		ask.Tabs = append(ask.Tabs, lt)
	}
	ans := layout.Place(ask)
	if ans.Refused != "" {
		return spot{}, project.Failf("%s", ans.Refused)
	}
	if ans.Said != "" {
		say(p, "%s", ans.Said)
	}
	if ans.New == "" {
		t, _ := findTab(sp.Tabs, ans.Tab.ID)
		return spot{Space: sp.ID, Tab: t}, nil
	}
	if sp.First.ID != "" { // the space this call made: its one tab is the new tab, its terminal herdr-app's
		t, err := cl.App.RenameTab(ctx, sp.First.ID, ans.New)
		t.Terminal = sp.First.Terminal
		return spot{Space: sp.ID, Tab: t, Folder: sp.Folder}, err
	}
	folder := cmp.Or(p.Cwd, pr.Root)
	t, err := cl.App.NewTab(ctx, sp.ID, ans.New, folder)
	return spot{Space: sp.ID, Tab: t, Folder: folder}, err
}

// take makes the terminal herdr-app started in the tab at made (at.Tab.Terminal, the address it printed) p's pane:
// labelled, kept when the app did not keep it, and ready (ready).
func (cl *Client) take(ctx context.Context, at spot, labels map[string]string, p port.Placement) (string, error) {
	addr := at.Tab.Terminal
	r, err := cl.live(ctx, addr)
	if err != nil {
		return "", err
	}
	if err := cl.label(ctx, addr, labels); err != nil {
		return "", err
	}
	if !r.Keep {
		if err := cl.call(ctx, nil, "terminal.keep", map[string]any{"terminal": addr, "keep": true}); err != nil {
			return "", err
		}
	}
	if err := cl.ready(ctx, addr, at.Folder, p); err != nil {
		return "", err
	}
	return addr, nil
}

// ready gives the app's terminal addr, started in folder, p's cwd and env: once its shell holds the foreground, the
// env line (project.EnvLine) typed and awaited as launch's is (project.RunEnvLine). Nothing is typed with p.Starts
// (the start types the env line) or when there is nothing to set (no env, the terminal started in p.Cwd). A shell that
// does not come up, or a line that fails or shows no mark, is the error.
func (cl *Client) ready(ctx context.Context, addr, folder string, p port.Placement) error {
	if p.Starts || len(p.Env) == 0 && (p.Cwd == "" || p.Cwd == folder) {
		return nil
	}
	atShell := func() (bool, error) {
		r, err := cl.live(ctx, addr)
		return err == nil && r.Foreground != nil && !r.programRuns(), err
	}
	if ok, err := until(ctx, atShell); err != nil || !ok {
		return cmp.Or(err, error(&Error{Op: "terminal.get", Msg: fmt.Sprintf("the shell of %s did not come up in %s", addr, readyBound)}))
	}
	line := project.EnvLine(p.Cwd, p.Env)
	switch res, err := project.RunEnvLine(ctx, cl, addr, line, readyPoll, readyBound); {
	case err != nil:
		return err
	case res == project.EnvLineNoMark:
		return &Error{Op: "terminal.read", Msg: fmt.Sprintf("the env line in %s showed no result in %s", addr, readyBound)}
	case res == project.EnvLineFailed:
		return &Error{Op: "terminal.send", Msg: fmt.Sprintf("the env line in %s failed (cd %s and its env)", addr, p.Cwd)}
	}
	return nil
}

// until reads cond every readyPoll for at most readyBound: true once it holds; its error ends the wait.
func until(ctx context.Context, cond func() (bool, error)) (bool, error) {
	end := time.Now().Add(readyBound)
	for {
		ok, err := cond()
		if ok || err != nil {
			return ok, err
		}
		if !time.Now().Before(end) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(readyPoll):
		}
	}
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

// findTab is the tab of tabs with this id.
func findTab(tabs []herdrapp.Tab, id string) (herdrapp.Tab, bool) {
	for _, t := range tabs {
		if t.ID == id {
			return t, true
		}
	}
	return herdrapp.Tab{}, false
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

func say(p port.Placement, format string, a ...any) {
	if p.Say != nil {
		p.Say(fmt.Sprintf(format, a...))
	}
}
