package testkit

import (
	"os"
	"path/filepath"
	"strings"
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

func TestMain(m *testing.M) { Main(m) }

// New copies the binary's one template (T-1009): two projects are independent copies, each with the template's commit
// on main, a commit in one is not in the other, and the template was built once for both. Refs and objects are read
// from the files (a fresh repo's are loose), not by git: the package's git budget.
func TestNewCopiesOneTemplate(t *testing.T) {
	a, b := New(t), New(t)
	if a.Root == b.Root {
		t.Fatalf("precondition: two projects share %s", a.Root)
	}
	ref := filepath.Join(".git", "refs", "heads", "main")
	initial := strings.TrimSpace(a.Read(ref))
	if len(initial) != 40 {
		t.Fatalf("precondition: a's main %q, want the template's commit", initial)
	}
	if got := strings.TrimSpace(b.Read(ref)); got != initial {
		t.Fatalf("b's main %q, want the template's %s", got, initial)
	}
	a.Commit(a.Root)
	head := strings.TrimSpace(a.Read(ref))
	object := filepath.Join(".git", "objects", head[:2], head[2:])
	if head == initial || !a.Exists(object) {
		t.Fatalf("precondition: the commit in a moved its main to %q with its object", head)
	}
	if got := strings.TrimSpace(b.Read(ref)); got != initial {
		t.Errorf("b's main %q after a commit in a, want %s", got, initial)
	}
	if b.Exists(object) {
		t.Errorf("a's commit %s is in b's objects", head)
	}
	if b.Exists("work.txt") {
		t.Error("a's work.txt is in b")
	}
	if tmpl.builds != 1 {
		t.Errorf("template built %d times, want 1", tmpl.builds)
	}
}
