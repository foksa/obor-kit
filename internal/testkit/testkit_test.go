package testkit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Isolate clears the check-slot variables the merge gate exports to `make check`: a test's heavy `obor run`
// inside a merge otherwise reuses the merge's real slot file. It also turns git's auto maintenance off: a
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

// New copies the binary's one template: two projects are independent copies, each with the template's commit
// on main, a commit in one is not in the other, and the template was built once for both. Refs and objects are read
// from the files (a fresh repo's are loose), not by git: the package's git budget. The commit is the one git process
// (commit -a of a tracked file).
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
	a.Write(".gitignore", ".task-done.md\nwork\n") // tracked, so one `git commit -a` is a commit
	a.Git(a.Root, "commit", "-q", "-am", "work")
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
	if got, want := b.Read(".gitignore"), ".task-done.md\n"; got != want {
		t.Errorf("b's .gitignore = %q after a's commit, want the template's %q", got, want)
	}
	if tmpl.builds != 1 {
		t.Errorf("template built %d times, want 1", tmpl.builds)
	}
}

// noHookTB is the test as New sees it with the hook off: New's t.Fatal is a panic here, so the test can read the
// message it would have stopped with.
type noHookTB struct{ testing.TB }

func (noHookTB) Helper() {}

func (noHookTB) Fatal(args ...any)                 { panic(fmt.Sprint(args...)) }
func (noHookTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

// New in a test binary whose TestMain did not call Main/Run fails at the template's first use, naming the fix:
// nothing would remove the template then, and a silent build leaks an hk-tmpl* dir per run.
func TestNewWithoutTheHookFails(t *testing.T) {
	hooked.Store(false)
	defer hooked.Store(true)
	var got string
	func() {
		defer func() {
			if r := recover(); r != nil {
				got = fmt.Sprint(r)
			}
		}()
		New(noHookTB{t})
	}()
	if got == "" {
		t.Fatal("New succeeded without a TestMain that calls testkit.Main or testkit.Run")
	}
	for _, want := range []string{"TestMain", "testkit.Main"} {
		if !strings.Contains(got, want) {
			t.Errorf("New failed with %q, want the message to name %q", got, want)
		}
	}
}

// Run removes the template New copies: a child test binary gives the binary a template base (a dir of the template's
// shape) and writes it down, and after the child exits nothing is left there. This package's own run cannot
// show it: its template must live for its tests. The child builds no template, so the check starts no git;
// that New builds it once is TestNewCopiesOneTemplate's.
func TestRunRemovesTheTemplate(t *testing.T) {
	out := filepath.Join(t.TempDir(), "base")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTemplateChild$")
	cmd.Env = append(os.Environ(), "TESTKIT_TEMPLATE_CHILD="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v\n%s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(b))
	if base == "" {
		t.Fatal("child wrote no template base")
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Errorf("the child's template %s is still there after its run (stat: %v); Run must remove it", base, err)
	}
}

// TestTemplateChild is TestRunRemovesTheTemplate's child: it sets the template base (a dir with the template's proj
// and its .gitignore, no git) and writes where; the TestMain's Run removes it after the test.
func TestTemplateChild(t *testing.T) {
	out := os.Getenv("TESTKIT_TEMPLATE_CHILD")
	if out == "" {
		t.Skip("the child of TestRunRemovesTheTemplate")
	}
	base, err := os.MkdirTemp("", "hk-tmpl")
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".gitignore"), []byte(".task-done.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpl.base = base
	if err := os.WriteFile(out, []byte(base+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestScript: Script writes the body as an executable, and the same body again is the same file (a hardlink to the one
// cached copy: macOS scans a fresh executable on its first exec).
func TestScript(t *testing.T) {
	body := "#!/bin/sh\necho hi\n"
	one := Script(t, t.TempDir(), "stub", body)
	two := Script(t, filepath.Join(t.TempDir(), "sub"), "stub", body)
	info, err := os.Stat(one)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("%s is not executable (mode %v)", one, info.Mode())
	}
	if got, err := os.ReadFile(two); err != nil || string(got) != body {
		t.Errorf("%s: %q, %v; want the body", two, got, err)
	}
	twoInfo, err := os.Stat(two)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(info, twoInfo) {
		t.Errorf("the same body is two files: %s and %s (the scan is paid twice)", one, two)
	}
	other := Script(t, t.TempDir(), "stub", "#!/bin/sh\necho bye\n")
	otherInfo, err := os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(info, otherInfo) {
		t.Errorf("a different body is the same file %s", other)
	}
}
