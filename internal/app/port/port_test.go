package port

import "testing"

// The one place a pane's status becomes ready or busy: every spelled-out "idle"/"done" and "working"/
// "blocked" check in the app and launch reads these two.
func TestPaneReadyBusy(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]struct{ ready, busy bool }{
		"idle":    {ready: true},
		"done":    {ready: true},
		"working": {busy: true},
		"blocked": {busy: true},
		"unknown": {},
		"":        {},
	} {
		p := Pane{Status: status}
		if p.Ready() != want.ready || p.Busy() != want.busy {
			t.Errorf("Pane{Status: %q}.Ready(), .Busy() = %v, %v; want %v, %v", status, p.Ready(), p.Busy(),
				want.ready, want.busy)
		}
		if p.Ready() && p.Busy() {
			t.Errorf("Pane{Status: %q} is both ready and busy", status)
		}
	}
}

// Lost: every pane when All, else its panes; Add joins two (every pane when either is), each pane once.
func TestLost(t *testing.T) {
	t.Parallel()
	some := Lost{Panes: []string{"t1", "t2"}}
	if !some.Has("t2") || some.Has("t3") || some.None() || !(Lost{}).None() || !(Lost{All: true}).Has("t3") {
		t.Errorf("Has/None: %+v has t2 %v, t3 %v, none %v; zero none %v", some, some.Has("t2"), some.Has("t3"), some.None(),
			(Lost{}).None())
	}
	if got := some.Add(Lost{Panes: []string{"t2", "t3"}}); got.All || len(got.Panes) != 3 || !got.Has("t3") {
		t.Errorf("Add = %+v, want t1, t2, t3", got)
	}
	if got := some.Add(Lost{All: true}); !got.All {
		t.Errorf("Add of every pane = %+v, want every pane", got)
	}
	if got := (Lost{}).Add(some); got.All || len(got.Panes) != 2 {
		t.Errorf("Add to none = %+v, want %+v", got, some)
	}
}
