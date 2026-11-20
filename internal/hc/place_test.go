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

// placeKit is hc over hctest with herdr-app over herdrapptest: the pair Place lays terminals out in.
type placeKit struct {
	core *hctest.Core
	app  *herdrapptest.App
	c    *hc.Client
	pr   port.PlaceProject
	said []string
}

func newPlaceKit(maxPanes int) *placeKit {
	k := &placeKit{core: hctest.New(), app: herdrapptest.New()}
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

func (k *placeKit) tabNames() []string {
	var out []string
	for _, s := range k.app.Spaces() {
		for _, tab := range s.Tabs {
			out = append(out, tab.Name)
		}
	}
	return out
}

// The first placement makes the project's space (named after it, at the root) and a lanes tab; the terminal is a
// kept login shell with the cwd, the env, no parent, and the obor and herdr labels; a second lane joins the tab.
func TestPlaceFirstLaneMakesSpaceAndTab(t *testing.T) {
	k := newPlaceKit(4)
	if got := k.app.Spaces(); len(got) != 0 {
		t.Fatalf("precondition: the window has spaces %+v", got)
	}
	a := k.place(t, port.KindLane, "", "")
	spaces := k.app.Spaces()
	if len(spaces) != 1 || spaces[0].Name != "proj" || spaces[0].Folder != "/proj" {
		t.Fatalf("spaces = %+v, want one named proj at /proj", spaces)
	}
	if got := k.tabNames(); !slices.Equal(got, []string{"lanes"}) {
		t.Fatalf("tabs = %q, want the space's first tab renamed lanes", got)
	}
	runs := k.core.Runs()
	if len(runs) != 1 {
		t.Fatalf("terminal.run sent %d times, want 1", len(runs))
	}
	r := runs[0]
	if len(r.Argv) != 0 || r.Cwd != "/proj/lanes/x" || !r.Keep || !r.NoParent || !maps.Equal(r.Env, map[string]string{"A": "1", "B": ""}) {
		t.Errorf("terminal.run = %+v, want a login shell at the cwd, kept, no parent, env A=1 B=empty", r)
	}
	if r.Labels[hc.LabelProject] != "proj" || r.Labels[hc.LabelPlace] != "lane" ||
		r.Labels[herdrapp.LabelSpace] != spaces[0].ID || r.Labels[herdrapp.LabelTab] != spaces[0].Tabs[0].ID {
		t.Errorf("labels = %v, want obor.project proj, the lane kind and the space's and tab's ids", r.Labels)
	}
	b := k.place(t, port.KindLane, "", "")
	if a == b || k.tabOf(t, b) != "lanes" || len(k.tabNames()) != 1 {
		t.Errorf("second lane %s in tab %q of %q, want the same lanes tab", b, k.tabOf(t, b), k.tabNames())
	}
	if n := len(k.app.Spaces()); n != 1 {
		t.Errorf("%d spaces after the second lane, want 1", n)
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
	if n := len(k.core.Runs()); n != 1 {
		t.Errorf("%d terminals made, want only the lead's: the refusal made none", n)
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
