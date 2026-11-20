package app_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/hc"
	"obor/internal/herdrapp"
	"obor/internal/herdrapp/herdrapptest"
)

// adoptRig is the hc rig with herdr-app's window (a fake) and serve's adoption wired to hc.Client.Adopt, as cmd/obor
// does; t4 is a terminal the project made while the window was closed (obor labels only).
func adoptRig(t *testing.T) (*hcRig, *herdrapptest.App) {
	t.Helper()
	r := newHCRig(t)
	win := herdrapptest.New()
	win.StartTerminals(r.core.AppTerminals(false))
	cl := r.core.Client()
	cl.App = win.Client()
	pr := port.PlaceProject{Name: "pj", Root: r.root, MaxPanes: 4,
		Has: func(cwd string) bool { return strings.HasPrefix(cwd, r.root) },
		KindOf: func(label string) port.PaneKind {
			if label == "pj-lead" {
				return port.KindLead
			}
			return port.KindLane
		}}
	r.adopt = func(ctx context.Context) (int, error) { return cl.Adopt(ctx, pr) }
	r.core.Add(hc.Record{Address: "t4", Cwd: r.root,
		Labels: map[string]string{hc.LabelProject: "pj", hc.LabelPlace: "lane"}})
	return r, win
}

func tabNames(win *herdrapptest.App) []string {
	var out []string
	for _, s := range win.Spaces() {
		for _, tab := range s.Tabs {
			out = append(out, tab.Name)
		}
	}
	slices.Sort(out)
	return out
}

// A hidden terminal made while herdr-app was closed: the connect with the window still closed logs one line and leaves
// it hidden; the next connect, the window open, places it in its kind's tab with no extra shell left behind.
func TestServeOnHCPlacesHiddenTerminalsAtConnect(t *testing.T) {
	t.Parallel()
	bubble(t, func(t *testing.T) {
		r, win := adoptRig(t)
		win.Down()
		before := len(r.core.Live())
		r.start()
		r.waitFor("the one line", func() bool { return strings.Contains(r.out.String(), "hidden terminals stay unplaced") })
		if n := strings.Count(r.out.String(), "hidden terminals stay unplaced"); n != 1 {
			t.Errorf("%d lines for the closed window, want 1:\n%s", n, r.out)
		}
		if l := r.core.Labels("t4")[herdrapp.LabelSpace]; l != "" {
			t.Fatalf("precondition: t4 is placed (%q) with the window closed", l)
		}
		win.Up()
		sub := r.subscribes()
		r.core.Down()
		r.core.Up()
		r.settled(sub)
		r.waitFor("t4 placed", func() bool { return r.core.Labels("t4")[herdrapp.LabelTab] != "" })
		r.waitFor("the placed line", func() bool { return strings.Contains(r.out.String(), "placed 3 hidden terminal(s)") })
		if got := tabNames(win); !slices.Equal(got, []string{"lanes", "lead"}) {
			t.Errorf("tabs = %q, want lanes and lead", got)
		}
		if n := len(r.core.Live()); n != before {
			t.Errorf("%d live terminals after placing, want %d: no extra shell", n, before)
		}
		if l := r.core.Labels("t3")[herdrapp.LabelSpace]; l != "" {
			t.Errorf("another project's terminal was placed: %v", r.core.Labels("t3"))
		}
	})
}
