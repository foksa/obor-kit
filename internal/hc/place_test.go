package hc_test

import (
	"cmp"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/hc/hctest"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
	"obor/internal/project"
)

// placeKit is hc over hctest with herdr-app over herdrapptest, bridged: the app's space.new and tab.new start their
// tab's first terminal on the core (not kept), as herdr-app does on hc. The pair Place lays terminals out in.
type placeKit struct {
	core *hctest.Core
	app  *herdrapptest.App
	c    *hc.Client
	pr   port.PlaceProject
	said []string
}

func newPlaceKit(maxPanes int) *placeKit {
	k := &placeKit{core: hctest.New(), app: herdrapptest.New()}
	k.app.StartTerminals(k.core.AppTerminals(false))
	k.c = k.core.Client()
	k.c.App = k.app.Client()
	k.pr = port.PlaceProject{Name: "proj", Root: "/proj", MaxPanes: maxPanes,
		Has:    func(cwd string) bool { return strings.HasPrefix(cwd, "/proj") },
		KindOf: func(string) port.PaneKind { return "" }}
	return k
}

// place is Place of kind, with in/group, and the address it answered.
func (k *placeKit) place(t *testing.T, kind port.PaneKind, in, group string) string {
	t.Helper()
	id, err := k.c.Place(ctx, port.Placement{Kind: kind, Cwd: "/proj/lanes/x", Env: []string{"A=1", "B="}, In: in,
		Group: group, Project: k.pr, Say: func(l string) { k.said = append(k.said, l) }})
	if err != nil {
		t.Fatalf("Place(%s, in %q, group %q) = %v", kind, in, group, err)
	}
	return id
}

// tabOf is the name of the tab of the space "proj" that holds terminal id by its labels ("" when none does).
func (k *placeKit) tabOf(t *testing.T, id string) string {
	t.Helper()
	labels := k.core.Labels(id)
	if labels == nil {
		t.Fatalf("the core has no terminal %s", id)
	}
	for _, s := range k.app.Spaces() {
		for _, tab := range s.Tabs {
			if s.ID == labels[herdrapp.LabelSpace] && tab.ID == labels[herdrapp.LabelTab] {
				return tab.Name
			}
		}
	}
	return ""
}

// live is the core's live terminals' addresses, the app's own included.
func (k *placeKit) live() []string {
	var out []string
	for _, r := range k.core.Live() {
		out = append(out, r.Address)
	}
	return out
}

// inTab is the live terminals labelled with tab id.
func (k *placeKit) inTab(id string) []string {
	var out []string
	for _, r := range k.core.Live() {
		if r.Labels[herdrapp.LabelTab] == id {
			out = append(out, r.Address)
		}
	}
	return out
}

func (k *placeKit) tabNames() []string {
	var out []string
	for _, s := range k.app.Spaces() {
		for _, tab := range s.Tabs {
			out = append(out, tab.Name)
		}
	}
	return out
}

// A pane into a tab that is there is a terminal.run; the tab's panes, for the placement decision, include the app's
// own terminals (a person's split): they count toward its room.
func TestPlaceCountIncludesTheAppsTerminals(t *testing.T) {
	k := newPlaceKit(2)
	a := k.place(t, port.KindLane, "", "")
	sp, tab := k.core.Labels(a)[herdrapp.LabelSpace], k.core.Labels(a)[herdrapp.LabelTab]
	runs := len(k.core.Runs())
	b := k.place(t, port.KindRole, tab, "")
	if len(k.core.Runs()) != runs+1 || k.core.Labels(b)[herdrapp.LabelTab] != tab {
		t.Fatalf("In %s: %d terminal.run (was %d), labels %v; want a terminal.run into the tab", tab, len(k.core.Runs()), runs,
			k.core.Labels(b))
	}

	k2 := newPlaceKit(2)
	a = k2.place(t, port.KindLane, "", "")
	sp, tab = k2.core.Labels(a)[herdrapp.LabelSpace], k2.core.Labels(a)[herdrapp.LabelTab]
	person := k2.core.AppTerminals(true)(sp, tab, "/proj")
	if got := k2.inTab(tab); len(got) != 2 {
		t.Fatalf("precondition: the lanes tab holds %q, want the lane and the person's terminal", got)
	}
	k2.said = nil
	c := k2.place(t, port.KindLane, tab, "")
	if k2.core.Labels(c)[herdrapp.LabelTab] == tab ||
		!slices.ContainsFunc(k2.said, func(l string) bool { return strings.Contains(l, "is full (2 of 2") }) {
		t.Errorf("In a tab with a lane and the app's %s: labels %v, said %q; want it full and a new tab", person,
			k2.core.Labels(c), k2.said)
	}
}

// The app keeps its terminal: no terminal.keep; it does not: Place keeps it.
func TestPlaceKeepsTheAppsTerminal(t *testing.T) {
	k := newPlaceKit(4)
	a := k.place(t, port.KindLane, "", "")
	if !k.core.Kept(a) || !slices.Contains(k.core.Requests(), "terminal.keep") {
		t.Errorf("an unkept app terminal: kept %v, requests %q; want terminal.keep sent", k.core.Kept(a), k.core.Requests())
	}
	k2 := newPlaceKit(4)
	k2.app.StartTerminals(k2.core.AppTerminals(true))
	b := k2.place(t, port.KindLane, "", "")
	if !k2.core.Kept(b) || slices.Contains(k2.core.Requests(), "terminal.keep") {
		t.Errorf("a kept app terminal: kept %v, requests %q; want no terminal.keep", k2.core.Kept(b), k2.core.Requests())
	}
}

// Adopt with the window closed is port.NotRunning and places nothing; another project's terminal is left alone.
func TestAdoptSkipsOtherProjectsAndClosedWindow(t *testing.T) {
	k := newPlaceKit(4)
	k.core.Add(hc.Record{Address: "t1", Cwd: "/x", Labels: map[string]string{hc.LabelProject: "other"}})
	k.core.Add(hc.Record{Address: "t2", Cwd: "/proj", Labels: map[string]string{hc.LabelProject: "proj", hc.LabelPlace: "lane"}})
	k.app.Down()
	if n, err := k.c.Adopt(ctx, k.pr); n != 0 || !port.NotRunning(err) {
		t.Errorf("Adopt with the window closed = %d, %v; want 0 and NotRunning", n, err)
	}
	k.app.Up()
	if n, err := k.c.Adopt(ctx, k.pr); err != nil || n != 1 {
		t.Fatalf("Adopt = %d, %v; want 1", n, err)
	}
	if k.core.Labels("t2")[herdrapp.LabelTab] == "" {
		t.Errorf("t2 labels = %v, want placed", k.core.Labels("t2"))
	}
	if _, ok := k.core.Labels("t1")[herdrapp.LabelSpace]; ok {
		t.Errorf("another project's terminal was placed: %v", k.core.Labels("t1"))
	}
}

// RenameProject relabels the terminals and renames the space; herdr-app closed: the terminals are relabelled and Warn
// says to rename the space by hand.
func TestRenameProjectRenamesTheSpace(t *testing.T) {
	k := newPlaceKit(4)
	id := k.place(t, port.KindLane, "", "")
	spaceID := k.core.Labels(id)[herdrapp.LabelSpace]
	done, err := k.c.RenameProject(ctx, "proj", "nw", func(l string) string { return l })
	if err != nil || done.Already || done.Warn {
		t.Fatalf("RenameProject = %+v, %v", done, err)
	}
	if got := k.core.Labels(id)[hc.LabelProject]; got != "nw" {
		t.Errorf("obor.project = %q, want nw", got)
	}
	if s := k.app.Spaces(); len(s) != 1 || s[0].ID != spaceID || s[0].Name != "nw" {
		t.Errorf("spaces = %+v, want the same space named nw", s)
	}
	if again, err := k.c.RenameProject(ctx, "proj", "nw", func(l string) string { return l }); err != nil || !again.Already {
		t.Errorf("a rerun = %+v, %v; want Already", again, err)
	}

	k.app.Down()
	k.pr.Name = "nw"
	id2 := k.place(t, port.KindLane, "", "")
	done, err = k.c.RenameProject(ctx, "nw", "third", func(l string) string { return l })
	if err != nil || !done.Warn || !strings.Contains(done.Line, "rename its space nw to third") {
		t.Errorf("RenameProject with the window closed = %+v, %v; want Warn naming the space", done, err)
	}
	if got := k.core.Labels(id2)[hc.LabelProject]; got != "third" {
		t.Errorf("obor.project = %q, want third", got)
	}
}

const laneCwd, roleCwd = "/proj/lanes/x", "/proj/.obor/reviewer"

// envSet: the shell of id has the env the kit's placements give (A=1, B set empty or unset).
func (k *placeKit) envSet(id string) bool {
	env := k.core.Env(id)
	return env["A"] == "1" && env["B"] == ""
}

// cwdOf is the core's cwd of id.
func (k *placeKit) cwdOf(id string) string {
	for _, r := range k.core.Live() {
		if r.Address == id {
			return r.Cwd
		}
	}
	return ""
}

// person is a terminal a person opened in tab of space sp: the app's, kept, no obor label.
func (k *placeKit) person(sp, tab string) string {
	return k.core.AppTerminals(true)(sp, tab, "/proj")
}

// The case table of Place (and the adoption it runs first) on the bridged hctest + herdrapptest: the window's state,
// the space and tabs before, the placement's kind and option, whether its start types the env line (Starts), and
// what it makes. A pane in a tab this call made is the terminal herdr-app started there (known by the address the
// app printed), with p's cwd and env as Place returns; a pane in a tab that was there is a terminal.run with them.
func TestPlaceCases(t *testing.T) {
	type when struct {
		k    *placeKit
		addr string
		err  error
		pre  []string // the live terminals before the placement
	}
	for _, c := range []struct {
		name   string
		closed bool
		state  func(t *testing.T, k *placeKit)
		p      port.Placement
		expect func(t *testing.T, w when)
	}{
		{name: "open, no space, lane: new space and lanes tab, the app's terminal in the lane's cwd and env",
			p: port.Placement{Kind: port.KindLane},
			expect: func(t *testing.T, w when) {
				k := w.k
				spaces := k.app.Spaces()
				if len(spaces) != 1 || spaces[0].Name != "proj" || spaces[0].Folder != "/proj" || !slices.Equal(k.tabNames(), []string{"lanes"}) {
					t.Fatalf("spaces %+v, tabs %q; want one space proj at /proj with its tab renamed lanes", spaces, k.tabNames())
				}
				if !slices.Equal(k.live(), []string{w.addr}) || len(k.core.Runs()) != 0 {
					t.Errorf("terminals %q, %d terminal.run; want the app's %s alone", k.live(), len(k.core.Runs()), w.addr)
				}
				if got := k.cwdOf(w.addr); got != laneCwd || !k.envSet(w.addr) {
					t.Errorf("the pane's cwd %q, env %v; want %s and A=1, B unset", got, k.core.Env(w.addr), laneCwd)
				}
				l := k.core.Labels(w.addr)
				if l[hc.LabelProject] != "proj" || l[hc.LabelPlace] != "lane" || l[herdrapp.LabelTab] != spaces[0].Tabs[0].ID ||
					!k.core.Kept(w.addr) {
					t.Errorf("labels %v, kept %v; want the obor labels, the tab's and kept", l, k.core.Kept(w.addr))
				}
			}},
		{name: "open, no space, lane, started: Place types no env line, the start's is the one",
			p: port.Placement{Kind: port.KindLane, Starts: true},
			expect: func(t *testing.T, w when) {
				k := w.k
				if sent := k.core.Sent(w.addr); len(sent) != 0 || !slices.Equal(k.live(), []string{w.addr}) {
					t.Fatalf("Place typed %q, terminals %q; want nothing typed in the app's %s alone", sent, k.live(), w.addr)
				}
				k.core.Program("claude", &hc.Status{State: "idle", App: "claude-code"})
				if _, err := k.c.Start(ctx, port.AgentStart{Kind: "claude", Pane: w.addr, Label: "pj-l-01"}); err != nil {
					t.Fatalf("Start in %s = %v", w.addr, err)
				}
				if sent := k.core.Sent(w.addr); len(sent) != 1 || strings.Contains(sent[0], "export") {
					t.Errorf("typed in the pane %q, want the start's one line and no env line of Place's", sent)
				}
			}},
		{name: "open, lanes tab with room, lane: terminal.run into it with the cwd and env",
			state: func(t *testing.T, k *placeKit) { k.place(t, port.KindLane, "", "") },
			p:     port.Placement{Kind: port.KindLane},
			expect: func(t *testing.T, w when) {
				k, runs := w.k, w.k.core.Runs()
				if len(runs) != 1 || runs[0].Cwd != laneCwd || !maps.Equal(runs[0].Env, map[string]string{"A": "1", "B": ""}) ||
					!runs[0].Keep || !runs[0].NoParent {
					t.Fatalf("terminal.run %+v, want one: a kept login shell at %s, no parent, env A=1 B empty", runs, laneCwd)
				}
				if k.core.Labels(w.addr)[herdrapp.LabelTab] != k.core.Labels(w.pre[0])[herdrapp.LabelTab] || len(k.tabNames()) != 1 {
					t.Errorf("the lane in %v, tabs %q; want the first lane's tab", k.core.Labels(w.addr), k.tabNames())
				}
			}},
		{name: "open, lanes tab full, lane: a new lanes tab, the app's terminal the pane",
			state: func(t *testing.T, k *placeKit) { k.place(t, port.KindLane, "", ""); k.place(t, port.KindLane, "", "") },
			p:     port.Placement{Kind: port.KindLane},
			expect: func(t *testing.T, w when) {
				k := w.k
				tab := k.core.Labels(w.addr)[herdrapp.LabelTab]
				if !slices.Equal(k.tabNames(), []string{"lanes", "lanes"}) || !slices.Equal(k.inTab(tab), []string{w.addr}) {
					t.Fatalf("tabs %q, the new tab holds %q; want a second lanes tab holding %s alone", k.tabNames(), k.inTab(tab), w.addr)
				}
				if len(k.core.Runs()) != 1 || len(k.live()) != 3 || k.cwdOf(w.addr) != laneCwd || !k.envSet(w.addr) {
					t.Errorf("%d terminal.run, terminals %q, cwd %q, env %v; want the app's terminal with the lane's cwd and env",
						len(k.core.Runs()), k.live(), k.cwdOf(w.addr), k.core.Env(w.addr))
				}
			}},
		{name: "open, a lane there, role: a roles tab in the role worktree",
			state: func(t *testing.T, k *placeKit) { k.place(t, port.KindLane, "", "") },
			p:     port.Placement{Kind: port.KindRole, Cwd: roleCwd},
			expect: func(t *testing.T, w when) {
				if got := w.k.tabOf(t, w.addr); got != "roles" || w.k.cwdOf(w.addr) != roleCwd || !w.k.envSet(w.addr) {
					t.Errorf("the role in tab %q, cwd %q, env %v; want roles, %s and its env", got, w.k.cwdOf(w.addr),
						w.k.core.Env(w.addr), roleCwd)
				}
				var newTab []string
				for _, c := range w.k.app.Calls() {
					if len(c) > 2 && c[1] == "tab.new" {
						newTab = c
					}
				}
				if newTab == nil || !strings.Contains(newTab[2], `"folder":"`+roleCwd+`"`) {
					t.Errorf("tab.new call %q, want the role's cwd as its folder", newTab)
				}
			}},
		{name: "open, no space, lead: a lead tab, the lead alone",
			p: port.Placement{Kind: port.KindLead, Cwd: "/proj"},
			expect: func(t *testing.T, w when) {
				tab := w.k.core.Labels(w.addr)[herdrapp.LabelTab]
				if !slices.Equal(w.k.tabNames(), []string{"lead"}) || !slices.Equal(w.k.live(), []string{w.addr}) ||
					!slices.Equal(w.k.inTab(tab), []string{w.addr}) {
					t.Errorf("tabs %q, terminals %q; want a lead tab holding the lead %s alone", w.k.tabNames(), w.k.live(), w.addr)
				}
			}},
		{name: "open, a person's shell in the lead tab, lead: a new lead tab, the shell untouched",
			state: func(t *testing.T, k *placeKit) {
				k.app.AddSpace(herdrapptest.Space{ID: "sp9", Name: "proj", Folder: "/proj", Tabs: []herdrapptest.Tab{{ID: "tb9", Name: "lead"}}})
				k.person("sp9", "tb9")
			},
			p: port.Placement{Kind: port.KindLead, Cwd: "/proj"},
			expect: func(t *testing.T, w when) {
				k, shell := w.k, w.pre[0]
				tab := k.core.Labels(w.addr)[herdrapp.LabelTab]
				if tab == "tb9" || !slices.Equal(k.tabNames(), []string{"lead", "lead"}) || !slices.Equal(k.inTab(tab), []string{w.addr}) {
					t.Errorf("the lead in tab %s (tabs %q, it holds %q); want a new lead tab holding it alone", tab, k.tabNames(), k.inTab(tab))
				}
				if !slices.Equal(k.inTab("tb9"), []string{shell}) || k.core.Labels(shell)[hc.LabelProject] != "" {
					t.Errorf("tb9 holds %q, the shell's labels %v; want the person's %s there, unlabelled", k.inTab("tb9"),
						k.core.Labels(shell), shell)
				}
			}},
		{name: "open, lane tab, lead --tab: refused",
			state: func(t *testing.T, k *placeKit) { k.place(t, port.KindLane, "", "") },
			p:     port.Placement{Kind: port.KindLead, Cwd: "/proj", In: "lanes"},
			expect: func(t *testing.T, w when) {
				var f project.Fail
				if w.err == nil || !errors.As(w.err, &f) {
					t.Errorf("Place = %v, want the decision's refusal as a project failure", w.err)
				}
				if !slices.Equal(w.k.live(), w.pre) {
					t.Errorf("terminals %q after the refusal, want %q", w.k.live(), w.pre)
				}
			}},
		{name: "open, a lane there, lane --new-tab x: a new tab x, the app's terminal the pane",
			state: func(t *testing.T, k *placeKit) { k.place(t, port.KindLane, "", "") },
			p:     port.Placement{Kind: port.KindLane, Group: "x"},
			expect: func(t *testing.T, w when) {
				k := w.k
				tab := k.core.Labels(w.addr)[herdrapp.LabelTab]
				if k.tabOf(t, w.addr) != "x" || !slices.Equal(k.inTab(tab), []string{w.addr}) || len(k.core.Runs()) != 0 {
					t.Errorf("the lane in tab %q holding %q, %d terminal.run; want tab x holding the app's %s alone",
						k.tabOf(t, w.addr), k.inTab(tab), len(k.core.Runs()), w.addr)
				}
				if k.cwdOf(w.addr) != laneCwd || !k.envSet(w.addr) {
					t.Errorf("cwd %q, env %v; want the lane's", k.cwdOf(w.addr), k.core.Env(w.addr))
				}
			}},
		{name: "open, a person's terminal opened in the new tab meanwhile, adoption: never killed",
			state: func(t *testing.T, k *placeKit) {
				k.place(t, port.KindLead, "", "")
				k.app.Down()
				k.place(t, port.KindLane, "", "")
				k.app.Up()
				app := k.core.AppTerminals(false)
				k.app.StartTerminals(func(sp, tab, folder string) string {
					a := app(sp, tab, folder)
					k.person(sp, tab) // a person's split, opened before the adoption labels the hidden terminal
					return a
				})
			},
			p: port.Placement{Kind: port.KindRole, Cwd: roleCwd},
			expect: func(t *testing.T, w when) {
				k, hidden := w.k, w.pre[1]
				if w.err != nil {
					t.Fatalf("Place = %v", w.err)
				}
				if k.tabOf(t, hidden) != "lanes" {
					t.Fatalf("precondition: the hidden lane in tab %q, want lanes", k.tabOf(t, hidden))
				}
				tab := k.core.Labels(hidden)[herdrapp.LabelTab]
				held := k.inTab(tab)
				if len(held) != 2 || held[0] != hidden {
					t.Errorf("the lanes tab holds %q, want the adopted %s and the person's terminal", held, hidden)
				}
				for _, a := range held {
					if a != hidden && k.core.Labels(a)[hc.LabelProject] != "" {
						t.Errorf("the person's terminal %s got obor labels %v", a, k.core.Labels(a))
					}
				}
			}},
		{name: "closed, lane: a hidden terminal with the cwd and env, placed at the next Place with no extra shell",
			closed: true,
			p:      port.Placement{Kind: port.KindLane},
			expect: func(t *testing.T, w when) {
				k := w.k
				l, runs := k.core.Labels(w.addr), k.core.Runs()
				if l[herdrapp.LabelSpace] != "" || l[hc.LabelProject] != "proj" || len(runs) != 1 || runs[0].Cwd != laneCwd || !k.envSet(w.addr) {
					t.Fatalf("labels %v, terminal.run %+v; want a hidden terminal.run with the obor labels, the cwd and the env", l, runs)
				}
				if !slices.ContainsFunc(k.said, func(l string) bool { return strings.Contains(l, "herdr-app is not running") }) {
					t.Errorf("said %q, want the closed-window line", k.said)
				}
				k.app.Up()
				role := k.place(t, port.KindRole, "", "")
				if k.tabOf(t, w.addr) != "lanes" || k.tabOf(t, role) != "roles" {
					t.Errorf("after the next Place: the hidden one in %q, the role in %q; want lanes and roles", k.tabOf(t, w.addr), k.tabOf(t, role))
				}
				if got := k.live(); !slices.Equal(got, []string{w.addr, role}) {
					t.Errorf("terminals %q, want the two placed: the app's shell of the adopted one's tab gone", got)
				}
				if n, err := k.c.Adopt(ctx, k.pr); err != nil || n != 0 {
					t.Errorf("Adopt again = %d, %v; want 0", n, err)
				}
			}},
		{name: "closed, lane --tab: refused", closed: true,
			p:      port.Placement{Kind: port.KindLane, In: "tb1"},
			expect: func(t *testing.T, w when) { refusedClosed(t, w.k, w.err) }},
		{name: "closed, lane --new-tab: refused", closed: true,
			p:      port.Placement{Kind: port.KindLane, Group: "review"},
			expect: func(t *testing.T, w when) { refusedClosed(t, w.k, w.err) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			k := newPlaceKit(2)
			if c.state != nil {
				c.state(t, k)
			}
			if c.closed {
				k.app.Down()
			}
			pre := k.live()
			k.said = nil
			p := c.p
			p.Cwd, p.Env, p.Project = cmp.Or(p.Cwd, laneCwd), []string{"A=1", "B="}, k.pr
			p.Say = func(l string) { k.said = append(k.said, l) }
			addr, err := k.c.Place(ctx, p)
			if err == nil && addr == "" {
				t.Fatal("Place answered no address and no error")
			}
			if err != nil && !strings.Contains(c.name, "refused") {
				t.Fatalf("Place = %v", err)
			}
			c.expect(t, when{k: k, addr: addr, err: err, pre: pre})
		})
	}
}

// refusedClosed: err is the one-line refusal of an explicit tab with the window closed, a project failure, and
// nothing was made.
func refusedClosed(t *testing.T, k *placeKit, err error) {
	t.Helper()
	var f project.Fail
	if err == nil || err.Error() != "herdr-app is not running: open it, or place without --tab/--new-tab" || !errors.As(err, &f) {
		t.Errorf("Place with the window closed = %v, want the one-line project failure", err)
	}
	if got := k.live(); len(got) != 0 {
		t.Errorf("terminals %q after the refusal, want none", got)
	}
}

// An app terminal whose shell never comes up: Place's error names it (the pane is not handed back unready).
func TestPlaceAppTerminalWithNoShellIsTheError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newPlaceKit(2)
		app := k.core.AppTerminals(false)
		k.app.StartTerminals(func(sp, tab, folder string) string {
			a := app(sp, tab, folder)
			k.core.Update(a, func(r *hc.Record) { r.Foreground = nil })
			return a
		})
		_, err := k.c.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: laneCwd, Env: []string{"A=1"}, Project: k.pr})
		if err == nil || !strings.Contains(err.Error(), "did not come up") {
			t.Errorf("Place = %v, want the shell that did not come up", err)
		}
		if live := k.core.Live(); len(live) != 1 || len(k.core.Sent(live[0].Address)) != 0 {
			t.Errorf("terminals %+v; want the app's one, nothing typed in it", live)
		}
	})
}
