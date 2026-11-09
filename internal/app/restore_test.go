package app

import (
	"testing"
	"time"
)

// T-1135: the first connect's rule: another instance than recorded restores; the same one, a woken project (obor
// stop's pause at the load) and, with nothing recorded, a herdr that started before serve's last activity (or no
// activity known: a new project) do not; nothing recorded and a herdr started after it restores.
func TestFirstRestore(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 11, 9, 8, 0, 0, 123, time.UTC)
	cur := started.Format(time.RFC3339Nano)
	other := started.Add(-time.Hour).Format(time.RFC3339Nano)
	for _, c := range []struct {
		name    string
		was     string
		last    time.Time
		woke    bool
		restore bool
	}{
		{"another instance recorded", other, time.Time{}, false, true},
		{"the same instance recorded", cur, started.Add(-time.Hour), false, false},
		{"another instance, woken from obor stop", other, time.Time{}, true, false},
		{"nothing recorded, herdr started after serve's last activity", "", started.Add(-time.Minute), false, true},
		{"nothing recorded, herdr started before serve's last activity", "", started.Add(time.Minute), false, false},
		{"nothing recorded, no activity known", "", time.Time{}, false, false},
		{"nothing recorded, woken from obor stop", "", started.Add(-time.Minute), true, false},
	} {
		if why := firstRestore(c.was, started, c.last, c.woke); (why != "") != c.restore {
			t.Errorf("%s: firstRestore = %q, want restore %v", c.name, why, c.restore)
		}
	}
}
