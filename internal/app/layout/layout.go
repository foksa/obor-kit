// Package layout is the one placement decision of every terminal adapter: where a new pane goes (an existing tab, a
// new tab of which name, or a refusal) from the project's tabs and what their panes are, and whether the caller's pane
// qualifies as the lead's home. It is pure: no terminal call, no clock; each adapter reads its terminal's facts into
// the types here, asks, and does what the answer says. A tab is the adapter's unit of a group of panes (herdr's tab,
// herdr-app's tab); only the tabs of the project's own group are passed.
//
// The rules of Place, in the order they apply:
//
//	Group      → a new tab named Group
//	In         → that tab; refused when it holds the lead's pane, and for a lead when it holds any pane; full: a new
//	             tab of the kind; not among the tabs: refused
//	lead       → the first tab named `lead` that holds no pane at all, else a new `lead` tab
//	lane, role → the first tab with room whose panes are all of the kind (an aside pane, a person's, is none's and
//	             blocks no kind); with none of the kind's own: only a tab named for the kind; else a new tab of the kind
//
// A new tab of a kind is named for it (Label): `lanes`, `roles`, `lead`.
package layout

import (
	"cmp"
	"fmt"

	"obor/internal/app/port"
)

// Pane is a pane of a tab, by the facts an adapter can give.
type Pane struct {
	// Kind is what the pane is to placement: the lead's (by its live agent or its label, either keeps its tab the
	// lead's alone), a lane's, a role's; "" for a pane that is none of the project's agents (a shell).
	Kind port.PaneKind
	// Aside: a person's pane in the project's own group, known not to be another project's. It counts toward a tab's
	// room and toward a lead's tab being empty, but it blocks no kind and defines none. A terminal that cannot tell
	// a person's shell from another project's pane never sets it: such a pane blocks.
	Aside bool
}

// Tab is a tab of the project's group, in the window's order, with all its panes.
type Tab struct {
	ID, Name string
	Panes    []Pane
}

// Ask is one placement: the kind of the new pane, where the caller asked for it (at most one of Group, In: as in
// port.Placement), the project's group's tabs and how many panes a tab may hold.
type Ask struct {
	Kind     port.PaneKind
	Group    string // a new tab named so
	In       string // the tab with this id or name
	Home     string // what the group is called in a refusal: "the proj space"
	MaxPanes int
	Tabs     []Tab
}

// Answer is where the pane goes: in the tab Tab (an element of Ask.Tabs) when New is "", else in a new tab named New.
// Refused is the refusal's text (the adapter makes it a project failure), Said a progress line to show.
type Answer struct {
	Tab     Tab
	New     string
	Refused string
	Said    string
}

// Label is the name of a new tab for a pane of kind k: "lanes", "roles", "lead".
func Label(k port.PaneKind) string {
	switch k {
	case port.KindLane:
		return "lanes"
	case port.KindRole:
		return "roles"
	}
	return string(k)
}

// Place decides where a pane goes by the rules in the package's head. A kind that is none of port's is a refusal
// unless Group names the tab: nothing derived from an empty kind is a default.
func Place(a Ask) Answer {
	if a.Group != "" {
		return Answer{New: a.Group}
	}
	switch a.Kind {
	case port.KindLead, port.KindLane, port.KindRole:
	default:
		return Answer{Refused: fmt.Sprintf("a pane of kind %q has no place: it is a lead, a lane or a role", string(a.Kind))}
	}
	if a.In != "" {
		return in(a)
	}
	for _, t := range a.Tabs {
		if takes(t, a.Kind, a.MaxPanes) {
			return Answer{Tab: t}
		}
	}
	return Answer{New: Label(a.Kind)}
}

// in is the placement into the tab a.In names.
func in(a Ask) Answer {
	var t Tab
	found := false
	for _, c := range a.Tabs {
		if c.ID == a.In || c.Name == a.In {
			t, found = c, true
			break
		}
	}
	if !found {
		return Answer{Refused: fmt.Sprintf("tab %s is not in %s", a.In, cmp.Or(a.Home, "the project's group"))}
	}
	if holdsLead(t) {
		return Answer{Refused: fmt.Sprintf("tab %s holds the lead's pane, which keeps its tab alone: pass --new-tab <label> "+
			"(or --pane <id>)", a.In)}
	}
	if a.Kind == port.KindLead && len(t.Panes) > 0 {
		return Answer{Refused: fmt.Sprintf("tab %s holds %d pane(s): the lead keeps its tab alone: pass --new-tab <label>",
			a.In, len(t.Panes))}
	}
	if len(t.Panes) >= a.MaxPanes {
		return Answer{New: Label(a.Kind),
			Said: fmt.Sprintf("tab %s is full (%d of %d panes)", a.In, len(t.Panes), a.MaxPanes)}
	}
	return Answer{Tab: t}
}

// takes: a placement of kind k with no tab named goes to t. The lead's: a tab named `lead` with no pane at all. Any
// other kind's: room for one more, every pane that is not aside of kind k, and (the tab holds none of the
// project's) a name that is the kind's.
func takes(t Tab, k port.PaneKind, max int) bool {
	if k == port.KindLead {
		return t.Name == Label(k) && len(t.Panes) == 0
	}
	if len(t.Panes) >= max {
		return false
	}
	mine := 0
	for _, p := range t.Panes {
		if p.Aside {
			continue
		}
		if p.Kind != k {
			return false
		}
		mine++
	}
	return mine > 0 || t.Name == Label(k)
}

// holdsLead: a pane of t is the lead's.
func holdsLead(t Tab) bool {
	for _, p := range t.Panes {
		if p.Kind == port.KindLead {
			return true
		}
	}
	return false
}

// Caller is the facts of the caller's pane that decide whether it is the lead's home (Home), each as the terminal
// reports it.
type Caller struct {
	Pane    string
	Found   bool   // the pane is a live pane of this terminal
	Agent   string // the agent reported in it ("" none)
	Program string // a program in its foreground other than its shell or the command that offers it ("" none)
	Tabbed  bool   // it is shown in a tab
	// TabMates: how many other panes its tab holds.
	TabMates int
	// Foreign is a pane of another project in the caller's group ("" none) and ForeignOf what says it is another's
	// (its project or its cwd).
	Foreign, ForeignOf string
	Grouped            bool   // its group is one of this computer's
	Group              string // that group's name
	// Other: a group of this computer named Project (the project's name) is not the caller's.
	Project string
	Other   bool
}

// Home is why the caller's pane is not the lead's home, "" when it qualifies: a live pane of this terminal with no
// agent and nothing in the foreground but its shell or the command offering it, shown in a tab and alone in it, in a
// group that holds no pane of another project, while no other group is named after the project (the project's tabs
// are there). The checks run in this order; the first that fails is the answer.
func Home(c Caller) string {
	switch {
	case !c.Found:
		return fmt.Sprintf("pane %s is not a live pane of this terminal", c.Pane)
	case c.Agent != "":
		return fmt.Sprintf("pane %s runs an agent (%s)", c.Pane, c.Agent)
	case c.Program != "":
		return fmt.Sprintf("pane %s runs a program (%s)", c.Pane, c.Program)
	case !c.Tabbed:
		return fmt.Sprintf("pane %s is in no tab", c.Pane)
	case c.TabMates > 0:
		return fmt.Sprintf("pane %s shares its tab with %d other pane(s)", c.Pane, c.TabMates)
	case c.Foreign != "":
		return fmt.Sprintf("its group holds pane %s of another project (%s)", c.Foreign, c.ForeignOf)
	case !c.Grouped:
		return fmt.Sprintf("pane %s is in no group of this computer", c.Pane)
	case c.Other:
		return fmt.Sprintf("the %s group is another than this pane's (%s)", c.Project, c.Group)
	}
	return ""
}
