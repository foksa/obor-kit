package hc_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

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

// The first placement makes the project's space (named after it, at the root) and a lanes tab, and its pane is the
// terminal herdr-app started in the tab (in the root, now kept, with the obor and herdr labels): exactly one terminal,
// no terminal.run. A second lane joins the tab as a terminal.run: a kept login shell with the cwd, the env, no parent
// and the labels.
func TestPlaceFirstLaneMakesSpaceAndTab(t *testing.T) {
	k := newPlaceKit(4)
	if got := k.app.Spaces(); len(got) != 0 || len(k.live()) != 0 {
		t.Fatalf("precondition: the window has spaces %+v, the core terminals %q", got, k.live())
	}
	a := k.place(t, port.KindLane, "", "")
	spaces := k.app.Spaces()
	if len(spaces) != 1 || spaces[0].Name != "proj" || spaces[0].Folder != "/proj" {
		t.Fatalf("spaces = %+v, want one named proj at /proj", spaces)
	}
	if got := k.tabNames(); !slices.Equal(got, []string{"lanes"}) {
		t.Fatalf("tabs = %q, want the space's first tab renamed lanes", got)
	}
	if got := k.live(); !slices.Equal(got, []string{a}) {
		t.Fatalf("live terminals = %q, want exactly the lane's %s (the app's own terminal)", got, a)
	}
	if n := len(k.core.Runs()); n != 0 {
		t.Errorf("terminal.run sent %d times, want 0: the app's terminal is the pane", n)
	}
	l := k.core.Labels(a)
	if l[hc.LabelProject] != "proj" || l[hc.LabelPlace] != "lane" || l[herdrapp.LabelSpace] != spaces[0].ID ||
		l[herdrapp.LabelTab] != spaces[0].Tabs[0].ID {
		t.Errorf("labels = %v, want obor.project proj, the lane kind and the space's and tab's ids", l)
	}
	if !k.core.Kept(a) {
		t.Errorf("the app's terminal %s is not kept", a)
	}
	if r := k.core.Live()[0]; r.Cwd != "/proj" {
		t.Errorf("the pane's cwd = %q, want the project root (launch's env line changes to the lane's)", r.Cwd)
	}

	b := k.place(t, port.KindLane, "", "")
	runs := k.core.Runs()
	if len(runs) != 1 {
		t.Fatalf("terminal.run sent %d times after the second lane, want 1", len(runs))
	}
	r := runs[0]
	if len(r.Argv) != 0 || r.Cwd != "/proj/lanes/x" || !r.Keep || !r.NoParent || !maps.Equal(r.Env, map[string]string{"A": "1", "B": ""}) {
		t.Errorf("terminal.run = %+v, want a login shell at the cwd, kept, no parent, env A=1 B=empty", r)
	}
	if r.Labels[hc.LabelProject] != "proj" || r.Labels[hc.LabelPlace] != "lane" ||
		r.Labels[herdrapp.LabelSpace] != spaces[0].ID || r.Labels[herdrapp.LabelTab] != spaces[0].Tabs[0].ID {
		t.Errorf("labels = %v, want obor.project proj, the lane kind and the space's and tab's ids", r.Labels)
	}
	if a == b || k.tabOf(t, b) != "lanes" || len(k.tabNames()) != 1 {
		t.Errorf("second lane %s in tab %q of %q, want the same lanes tab", b, k.tabOf(t, b), k.tabNames())
	}
	if n := len(k.app.Spaces()); n != 1 || len(k.live()) != 2 {
		t.Errorf("%d spaces and terminals %q after the second lane, want 1 and two", n, k.live())
	}
}

// The lead first: the space's tab is the lead's and holds the lead alone. A lead after a lane: a new lead tab whose
// one terminal is the lead's (tab.new with the root as its folder), no extra shell anywhere.
func TestPlaceLeadTabHoldsTheLeadOnly(t *testing.T) {
	k := newPlaceKit(4)
	lead := k.place(t, port.KindLead, "", "")
	tab := k.core.Labels(lead)[herdrapp.LabelTab]
	if got := k.tabNames(); !slices.Equal(got, []string{"lead"}) {
		t.Fatalf("tabs = %q, want lead", got)
	}
	if got := k.inTab(tab); !slices.Equal(got, []string{lead}) || len(k.live()) != 1 {
		t.Errorf("the lead's tab holds %q (terminals %q), want the lead %s alone", got, k.live(), lead)
	}

	k2 := newPlaceKit(4)
	lane := k2.place(t, port.KindLane, "", "")
	lead2 := k2.place(t, port.KindLead, "", "")
	tab2 := k2.core.Labels(lead2)[herdrapp.LabelTab]
	if got := k2.inTab(tab2); !slices.Equal(got, []string{lead2}) || k2.tabOf(t, lead2) != "lead" {
		t.Errorf("the lead's tab %q holds %q, want the lead %s alone", k2.tabOf(t, lead2), got, lead2)
	}
	if got := k2.live(); !slices.Equal(got, []string{lane, lead2}) || len(k2.core.Runs()) != 0 {
		t.Errorf("terminals %q, %d terminal.run, want the lane and the lead only, both the app's", got, len(k2.core.Runs()))
	}
	var newTab []string
	for _, c := range k2.app.Calls() {
		if len(c) > 2 && c[1] == "tab.new" {
			newTab = c
		}
	}
	if newTab == nil || !strings.Contains(newTab[2], `"folder":"/proj"`) {
		t.Errorf("tab.new call %q, want the project root as its folder", newTab)
	}
}

// --new-tab, the second tab a full tab makes, and adopting a hidden terminal into a new tab make no extra shell: each
// adds exactly the placed terminal.
func TestPlaceNewTabsMakeNoExtraShell(t *testing.T) {
	k := newPlaceKit(2)
	g := k.place(t, port.KindRole, "", "review")
	if got := k.tabNames(); !slices.Equal(got, []string{"review"}) || !slices.Equal(k.live(), []string{g}) {
		t.Fatalf("--new-tab review first: tabs %q, terminals %q, want the space's tab named review holding %s only",
			got, k.live(), g)
	}
	g2 := k.place(t, port.KindRole, "", "review")
	if got := k.live(); len(got) != 2 || k.tabOf(t, g2) != "review" || k.core.Labels(g2)[herdrapp.LabelTab] == k.core.Labels(g)[herdrapp.LabelTab] {
		t.Errorf("a second --new-tab review: terminals %q, want two in two review tabs", got)
	}
	a, b := k.place(t, port.KindLane, "", ""), k.place(t, port.KindLane, "", "")
	c := k.place(t, port.KindLane, "", "") // lanes is full: a second lanes tab
	if got := k.tabNames(); !slices.Equal(got, []string{"review", "review", "lanes", "lanes"}) {
		t.Fatalf("tabs = %q, want two review and two lanes tabs", got)
	}
	if got := k.live(); !slices.Equal(got, []string{g, g2, a, b, c}) {
		t.Errorf("terminals %q, want exactly the five placed", got)
	}
	if got := k.inTab(k.core.Labels(c)[herdrapp.LabelTab]); !slices.Equal(got, []string{c}) {
		t.Errorf("the second lanes tab holds %q, want %s only", got, c)
	}

	h := newPlaceKit(4)
	h.place(t, port.KindLead, "", "")
	h.app.Down()
	hidden := h.place(t, port.KindLane, "", "")
	h.app.Up()
	if n, err := h.c.Adopt(ctx, h.pr); err != nil || n != 1 {
		t.Fatalf("Adopt = %d, %v; want 1", n, err)
	}
	tab := h.core.Labels(hidden)[herdrapp.LabelTab]
	if got := h.inTab(tab); h.tabOf(t, hidden) != "lanes" || !slices.Equal(got, []string{hidden}) {
		t.Errorf("the adopted terminal's tab %q holds %q, want lanes with %s only (the app's shell gone)",
			h.tabOf(t, hidden), got, hidden)
	}
	if n := len(h.live()); n != 2 {
		t.Errorf("terminals %q after the adoption, want the lead's and the adopted one", h.live())
	}
}

// A pane into a tab that is there is a terminal.run; the tab's count includes the app's own terminals (a person's
// split): with them it is full, and the next lane goes to a new tab.
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

// A lead with --tab: a tab holding a pane (a lane's) is refused naming --new-tab, nothing made; an empty tab is
// allowed (RV-220 #2).
func TestPlaceLeadInAnOccupiedTabRefused(t *testing.T) {
	k := newPlaceKit(4)
	k.app.AddSpace(herdrapptest.Space{ID: "sp9", Name: "proj", Folder: "/proj",
		Tabs: []herdrapptest.Tab{{ID: "tb8", Name: "lanes"}, {ID: "tb9", Name: "spare"}}})
	lane := k.place(t, port.KindLane, "", "")
	if got := k.core.Labels(lane)[herdrapp.LabelTab]; got != "tb8" {
		t.Fatalf("precondition: the lane in tab %q, want tb8", got)
	}
	before := k.live()
	_, err := k.c.Place(ctx, port.Placement{Kind: port.KindLead, Cwd: "/proj", In: "tb8", Project: k.pr})
	if err == nil || !strings.Contains(err.Error(), "the lead keeps its tab alone") || !strings.Contains(err.Error(), "--new-tab") {
		t.Errorf("lead Place into the lane's tab = %v, want the refusal naming --new-tab", err)
	}
	if got := k.live(); !slices.Equal(got, before) {
		t.Errorf("terminals %q after the refusal, want %q", got, before)
	}
	lead := k.place(t, port.KindLead, "tb9", "")
	if got := k.inTab("tb9"); !slices.Equal(got, []string{lead}) {
		t.Errorf("lead into the empty tab tb9: it holds %q, want the lead %s", got, lead)
	}
}

// herdr-app closed: --tab and --new-tab are refused with one line and make nothing (RV-220 #3).
func TestPlaceExplicitTabRefusedWithTheWindowClosed(t *testing.T) {
	k := newPlaceKit(4)
	k.app.Down()
	for _, p := range []port.Placement{{Kind: port.KindLane, In: "tb1"}, {Kind: port.KindLane, Group: "review"}} {
		p.Cwd, p.Project = "/proj", k.pr
		_, err := k.c.Place(ctx, p)
		if err == nil || err.Error() != "herdr-app is not running: open it, or place without --tab/--new-tab" {
			t.Errorf("Place(in %q, group %q) with the window closed = %v, want the one-line refusal", p.In, p.Group, err)
		}
		var f project.Fail
		if !errors.As(err, &f) {
			t.Errorf("the refusal %v is not a project failure", err)
		}
	}
	if got := k.live(); len(got) != 0 {
		t.Errorf("terminals %q after the refusals, want none", got)
	}
	k.c.App = nil
	if _, err := k.c.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj", Group: "x", Project: k.pr}); err == nil {
		t.Error("--new-tab with no herdr-app client succeeded")
	}
}

// A full tab: the next lane goes to a second tab named lanes; the lane after it joins that one.
func TestPlaceFullTabMakesASecondLanesTab(t *testing.T) {
	k := newPlaceKit(2)
	ids := []string{k.place(t, port.KindLane, "", ""), k.place(t, port.KindLane, "", "")}
	if got := k.tabNames(); !slices.Equal(got, []string{"lanes"}) {
		t.Fatalf("tabs after two lanes = %q, want one lanes tab", got)
	}
	ids = append(ids, k.place(t, port.KindLane, "", ""), k.place(t, port.KindLane, "", ""))
	if got := k.tabNames(); !slices.Equal(got, []string{"lanes", "lanes"}) {
		t.Fatalf("tabs after four lanes = %q, want two lanes tabs", got)
	}
	l := func(id string) string { return k.core.Labels(id)[herdrapp.LabelTab] }
	if l(ids[0]) != l(ids[1]) || l(ids[2]) != l(ids[3]) || l(ids[1]) == l(ids[2]) {
		t.Errorf("tabs of the four lanes = %s %s %s %s, want two and two", l(ids[0]), l(ids[1]), l(ids[2]), l(ids[3]))
	}
}

// A role goes to roles, never into a lanes tab; the lead to its own lead tab, which no lane, role or second lead
// shares.
func TestPlaceRolesAndLeadKeepTheirTabs(t *testing.T) {
	k := newPlaceKit(4)
	lane := k.place(t, port.KindLane, "", "")
	role := k.place(t, port.KindRole, "", "")
	lead := k.place(t, port.KindLead, "", "")
	lane2 := k.place(t, port.KindLane, "", "")
	role2 := k.place(t, port.KindRole, "", "")
	lead2 := k.place(t, port.KindLead, "", "")
	for id, want := range map[string]string{lane: "lanes", lane2: "lanes", role: "roles", role2: "roles", lead: "lead", lead2: "lead"} {
		if got := k.tabOf(t, id); got != want {
			t.Errorf("terminal %s is in tab %q, want %q", id, got, want)
		}
	}
	if k.core.Labels(lead)[herdrapp.LabelTab] == k.core.Labels(lead2)[herdrapp.LabelTab] {
		t.Errorf("two leads share a tab: %v", k.core.Labels(lead))
	}
	if k.core.Labels(lane)[herdrapp.LabelTab] != k.core.Labels(lane2)[herdrapp.LabelTab] {
		t.Errorf("lanes are in two tabs, want one")
	}
}

// --tab: In the lead's tab is refused with a line naming the way out; In a lanes tab joins it, a full one makes a new
// tab of the kind; a tab the space lacks is an error.
func TestPlaceInATab(t *testing.T) {
	k := newPlaceKit(2)
	lead := k.place(t, port.KindLead, "", "")
	leadTab := k.core.Labels(lead)[herdrapp.LabelTab]
	_, err := k.c.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj", In: leadTab, Project: k.pr})
	if err == nil || !strings.Contains(err.Error(), "holds the lead") || !strings.Contains(err.Error(), "--new-tab") {
		t.Errorf("Place in the lead's tab = %v, want the refusal naming --new-tab", err)
	}
	if n := len(k.live()); n != 1 {
		t.Errorf("terminals %q, want only the lead's: the refusal made none", k.live())
	}
	a := k.place(t, port.KindLane, "", "")
	laneTab := k.core.Labels(a)[herdrapp.LabelTab]
	b := k.place(t, port.KindRole, laneTab, "")
	if k.core.Labels(b)[herdrapp.LabelTab] != laneTab {
		t.Errorf("In %s put the terminal in %v", laneTab, k.core.Labels(b))
	}
	k.said = nil
	c := k.place(t, port.KindLane, laneTab, "")
	if got := k.tabOf(t, c); got != "lanes" || k.core.Labels(c)[herdrapp.LabelTab] == laneTab {
		t.Errorf("In a full tab: terminal in %q %v, want a new lanes tab", got, k.core.Labels(c))
	}
	if !slices.ContainsFunc(k.said, func(l string) bool { return strings.Contains(l, "is full (2 of 2") }) {
		t.Errorf("said %q, want the full-tab line", k.said)
	}
	if _, err := k.c.Place(ctx, port.Placement{Kind: port.KindLane, Cwd: "/proj", In: "tb99", Project: k.pr}); err == nil {
		t.Error("Place in a tab the space lacks succeeded")
	}
}

// Group is always a new tab of that name: a second Group of the same name is another tab, and `--new-tab lead` never
// lands in the lead's tab.
func TestPlaceGroupMakesANewNamedTab(t *testing.T) {
	k := newPlaceKit(4)
	lead := k.place(t, port.KindLead, "", "")
	first := k.place(t, port.KindLane, "", "review")
	second := k.place(t, port.KindLane, "", "review")
	if got := k.tabOf(t, first); got != "review" {
		t.Errorf("terminal in tab %q, want review", got)
	}
	if k.core.Labels(first)[herdrapp.LabelTab] == k.core.Labels(second)[herdrapp.LabelTab] || k.tabOf(t, second) != "review" {
		t.Errorf("two Places of Group review share a tab: %v %v", k.core.Labels(first), k.core.Labels(second))
	}
	if got := k.tabNames(); !slices.Equal(got, []string{"lead", "review", "review"}) {
		t.Errorf("tabs = %q, want lead and two review tabs", got)
	}
	other := k.place(t, port.KindLane, "", "lead")
	if k.core.Labels(other)[herdrapp.LabelTab] == k.core.Labels(lead)[herdrapp.LabelTab] {
		t.Errorf("Group lead put a terminal in the lead's tab")
	}
}

// herdr-app closed: the terminal is made hidden (the obor labels only), the reply says so, nothing is started; the
// next Place, once the window answers, places it by the same rules, and a second Adopt finds nothing to do.
func TestPlaceWindowClosedMakesHiddenTerminalPlacedLater(t *testing.T) {
	k := newPlaceKit(4)
	k.app.Down()
	hidden := k.place(t, port.KindLane, "", "")
	labels := k.core.Labels(hidden)
	if labels[hc.LabelProject] != "proj" || labels[herdrapp.LabelSpace] != "" || labels[herdrapp.LabelTab] != "" {
		t.Fatalf("hidden terminal labels = %v, want the obor labels only", labels)
	}
	if !slices.ContainsFunc(k.said, func(l string) bool { return strings.Contains(l, "herdr-app is not running") }) {
		t.Errorf("said %q, want the line that the window is closed", k.said)
	}
	for _, call := range k.app.Calls() {
		if call[0] != "--version" && call[0] != "ls" && call[0] != "do" {
			t.Errorf("a call %q to the window: obor only lists and does", call)
		}
	}
	k.app.Up()
	other := k.place(t, port.KindRole, "", "")
	if got := k.tabOf(t, hidden); got != "lanes" {
		t.Errorf("the hidden terminal is in tab %q after the next Place, want lanes", got)
	}
	if got := k.tabOf(t, other); got != "roles" {
		t.Errorf("the new role is in tab %q, want roles", got)
	}
	if n, err := k.c.Adopt(ctx, k.pr); err != nil || n != 0 {
		t.Errorf("Adopt again = %d, %v; want 0 (idempotent)", n, err)
	}
	if got := k.tabNames(); !slices.Equal(got, []string{"lanes", "roles"}) {
		t.Errorf("tabs = %q, want lanes and roles", got)
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

// A refusal is a project failure (a line setup prints after FAIL).
func TestPlaceRefusalIsAProjectFailure(t *testing.T) {
	k := newPlaceKit(2)
	lead := k.place(t, port.KindLead, "", "")
	_, err := k.c.Place(ctx, port.Placement{Kind: port.KindRole, Cwd: "/proj", In: k.core.Labels(lead)[herdrapp.LabelTab], Project: k.pr})
	var f project.Fail
	if !errors.As(err, &f) {
		t.Errorf("refusal %v is not a project failure", err)
	}
}
