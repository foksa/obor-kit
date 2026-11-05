package port

import "testing"

// The one place a pane's status becomes ready or busy (T-888): every spelled-out "idle"/"done" and "working"/
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
