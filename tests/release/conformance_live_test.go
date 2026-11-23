//go:build release

package release

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// conformance-live (commands.conformance_live, the nightly's non-gating line): agy updates itself, so its live
// conformance tier runs only when `agy --version` differs from the version its last green live run recorded, and
// prints the one line the nightly shows. The live tier is never run here: make is a stub that logs its argv and
// prints a conformance summary, agy is a fake that prints a version, and git is a stub that answers the common-dir
// lookup.
//
// conformanceLiveFixture is a plain project dir (no git) for `make conformance-live`: the tree's Makefile and
// scripts/conformance-live in it, its record under .obor/state (which the script finds through git), a stub make (a
// live-fail file in Base makes it fail), a stub dir where a test writes its agy, and the stub git that answers the
// common-dir path.
type conformanceLiveFixture struct {
	Base    string // the test's temp dir
	Root    string // the plain project dir, <Base>/proj
	stub    string // the stub dir: first on PATH
	makeLog string // the stub make's argv, one line per call
}

func newConformanceLiveFixture(t *testing.T) *conformanceLiveFixture {
	t.Helper()
	base := t.TempDir()
	f := &conformanceLiveFixture{Base: base, Root: filepath.Join(base, "proj"), stub: filepath.Join(base, "stub"), makeLog: filepath.Join(base, "make.log")}
	writeFile(t, f.Path("Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, f.Path("VERSION"), "1.2.3\n")
	writeExec(t, f.Path("scripts/conformance-live"), string(treeFile(t, "scripts/conformance-live")))
	f.git(t, ".git")
	writeExec(t, filepath.Join(f.stub, "make"), `#!/bin/sh
echo "$*" >> "`+f.makeLog+`"
if [ -f "`+filepath.Join(f.Base, "live-fail")+`" ]; then
  echo "conformance: 9 pass, 1 fail, 0 skip, 0 warn"
  echo "make: *** [conformance] Error 1" >&2
  exit 1
fi
echo "conformance: 9 pass, 0 fail, 0 skip, 0 warn"
`)
	return f
}

// git writes the fixture's stub git, which answers `rev-parse --git-common-dir` with common: `.git` for the plain
// project dir, as a main checkout answers it, or the main checkout's absolute .git path for a lane worktree.
func (f *conformanceLiveFixture) git(t *testing.T, common string) {
	t.Helper()
	writeExec(t, filepath.Join(f.stub, "git"), `#!/bin/sh
[ "$*" = "rev-parse --git-common-dir" ] || { echo "stub git: $*" >&2; exit 1; }
echo '`+common+`'
`)
}

// Path is rel in the fixture's project dir (an absolute rel as it is).
func (f *conformanceLiveFixture) Path(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(f.Root, rel)
}

// Read is the fixture file rel, "" when it does not exist.
func (f *conformanceLiveFixture) Read(rel string) string {
	b, _ := os.ReadFile(f.Path(rel))
	return string(b)
}

// Exists reports whether the fixture file rel exists.
func (f *conformanceLiveFixture) Exists(rel string) bool {
	_, err := os.Lstat(f.Path(rel))
	return err == nil
}

// agy writes the fixture's agy, whose --version prints line.
func (f *conformanceLiveFixture) agy(t *testing.T, line string) {
	t.Helper()
	writeExec(t, filepath.Join(f.stub, "agy"), "#!/bin/sh\necho '"+line+"'\n")
}

// env is the fixture's environment: the stub dir first on PATH, then the test's own PATH without every directory that
// holds an agy (the script must see the stub, or none, never the machine's).
func (f *conformanceLiveFixture) env(t *testing.T) []string {
	t.Helper()
	var env []string
	for _, kv := range plainMakeEnv() {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	path := []string{f.stub}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir != "" && !hasAgy(dir) {
			path = append(path, dir)
		}
	}
	return append(env, "PATH="+strings.Join(path, string(os.PathListSeparator)))
}

// hasAgy reports whether dir holds an executable agy.
func hasAgy(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "agy"))
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

// live runs `make -s conformance-live` in the fixture with env: the real make (found on the test process's PATH, the
// recipe's `make` is the stub) and the fixture's own PATH.
func (f *conformanceLiveFixture) live(t *testing.T, env []string) (string, bool) {
	t.Helper()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("no make: %v", err)
	}
	cmd := exec.Command(makePath, "-s", "-C", f.Root, "conformance-live")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

// shell is sh -c cmd in env: how a test asks the fixture's PATH what a command resolves to.
func shell(t *testing.T, env []string, cmdline string) (string, bool) {
	t.Helper()
	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// wantAgyVersion asserts the precondition that env's PATH finds the fixture's agy printing line.
func (f *conformanceLiveFixture) wantAgyVersion(t *testing.T, env []string, line string) {
	t.Helper()
	got, ok := shell(t, env, "agy --version")
	if !ok || got != line {
		t.Fatalf("precondition: agy --version = %q (ok %v), want the fixture's %q", got, ok, line)
	}
}

func TestOborKitConformanceLive(t *testing.T) {
	t.Parallel()
	t.Run("the version its last run recorded: one skip line, no make", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.14")
		writeFile(t, f.Path(".obor/state/agy-live-version"), "1.5.14\n")
		env := f.env(t)
		f.wantAgyVersion(t, env, "1.5.14")
		out, failed := f.live(t, env)
		if failed {
			t.Fatalf("make conformance-live failed on an unchanged version:\n%s", out)
		}
		if got := strings.TrimSpace(out); got != "conformance_live: agy 1.5.14 unchanged, live tier skipped" {
			t.Errorf("output %q, want the one skip line", got)
		}
		if f.Exists(f.makeLog) {
			t.Errorf("the live tier ran on an unchanged version: make %q", readFileOr(f.makeLog))
		}
		if got := f.Read(".obor/state/agy-live-version"); got != "1.5.14\n" {
			t.Errorf("the record is %q, want the version it held", got)
		}
	})
	t.Run("a different version: the live tier, then the version recorded", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.15")
		writeFile(t, f.Path(".obor/state/agy-live-version"), "1.5.14\n")
		env := f.env(t)
		f.wantAgyVersion(t, env, "1.5.15")
		out, failed := f.live(t, env)
		if failed {
			t.Fatalf("make conformance-live failed:\n%s", out)
		}
		if got := strings.TrimSpace(readFileOr(f.makeLog)); got != "conformance TIER=live KIND=agy TURN=1" {
			t.Errorf("the live tier ran as make %q, want conformance TIER=live KIND=agy TURN=1", got)
		}
		if got := f.Read(".obor/state/agy-live-version"); got != "1.5.15\n" {
			t.Errorf("the record is %q after the run, want the version it ran at", got)
		}
		if line := decisionLine(out); line != "conformance_live: agy 1.5.15 live tier: conformance: 9 pass, 0 fail, 0 skip, 0 warn" {
			t.Errorf("the line the nightly shows is %q, want the live run's conformance summary", line)
		}
	})
	t.Run("never run: the live tier, then the version recorded", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.15")
		if f.Exists(".obor/state/agy-live-version") {
			t.Fatal("precondition: the fixture has no recorded version")
		}
		env := f.env(t)
		f.wantAgyVersion(t, env, "1.5.15")
		out, failed := f.live(t, env)
		if failed {
			t.Fatalf("make conformance-live failed with no recorded version:\n%s", out)
		}
		if got := strings.TrimSpace(readFileOr(f.makeLog)); got != "conformance TIER=live KIND=agy TURN=1" {
			t.Errorf("the live tier ran as make %q, want conformance TIER=live KIND=agy TURN=1", got)
		}
		if got := f.Read(".obor/state/agy-live-version"); got != "1.5.15\n" {
			t.Errorf("the record is %q after the run, want the version it ran at", got)
		}
		if line := decisionLine(out); line != "conformance_live: agy 1.5.15 live tier: conformance: 9 pass, 0 fail, 0 skip, 0 warn" {
			t.Errorf("the line the nightly shows is %q, want the live run's conformance summary", line)
		}
	})
	t.Run("no agy: one skip line, no make", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		env := f.env(t)
		if got, ok := shell(t, env, "command -v agy"); ok {
			t.Fatalf("precondition: the fixture's PATH finds an agy at %q", got)
		}
		out, failed := f.live(t, env)
		if failed {
			t.Fatalf("make conformance-live failed with no agy:\n%s", out)
		}
		if got := strings.TrimSpace(out); got != "conformance_live: agy not on PATH, live tier skipped" {
			t.Errorf("output %q, want the one skip line", got)
		}
		if f.Exists(f.makeLog) {
			t.Errorf("the live tier ran with no agy: make %q", readFileOr(f.makeLog))
		}
		if f.Exists(".obor/state/agy-live-version") {
			t.Errorf("no agy was recorded: %q", f.Read(".obor/state/agy-live-version"))
		}
	})
	t.Run("a failed live run: the nightly's WARN line is the summary, the record unchanged", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.15")
		writeFile(t, f.Path(".obor/state/agy-live-version"), "1.5.14\n")
		writeFile(t, filepath.Join(f.Base, "live-fail"), "")
		out, failed := f.live(t, f.env(t))
		if !failed {
			t.Fatalf("a failed live run must fail the line:\n%s", out)
		}
		if line := decisionLine(out); line != "conformance_live: agy 1.5.15 live tier: conformance: 9 pass, 1 fail, 0 skip, 0 warn" {
			t.Errorf("the line the nightly shows is %q, want the live run's conformance summary", line)
		}
		// make appends its failed-recipe trailer after the summary: a bare last line would lose it (the prefixed
		// line is what nonGatingLine reads).
		if last := lastLine(out); !makeErrorLine.MatchString(last) {
			t.Errorf("last line %q is not make's failed-recipe trailer:\n%s", last, out)
		}
		// A red run records nothing: the tier runs again at the next nightly, at the same version.
		if got := f.Read(".obor/state/agy-live-version"); got != "1.5.14\n" {
			t.Errorf("the record is %q after a failed run at 1.5.15, want the version it held", got)
		}
	})
	t.Run("a failed first run: no record", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.15")
		writeFile(t, filepath.Join(f.Base, "live-fail"), "")
		if f.Exists(".obor/state/agy-live-version") {
			t.Fatal("precondition: the fixture has no recorded version")
		}
		out, failed := f.live(t, f.env(t))
		if !failed {
			t.Fatalf("a failed live run must fail the line:\n%s", out)
		}
		if f.Exists(".obor/state/agy-live-version") {
			t.Errorf("a failed first run recorded %q, want no record", f.Read(".obor/state/agy-live-version"))
		}
	})
	t.Run("run in a lane worktree: the record is the main checkout's", func(t *testing.T) {
		t.Parallel()
		f := newConformanceLiveFixture(t)
		f.agy(t, "1.5.15")
		// obor nightly runs the command in its own worktree: a plain lane dir whose git common dir is the main
		// checkout's, so the record goes there.
		lane := filepath.Join(f.Base, "proj-wt", "L-01")
		if err := os.MkdirAll(lane, 0o755); err != nil {
			t.Fatal(err)
		}
		f.git(t, f.Path(".git"))
		if f.Exists(".obor/state/agy-live-version") {
			t.Fatal("precondition: the main checkout has no recorded version")
		}
		cmd := exec.Command("sh", f.Path("scripts/conformance-live"))
		cmd.Dir, cmd.Env = lane, f.env(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("scripts/conformance-live in the lane: %v\n%s", err, out)
		}
		if got := f.Read(".obor/state/agy-live-version"); got != "1.5.15\n" {
			t.Errorf("the main checkout's record is %q after the lane ran it, want the version it ran at", got)
		}
		// The worktree's own state dir would be reset and lost with it.
		if _, err := os.Stat(filepath.Join(lane, ".obor", "state", "agy-live-version")); !os.IsNotExist(err) {
			t.Error("the lane wrote the record into its own worktree, want the project's .obor/state")
		}
	})
}

// The kit's own nightly runs it: commands.conformance_live is the conformance-live target, and nightly.also_run
// names it (validate refuses a name that is no command, never one the nightly does not run).
func TestConformanceLiveIsANightlyCommand(t *testing.T) {
	t.Parallel()
	var cfg struct {
		Nightly struct {
			AlsoRun []string `json:"also_run"`
		} `json:"nightly"`
		Commands map[string]struct {
			Cmd     string `json:"cmd"`
			Timeout int    `json:"timeout"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(treeFile(t, ".obor/config.json"), &cfg); err != nil {
		t.Fatalf(".obor/config.json: %v", err)
	}
	c, ok := cfg.Commands["conformance_live"]
	if !ok {
		t.Fatal("precondition: .obor/config.json defines commands.conformance_live")
	}
	if c.Cmd != "make -s conformance-live" {
		t.Errorf("commands.conformance_live.cmd = %q, want make -s conformance-live", c.Cmd)
	}
	if c.Timeout <= 0 {
		t.Errorf("commands.conformance_live has no timeout (a live tier starts agents and calls the model)")
	}
	if !slices.Contains(cfg.Nightly.AlsoRun, "conformance_live") {
		t.Errorf("nightly.also_run = %q, want conformance_live in it", cfg.Nightly.AlsoRun)
	}
}
