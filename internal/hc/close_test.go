package hc_test

import (
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
)

// CloseProject kills the project's terminals and closes the tabs they leave empty and the space itself when nothing
// else is in it: another project's terminal and its space stay; with Dry nothing is killed or closed.
func TestCloseProjectKillsItsTerminalsThenItsTabsAndSpace(t *testing.T) {
	k := newPlaceKit(4)
	a := k.place(t, port.KindLane, "", "")
	b := k.place(t, port.KindRole, "", "")
	if got := len(k.app.Spaces()); got != 1 {
		t.Fatalf("precondition: %d spaces, want the project's", got)
	}
	// another project's space with its terminal
	k.app.AddSpace(herdrapptest.Space{ID: "sp-o", Name: "other", Tabs: []herdrapptest.Tab{{ID: "tb-o", Name: "lanes"}}})
	k.core.Add(hc.Record{Address: "t9", State: "live", Cwd: "/other", Labels: map[string]string{hc.LabelProject: "other",
		herdrapp.LabelSpace: "sp-o", herdrapp.LabelTab: "tb-o"}})
	one := port.CloseProject{Name: "proj"}

	lines, err := k.c.CloseProject(ctx, port.CloseProject{Name: "proj", Dry: true})
	if err != nil || len(lines) != 3 { // two terminals, the space with its tabs
		t.Fatalf("dry run: %v, %v", lines, err)
	}
	if k.core.Labels(a) == nil || len(k.app.Spaces()) != 2 || slices.Contains(k.core.Requests(), "terminal.kill") {
		t.Errorf("a dry run closed something: spaces %v, requests %v", k.app.Spaces(), k.core.Requests())
	}

	lines, err = k.c.CloseProject(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"terminal " + a, "terminal " + b, "herdr-app space proj"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if n := countKills(k); n != 2 {
		t.Errorf("terminal.kill sent %d times, want 2 (the project's terminals)", n)
	}
	spaces := k.app.Spaces()
	if len(spaces) != 1 || spaces[0].ID != "sp-o" || len(spaces[0].Tabs) != 1 {
		t.Errorf("spaces left %+v, want only the other project's", spaces)
	}
	again, err := k.c.CloseProject(ctx, one)
	if err != nil || len(again) != 0 {
		t.Errorf("second run: %v, %v, want nothing left", again, err)
	}
}

// A tab with a terminal of another project, or a tab a person made, stays: only the project's own tab goes, the space
// with them.
func TestCloseProjectKeepsTabsItDoesNotOwn(t *testing.T) {
	k := newPlaceKit(4)
	k.place(t, port.KindLane, "", "")
	sp := k.app.Spaces()[0]
	k.app.AddTab(sp.ID, herdrapptest.Tab{ID: "tb-mine", Name: "notes"}) // a person's tab: empty, not obor's kind
	lines, err := k.c.CloseProject(ctx, port.CloseProject{Name: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	spaces := k.app.Spaces()
	if len(spaces) != 1 || len(spaces[0].Tabs) != 1 || spaces[0].Tabs[0].Name != "notes" {
		t.Errorf("spaces after %v: %+v, want the space with its person's tab only", lines, spaces)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "herdr-app tab lanes") {
		t.Errorf("no tab line in %v", lines)
	}
}

// herdr-app closed: the terminals are killed, the window stays, a line says so.
func TestCloseProjectWithTheWindowClosed(t *testing.T) {
	k := newPlaceKit(4)
	k.place(t, port.KindLane, "", "")
	k.app.Down()
	lines, err := k.c.CloseProject(ctx, port.CloseProject{Name: "proj"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "\n"); !strings.Contains(got, "herdr-app is not running") {
		t.Errorf("lines %q, want the window note", got)
	}
	if n := countKills(k); n != 1 {
		t.Errorf("terminal.kill sent %d times, want 1", n)
	}
}

func countKills(k *placeKit) int {
	n := 0
	for _, r := range k.core.Requests() {
		if r == "terminal.kill" {
			n++
		}
	}
	return n
}

// A terminal Ours refuses (the caller's) is not killed, not listed, and keeps its tab and space; the project's others
// close, with the tab only they held. A dry run lists the same terminals and kills nothing.
func TestCloseProjectSparesTheTerminalOursRefuses(t *testing.T) {
	k := newPlaceKit(4)
	own := k.place(t, port.KindLane, "", "")
	other := k.place(t, port.KindRole, "", "")
	if k.tabOf(t, own) == k.tabOf(t, other) {
		t.Fatalf("precondition: the caller's terminal and the other share the tab %s", k.tabOf(t, own))
	}
	ownTab := k.tabOf(t, own)
	ours := func(p port.Pane) bool { return p.ID != own }

	lines, err := k.c.CloseProject(ctx, port.CloseProject{Name: "proj", Ours: ours, Dry: true})
	got := strings.Join(lines, "\n")
	if err != nil || !strings.Contains(got, "terminal "+other) || strings.Contains(got, "terminal "+own) ||
		strings.Contains(got, "space") {
		t.Fatalf("dry run: %q, %v: want the other terminal only, no space", got, err)
	}
	if countKills(k) != 0 || len(k.app.Spaces()[0].Tabs) != 2 {
		t.Fatalf("a dry run closed something: kills %d, spaces %+v", countKills(k), k.app.Spaces())
	}

	lines, err = k.c.CloseProject(ctx, port.CloseProject{Name: "proj", Ours: ours})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "\n"); strings.Contains(got, "terminal "+own) || !strings.Contains(got, "terminal "+other) {
		t.Errorf("lines %q: want the other terminal, not the caller's", got)
	}
	if live := k.live(); countKills(k) != 1 || !slices.Contains(live, own) || slices.Contains(live, other) {
		t.Errorf("kills %d, live %v: want the other killed and the caller's (%s) alive", countKills(k), live, own)
	}
	spaces := k.app.Spaces()
	if len(spaces) != 1 || len(spaces[0].Tabs) != 1 || spaces[0].Tabs[0].Name != ownTab {
		t.Errorf("spaces %+v, want the project's space with the caller's tab %s only", spaces, ownTab)
	}
}
