package hc

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

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
// One rule set decides where a terminal goes (spot), for Place and for the adoption of a hidden terminal alike:
//
//	Group      → a new tab named Group
//	In         → that tab; refused when it holds the lead's terminal, and for a lead when it holds any terminal;
//	             full: a new tab of the kind
//	lead       → the first tab named `lead` that holds no terminal at all, else a new `lead` tab
//	lane, role → the first tab with room whose project terminals are all of the kind (with none: a tab named for
//	             the kind), else a new tab of the kind
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
			kindLabel(p.Kind))
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

// errNoApp is spot's error for a client with no App: no window, as with herdr-app closed.
var errNoApp error = &herdrapp.Error{Op: "ls", Msg: "no herdr-app client"}

// closed: err says there is no window to lay out in (herdr-app not running, or no App).
func closed(err error) bool { return err == errNoApp || port.NotRunning(err) }

// spot is where p goes by the rules in this file's head: the project's space (made when there is none) and its tab,
// made when none that is there takes p. Its refusals are project failures.
func (cl *Client) spot(ctx context.Context, recs []Record, p port.Placement) (spot, error) {
	if cl.App == nil {
		return spot{}, errNoApp
	}
	pr := p.Project
	sp, err := cl.App.EnsureSpace(ctx, pr.Name, pr.Root)
	if err != nil {
		return spot{}, err
	}
	newTab := func(name string) (spot, error) {
		if sp.First.ID != "" { // the space this call made: its one tab is the new tab, its terminal herdr-app's
			t, err := cl.App.RenameTab(ctx, sp.First.ID, name)
			t.Terminal = sp.First.Terminal
			return spot{Space: sp.ID, Tab: t, Folder: sp.Folder}, err
		}
		folder := cmp.Or(p.Cwd, pr.Root)
		t, err := cl.App.NewTab(ctx, sp.ID, name, folder)
		return spot{Space: sp.ID, Tab: t, Folder: folder}, err
	}
	switch {
	case p.Group != "":
		return newTab(p.Group)
	case p.In != "":
		t, ok := findTab(sp.Tabs, p.In)
		if !ok {
			return spot{}, project.Failf("tab %s is not in the %s space", p.In, sp.Name)
		}
		held := inTab(recs, t.ID)
		if p.Kind == port.KindLead && len(held) > 0 {
			return spot{}, project.Failf("tab %s holds %d pane(s): the lead keeps its tab alone: pass --new-tab <label>",
				p.In, len(held))
		}
		if holdsLead(held, pr) {
			return spot{}, project.Failf("tab %s holds the lead's pane, which keeps its tab alone: pass --new-tab <label> "+
				"(or --pane <id>)", p.In)
		}
		if len(held) < pr.MaxPanes {
			return spot{Space: sp.ID, Tab: t}, nil
		}
		say(p, "tab %s is full (%d of %d panes)", p.In, len(held), pr.MaxPanes)
	default:
		if t, ok := kindTab(sp.Tabs, recs, p.Kind, pr); ok {
			return spot{Space: sp.ID, Tab: t}, nil
		}
	}
	return newTab(kindLabel(p.Kind))
}

// kindTab is the first tab of tabs, in the window's order, a placement of kind k with no place named goes to. A
// lead's: a tab named `lead` that holds no terminal at all (a person's shell, the app's own and an unlabelled one
// occupy it too). Any other kind's: fewer than MaxPanes terminals in it, every project terminal in it of kind k (the
// lead's is of none), and with none of them a tab named for the kind.
func kindTab(tabs []herdrapp.Tab, recs []Record, k port.PaneKind, pr port.PlaceProject) (herdrapp.Tab, bool) {
	for _, t := range tabs {
		held := inTab(recs, t.ID)
		if k == port.KindLead {
			if t.Name == kindLabel(k) && len(held) == 0 {
				return t, true
			}
			continue
		}
		if len(held) >= pr.MaxPanes {
			continue
		}
		mine, fits := 0, true
		for _, r := range held {
			if r.Labels[LabelProject] == pr.Name {
				mine++
				fits = fits && placeKind(r, pr) == k
			}
		}
		if fits && (mine > 0 || t.Name == kindLabel(k)) {
			return t, true
		}
	}
	return herdrapp.Tab{}, false
}

// holdsLead: a terminal of held is the project's lead's.
func holdsLead(held []Record, pr port.PlaceProject) bool {
	for _, r := range held {
		if r.Labels[LabelProject] == pr.Name && placeKind(r, pr) == port.KindLead {
			return true
		}
	}
	return false
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

// findTab is the tab of tabs with this id or name.
func findTab(tabs []herdrapp.Tab, idOrName string) (herdrapp.Tab, bool) {
	for _, t := range tabs {
		if t.ID == idOrName || t.Name == idOrName {
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
