package testkit

import (
	"os"
	"testing"
)

// Isolate clears the check-slot variables the merge gate exports to `make check` (T-297): a test's heavy `obor run`
// inside a merge otherwise reuses the merge's real slot file. It also turns git's auto maintenance off (T-438): a
// commit's detached `git maintenance run --auto` could still be writing .git/objects when the test's temp dir is
// removed.
func TestIsolate(t *testing.T) {
	ks := []string{"CHECK_PRIORITY", "CHECK_SLOT_HELD", "CHECK_SLOT_DIR", "CHECK_SLOTS"}
	for _, k := range ks {
		t.Setenv(k, "/x")
		if v, ok := os.LookupEnv(k); !ok || v != "/x" {
			t.Fatalf("precondition: %s = %q, set %v", k, v, ok)
		}
	}
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_KEY_0", "")
	t.Setenv("GIT_CONFIG_VALUE_0", "")
	Isolate()
	for _, k := range ks {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set to %q", k, v)
		}
	}
	p := New(t)
	if got := p.Git(p.Root, "config", "--get", "maintenance.auto"); got != "false" {
		t.Errorf("maintenance.auto = %q, want false", got)
	}
}
