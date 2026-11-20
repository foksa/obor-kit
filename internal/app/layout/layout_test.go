package layout_test

import (
	"strings"
	"testing"

	"obor/internal/app/layout"
	"obor/internal/app/port"
)

const (
	lead, lane, role = port.KindLead, port.KindLane, port.KindRole
	shell            = port.PaneKind("") // a pane that is none of the project's agents, blocking: not known as a person's
)

// tab is a tab of panes by kind; "aside" is a person's pane in the project's own group.
func tab(id, name string, kinds ...port.PaneKind) layout.Tab {
	t := layout.Tab{ID: id, Name: name}
	for _, k := range kinds {
		t.Panes = append(t.Panes, layout.Pane{Kind: k})
	}
	return t
}

func withAside(t layout.Tab) layout.Tab {
	t.Panes = append(t.Panes, layout.Pane{Aside: true})
	return t
}

// withAsideLead adds an aside pane that carries the lead's kind: aside wins, it holds no lead.
func withAsideLead(t layout.Tab) layout.Tab {
	t.Panes = append(t.Panes, layout.Pane{Kind: lead, Aside: true})
	return t
}

// The case table of the placement decision: every row the herdr adapter's tests (internal/setup/placement_test.go,
// internal/herdr/setup_test.go) and the hc adapter's (internal/hc/place_test.go) cover, as tabs and panes in, the
// answer out. want is the tab's id, "new:<name>" for a new tab, or "refused: <text>" (a part of the refusal).
func TestPlace(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		a     layout.Ask
		want  string
		saidA string // a part of the progress line ("" none)
	}{
		// no flag: lanes and roles
		{"lane, no tab at all: a new lanes tab", layout.Ask{Kind: lane}, "new:lanes", ""},
		{"role, no tab at all: a new roles tab", layout.Ask{Kind: role}, "new:roles", ""},
		{"lane, a lanes tab with 2 lanes: split into it",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane)}}, "t1", ""},
		{"lane, a tab of lanes under another name: it too",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "x", lane)}}, "t1", ""},
		{"lane, the lanes tab full: a new lanes tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane, lane, lane)}}, "new:lanes", ""},
		{"lane, the first lanes tab full, a second with room: the second",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane, lane, lane), tab("t2", "lanes", lane)}},
			"t2", ""},
		{"lane, two lanes tabs with room: the first, in the window's order",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane), tab("t2", "lanes", lane)}}, "t1", ""},
		{"role, a roles tab with 3 roles: split into it",
			layout.Ask{Kind: role, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane), tab("t2", "roles", role, role, role)}},
			"t2", ""},
		{"role, only a lanes tab: a new roles tab",
			layout.Ask{Kind: role, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane)}}, "new:roles", ""},
		{"lane, a lanes tab with an aside pane of kind lead: split into it",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{withAsideLead(tab("t1", "lanes", lane))}}, "t1", ""},
		{"lane, only a roles tab: a new lanes tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "roles", role)}}, "new:lanes", ""},
		{"lane, a tab with lanes and a shell: not a lanes tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane, shell)}}, "new:lanes", ""},
		{"lane, a tab with a lane and an unknown pane (maybe another project's): not a lanes tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes", lane, shell, shell)}}, "new:lanes", ""},
		{"lane, a tab with a lane and a person's pane: it, the person's blocks no kind",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{withAside(tab("t1", "lanes", lane))}}, "t1", ""},
		{"lane, a lanes tab full with the person's pane counted: a new tab",
			layout.Ask{Kind: lane, MaxPanes: 2, Tabs: []layout.Tab{withAside(tab("t1", "lanes", lane))}}, "new:lanes", ""},
		{"lane, a tab named lanes holding only a person's pane: it",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{withAside(tab("t1", "lanes"))}}, "t1", ""},
		{"lane, a tab not named lanes holding only a person's pane: a new tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{withAside(tab("t1", "notes"))}}, "new:lanes", ""},
		{"lane, an empty tab named lanes: it",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lanes")}}, "t1", ""},
		{"lane, an empty tab named otherwise: a new tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "x")}}, "new:lanes", ""},
		{"lane, the lead's tab: never, even a lead beside a lane",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t1", "lead", lead, lane)}}, "new:lanes", ""},
		{"role, a tab with the lead and a role: never",
			layout.Ask{Kind: role, Tabs: []layout.Tab{tab("t1", "lead", lead, role)}}, "new:roles", ""},
		{"lane, a lead tab then a lanes tab: the lanes tab",
			layout.Ask{Kind: lane, Tabs: []layout.Tab{tab("t0", "lead", lead), tab("t1", "lanes", lane)}}, "t1", ""},

		// no flag: the lead
		{"lead, no tab: a new lead tab", layout.Ask{Kind: lead}, "new:lead", ""},
		{"lead, an empty tab named lead: in it, alone",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t9", "lead")}}, "t9", ""},
		{"lead, a person's shell in the lead tab: a new lead tab",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{withAside(tab("t9", "lead"))}}, "new:lead", ""},
		{"lead, an unknown pane in the lead tab: a new lead tab",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t9", "lead", shell)}}, "new:lead", ""},
		{"lead, a lead already in the lead tab: a new lead tab, never two in one",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t9", "lead", lead)}}, "new:lead", ""},
		{"lead, a lanes tab: a new lead tab, never a shared one",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "new:lead", ""},
		{"lead, an empty tab not named lead: a new lead tab",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t1", "x")}}, "new:lead", ""},
		{"lead, a lead tab with a lead then an empty lead tab: the empty one",
			layout.Ask{Kind: lead, Tabs: []layout.Tab{tab("t8", "lead", lead), tab("t9", "lead")}}, "t9", ""},

		// --new-tab
		{"lane --new-tab x: a new tab x",
			layout.Ask{Kind: lane, Group: "x", Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "new:x", ""},
		{"lane --new-tab review with a review tab there: another new tab review",
			layout.Ask{Kind: lane, Group: "review", Tabs: []layout.Tab{tab("t1", "review", lane)}}, "new:review", ""},
		{"lane --new-tab lead: a new tab, never the lead's",
			layout.Ask{Kind: lane, Group: "lead", Tabs: []layout.Tab{tab("t0", "lead", lead)}}, "new:lead", ""},
		{"a pane of no kind --new-tab x: a new tab x", layout.Ask{Group: "x"}, "new:x", ""},

		// --tab
		{"lane --tab a lanes tab by id: it",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "t1", ""},
		{"lane --tab by name: it",
			layout.Ask{Kind: lane, In: "lanes", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "t1", ""},
		{"role --tab a lanes tab: it, the kind is the caller's choice",
			layout.Ask{Kind: role, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "t1", ""},
		{"lane --tab a mixed tab (a project pane, one unknown): it",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "x", lane, shell), tab("t2", "y", lane)}},
			"t1", ""},
		{"lane --tab one pane short of full: it",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 2, Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, "t1", ""},
		{"lane --tab a full tab: a new lanes tab, said",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 2, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane)}},
			"new:lanes", "tab t1 is full (2 of 2 panes)"},
		{"role --tab a full tab named by name: a new roles tab, said with the name",
			layout.Ask{Kind: role, In: "lanes", MaxPanes: 2, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane)}},
			"new:roles", "tab lanes is full (2 of 2 panes)"},
		{"lane --tab a tab full with the person's pane counted: a new lanes tab",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 2, Tabs: []layout.Tab{withAside(tab("t1", "lanes", lane))}},
			"new:lanes", "tab t1 is full (2 of 2 panes)"},
		{"lane --tab a lanes tab with an aside pane of kind lead: it, never refused as the lead's",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{withAsideLead(tab("t1", "lanes", lane))}}, "t1", ""},
		{"lane --tab the lead's tab: refused",
			layout.Ask{Kind: lane, In: "t0", MaxPanes: 4, Tabs: []layout.Tab{tab("t0", "lead", lead)}},
			"refused: tab t0 holds the lead's pane, which keeps its tab alone: pass --new-tab <label> (or --pane <id>)", ""},
		{"role --tab the lead's tab with more panes: refused the same",
			layout.Ask{Kind: role, In: "t0", MaxPanes: 4, Tabs: []layout.Tab{tab("t0", "lead", lead, role)}},
			"refused: tab t0 holds the lead's pane", ""},
		{"lane --tab the lead's tab, which is full: refused, not a new tab",
			layout.Ask{Kind: lane, In: "t0", MaxPanes: 1, Tabs: []layout.Tab{tab("t0", "lead", lead)}},
			"refused: tab t0 holds the lead's pane", ""},
		{"lead --tab the lead's tab: refused",
			layout.Ask{Kind: lead, In: "t0", MaxPanes: 4, Tabs: []layout.Tab{tab("t0", "lead", lead)}},
			"refused: tab t0 holds the lead's pane", ""},
		{"lead --tab a lanes tab: refused, naming the count and --new-tab",
			layout.Ask{Kind: lead, In: "lanes", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "lanes", lane, lane)}},
			"refused: tab lanes holds 2 pane(s): the lead keeps its tab alone: pass --new-tab <label>", ""},
		{"lead --tab a tab with a person's pane: refused",
			layout.Ask{Kind: lead, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{withAside(tab("t1", "x"))}},
			"refused: tab t1 holds 1 pane(s)", ""},
		{"lead --tab an empty tab: it",
			layout.Ask{Kind: lead, In: "t1", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "x")}}, "t1", ""},
		{"lane --tab a tab the group lacks: refused",
			layout.Ask{Kind: lane, In: "tb99", Home: "the proj space", MaxPanes: 4, Tabs: []layout.Tab{tab("t1", "lanes", lane)}},
			"refused: tab tb99 is not in the proj space", ""},
		{"lane --tab with no tab at all: refused, naming the group generally",
			layout.Ask{Kind: lane, In: "t1", MaxPanes: 4}, "refused: tab t1 is not in the project's group", ""},

		// no kind
		{"a pane of no kind, no flag: refused", layout.Ask{Tabs: []layout.Tab{tab("t1", "lanes", lane)}},
			`refused: a pane of kind "" has no place`, ""},
		{"a pane of an unknown kind --tab: refused", layout.Ask{Kind: "guest", In: "t1", MaxPanes: 4,
			Tabs: []layout.Tab{tab("t1", "lanes", lane)}}, `refused: a pane of kind "guest" has no place`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.a.MaxPanes == 0 {
				c.a.MaxPanes = 4 // herdr-app's and herdr's default; a row that tests room sets its own
			}
			got := layout.Place(c.a)
			var have string
			switch {
			case got.Refused != "":
				have = "refused: " + got.Refused
			case got.New != "":
				have = "new:" + got.New
			default:
				have = got.Tab.ID
			}
			if want, refusal := strings.CutPrefix(c.want, "refused: "); refusal {
				if got.Refused == "" || !strings.Contains(got.Refused, want) {
					t.Fatalf("Place = %q, want a refusal with %q", have, want)
				}
				if got.New != "" || got.Tab.ID != "" {
					t.Errorf("a refusal that also names a place: %+v", got)
				}
			} else if have != c.want {
				t.Fatalf("Place = %q, want %q", have, c.want)
			}
			if c.saidA == "" && got.Said != "" || !strings.Contains(got.Said, c.saidA) {
				t.Errorf("Said = %q, want %q", got.Said, c.saidA)
			}
		})
	}
}

// A refusal is exactly the text the adapters print after FAIL: the full lead's-tab line is one text for both.
func TestPlaceRefusalTexts(t *testing.T) {
	t.Parallel()
	one := []layout.Tab{tab("t0", "lead", lead)}
	if got := layout.Place(layout.Ask{Kind: lane, In: "t0", MaxPanes: 4, Tabs: one}).Refused; got !=
		"tab t0 holds the lead's pane, which keeps its tab alone: pass --new-tab <label> (or --pane <id>)" {
		t.Errorf("the lead's tab: %q", got)
	}
	many := []layout.Tab{tab("t1", "lanes", lane, lane, lane)}
	if got := layout.Place(layout.Ask{Kind: lead, In: "t1", MaxPanes: 4, Tabs: many}).Refused; got !=
		"tab t1 holds 3 pane(s): the lead keeps its tab alone: pass --new-tab <label>" {
		t.Errorf("a lead into an occupied tab: %q", got)
	}
}

func TestLabel(t *testing.T) {
	t.Parallel()
	for k, want := range map[port.PaneKind]string{lane: "lanes", role: "roles", lead: "lead"} {
		if got := layout.Label(k); got != want {
			t.Errorf("Label(%s) = %q, want %q", k, got, want)
		}
	}
}

// The lead-home qualification: a caller that qualifies is the answer "", and each fact that disqualifies it gives its
// one line. Every row starts from a qualifying caller, so the row's fact is what the line is for, and the order of the
// checks is what makes a caller with two faults say the first.
func TestHome(t *testing.T) {
	t.Parallel()
	ok := layout.Caller{Pane: "p1", Found: true, Tabbed: true, Grouped: true, Group: "g", Project: "proj"}
	if got := layout.Home(ok); got != "" {
		t.Fatalf("precondition: the base caller does not qualify: %q", got)
	}
	for _, c := range []struct {
		name string
		edit func(*layout.Caller)
		want string
	}{
		{"not a live pane of this terminal", func(c *layout.Caller) { c.Found = false }, "pane p1 is not a live pane of this terminal"},
		{"runs an agent", func(c *layout.Caller) { c.Agent = "claude" }, "pane p1 runs an agent (claude)"},
		{"runs a program", func(c *layout.Caller) { c.Program = "vim" }, "pane p1 runs a program (vim)"},
		{"in no tab", func(c *layout.Caller) { c.Tabbed = false }, "pane p1 is in no tab"},
		{"shares its tab", func(c *layout.Caller) { c.TabMates = 2 }, "pane p1 shares its tab with 2 other pane(s)"},
		{"its group holds another project's pane", func(c *layout.Caller) { c.Foreign, c.ForeignOf = "p7", "/elsewhere" },
			"its group holds pane p7 of another project (/elsewhere)"},
		{"in no group of this computer", func(c *layout.Caller) { c.Grouped = false }, "pane p1 is in no group of this computer"},
		{"another group is named after the project", func(c *layout.Caller) { c.Other = true },
			"the proj group is another than this pane's (g)"},
		// the order: the first fault decides
		{"an agent beats a program and a shared tab", func(c *layout.Caller) { c.Agent, c.Program, c.TabMates = "claude", "vim", 1 },
			"pane p1 runs an agent (claude)"},
		{"a program beats a shared tab", func(c *layout.Caller) { c.Program, c.TabMates = "vim", 1 }, "pane p1 runs a program (vim)"},
		{"no tab beats a shared tab", func(c *layout.Caller) { c.Tabbed, c.TabMates = false, 1 }, "pane p1 is in no tab"},
		{"a shared tab beats a foreign pane", func(c *layout.Caller) { c.TabMates, c.Foreign = 1, "p7" },
			"pane p1 shares its tab with 1 other pane(s)"},
		{"a foreign pane beats another group named so", func(c *layout.Caller) { c.Foreign, c.ForeignOf, c.Other = "p7", "x", true },
			"its group holds pane p7 of another project (x)"},
		{"not found beats every other fault", func(c *layout.Caller) { c.Found, c.Agent = false, "claude" },
			"pane p1 is not a live pane of this terminal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := ok
			c.edit(&x)
			if got := layout.Home(x); got != c.want {
				t.Errorf("Home = %q, want %q", got, c.want)
			}
		})
	}
}
