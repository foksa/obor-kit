package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// Tests for T-640: `make release V=x.y.z` (scripts/release) and `make ship` (scripts/ship). The fixtures are kit copies
// in a temp repo with a fake go that writes its -o file, so `make check` and `make release` are fast and observable.

const devMarketplace = `{"name":"obor-kit-dev","owner":{"name":"x"},"plugins":[{"name":"obor","source":"./"}]}`

// releaseFixture is a kit copy ready for a release of 1.2.4 from VERSION 1.2.3: the Makefile, both scripts, the guide
// with its v1.2.4 heading, an old catalog, and a fake go. tmp is the TMPDIR its scratch worktree goes to.
type releaseFixture struct {
	*testkit.Project
	tmp string
	log string
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()
	p := testkit.New(t)
	dir := p.Root
	f := &releaseFixture{Project: p, tmp: testkit.Dir(t), log: filepath.Join(p.Base, "go.log")}
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeExec(t, filepath.Join(dir, "scripts", "release"), string(treeFile(t, "scripts/release")))
	writeExec(t, filepath.Join(dir, "scripts", "ship"), string(treeFile(t, "scripts/ship")))
	writeExec(t, filepath.Join(dir, "scripts", "publish"), string(treeFile(t, "scripts/publish")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, ".gitignore"), ".task-done.md\ndist/\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), devMarketplace+"\n")
	writeFile(t, filepath.Join(dir, "docs", "UPGRADE-v1.2.md"), "# Upgrading obor from v1.1 to v1.2\n\n## v1.2.4 (patch)\n")
	var catalog string
	for _, pl := range publishPlatforms {
		catalog += strings.Repeat("a", 64) + "  dist/obor-1.2.2-" + pl + "\n"
	}
	writeFile(t, filepath.Join(dir, "bin", "checksums.txt"), catalog)
	// A go that logs its argv and writes the -o path; `go test` fails while go-fail exists (a red make check).
	writeExec(t, filepath.Join(dir, "stub", "go"), `#!/bin/bash
echo "$*" >> "`+f.log+`"
out=
prev=
for a in "$@"; do
  [ "$prev" != -o ] || out=$a
  prev=$a
done
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  printf '%s/%s\n' "$GOOS" "$GOARCH" > "$out"
fi
[ "$1" != test ] || [ ! -f "`+filepath.Join(p.Base, "go-fail")+`" ]
`)
	for k, v := range map[string]string{"user.name": "obor test", "user.email": "obor-test@example.invalid", "commit.gpgsign": "false"} {
		p.Git(dir, "config", k, v)
	}
	p.Git(dir, "add", "-A")
	p.Git(dir, "commit", "-q", "-m", "kit")
	return f
}

func (f *releaseFixture) env() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"PATH="+filepath.Join(f.Root, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+f.tmp,
		"HOME="+filepath.Join(f.Base, "home"))
}

// make runs `make -s -C root <args…>` with the fixture's go stub first on PATH.
func (f *releaseFixture) make(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s", "-C", f.Root}, args...)...)
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

func (f *releaseFixture) release(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	return f.make(t, append([]string{"release"}, args...)...)
}

// hasTag reports whether the fixture's repo has tag.
func (f *releaseFixture) hasTag(tag string) bool {
	return exec.Command("git", "-C", f.Root, "rev-parse", "-q", "--verify", "refs/tags/"+tag).Run() == nil
}

// untouched asserts a refused or dry run did nothing: same HEAD, no tag, no go calls, no worktree, VERSION as it was.
func (f *releaseFixture) untouched(t *testing.T, v, head string) {
	t.Helper()
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s", head, got)
	}
	if f.hasTag("v" + v) {
		t.Errorf("tag v%s was created", v)
	}
	if f.Exists(f.log) {
		t.Errorf("go ran: %s", readFile(t, f.log))
	}
	if f.Exists(filepath.Join(f.tmp, "obor-release-"+v)) {
		t.Errorf("the scratch worktree was created")
	}
	if got := strings.TrimSpace(f.Read("VERSION")); got != "1.2.3" {
		t.Errorf("VERSION became %q", got)
	}
}

// T-640: make release refuses off main, a dirty tree, a V not greater than VERSION, and a guide without its heading;
// each refusal leaves the repo as it was (nothing committed, no tag, no check, no worktree).
func TestMakeReleaseRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		version string
		prepare func(t *testing.T, f *releaseFixture)
		want    string
	}{
		{"not main", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.Git(f.Root, "checkout", "-q", "-b", "other")
		}, "not main"},
		{"dirty tree", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.Write("notes.txt", "uncommitted\n")
		}, "dirty"},
		{"V not greater", "1.2.2", func(t *testing.T, f *releaseFixture) {}, "must be greater"},
		{"no guide", "1.3.1", func(t *testing.T, f *releaseFixture) {}, "docs/UPGRADE-v1.3.md"},
		{"guide without this version's heading", "1.2.5", func(t *testing.T, f *releaseFixture) {}, "no v1.2.5 heading"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t)
			head := f.Git(f.Root, "rev-parse", "HEAD")
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			out, failed := f.release(t, "V="+tc.version)
			if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, tc.want) {
				t.Errorf("want a FAIL naming %q (failed %v):\n%s", tc.want, failed, out)
			}
			f.untouched(t, tc.version, head)
		})
	}
}

// T-640: a full run makes the two commits on main and tags v1.2.4 (marketplace obor-kit there, obor-kit-dev on main),
// with the four binaries and their catalog copied back to main and the worktree removed.
func TestMakeReleaseFullRun(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	if !strings.Contains(out, "next (owner): make ship") {
		t.Errorf("the run does not end with the owner's next step:\n%s", out)
	}
	if got := strings.Split(f.Git(f.Root, "log", "--format=%s", "-3"), "\n"); !slices.Equal(got,
		[]string{"checksums: v1.2.4", "obor v1.2.4: VERSION, plugin version", "kit"}) {
		t.Errorf("main's commits = %q, want the version commit then the catalog commit", got)
	}
	if got := strings.TrimSpace(f.Read("VERSION")); got != "1.2.4" {
		t.Errorf("main's VERSION = %q, want 1.2.4", got)
	}
	if got := f.Read(".claude-plugin/marketplace.json"); !strings.Contains(got, "obor-kit-dev") {
		t.Errorf("main's marketplace name is gone from %q", got)
	}
	if got := f.Git(f.Root, "show", "v1.2.4:.claude-plugin/marketplace.json"); !strings.Contains(got, `"name": "obor-kit"`) {
		t.Errorf("the tag's marketplace name is not obor-kit: %q", got)
	}
	if got := f.Git(f.Root, "show", "v1.2.4:VERSION"); got != "1.2.4" {
		t.Errorf("the tag's VERSION = %q, want 1.2.4", got)
	}
	if exec.Command("git", "-C", f.Root, "merge-base", "--is-ancestor", "v1.2.4", "main").Run() == nil {
		t.Error("the tag's commit is on main: main must keep only the catalog")
	}
	catalog := f.Git(f.Root, "show", "v1.2.4:bin/checksums.txt")
	main := strings.Split(strings.TrimSpace(catalog), "\n")
	if len(main) != 8 {
		t.Errorf("the tag's catalog has %d lines, want 4 old and 4 new:\n%s", len(main), catalog)
	}
	for _, pl := range publishPlatforms {
		if !strings.Contains(catalog, "dist/obor-1.2.2-"+pl) {
			t.Errorf("the tag's catalog drops 1.2.2's %s line:\n%s", pl, catalog)
		}
	}
	if got := strings.TrimSpace(f.Read("bin/checksums.txt")); got != catalog {
		t.Errorf("main's bin/checksums.txt is not the tag's catalog:\n%s", got)
	}
	for _, line := range main {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("catalog line %q", line)
		}
		if !strings.Contains(fields[1], "dist/obor-1.2.4-") {
			continue // the older version's files are not built here, only kept
		}
		if got := fileSum(t, filepath.Join(f.Root, fields[1])); got != fields[0] {
			t.Errorf("%s is sha256 %s, not its catalog line's %s", fields[1], got, fields[0])
		}
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("main is dirty after the release: %q", got)
	}
	if f.Exists(filepath.Join(f.tmp, "obor-release-1.2.4")) {
		t.Error("the scratch worktree is still there")
	}
	if log := readFile(t, f.log); !strings.Contains(log, "build ") || !strings.Contains(log, "test ") {
		t.Errorf("the release did not run the full make check (go got: %s)", log)
	}
}

// T-640: a re-run after a failure resumes: the version commit and the local tag are skipped, and a run interrupted
// after the tag makes only the catalog commit, without rebuilding.
func TestMakeReleaseResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	goCalls := strings.Count(readFile(t, f.log), "\n")
	head := f.Git(f.Root, "rev-parse", "HEAD")

	out, failed := f.release(t, "V=1.2.4")
	if failed {
		t.Fatalf("the re-run failed:\n%s", out)
	}
	for _, want := range []string{"commit skipped", "build and tag skipped", "copy-back commit skipped", "next (owner): make ship"} {
		if !strings.Contains(out, want) {
			t.Errorf("the re-run does not say %q:\n%s", want, out)
		}
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("the re-run committed: HEAD %s, was %s", got, head)
	}
	if n := strings.Count(readFile(t, f.log), "\n"); n != goCalls {
		t.Errorf("the re-run ran go %d more times, want the check skipped", n-goCalls)
	}

	// The tag is there but the copy-back commit is not: main rebuilt from the tag's files only.
	f.Git(f.Root, "reset", "--hard", "HEAD~1")
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("the resume failed:\n%s", out)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "checksums: v1.2.4" {
		t.Errorf("the resume's commit = %q, want checksums: v1.2.4", got)
	}
	if n := strings.Count(readFile(t, f.log), "\n"); n != goCalls {
		t.Errorf("the resume rebuilt (%d more go calls), want the tag skipped", n-goCalls)
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("main is dirty after the resume: %q", got)
	}
}

// T-640: a red make check stops the release with nothing committed and puts VERSION and the plugin version back, so
// the re-run (check green) starts from a clean tree and finishes the release.
func TestMakeReleaseCheckFailsAndResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail"), "")
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "make check failed: nothing is committed") {
		t.Fatalf("a red check must stop the release and commit nothing (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s on a red check", got)
	}
	if f.hasTag("v1.2.4") {
		t.Error("a red check made the tag")
	}
	if got := f.Git(f.Root, "show", "HEAD:VERSION"); got != "1.2.3" {
		t.Errorf("HEAD:VERSION = %q, want the release's 1.2.3", got)
	}
	if got := strings.TrimSpace(f.Read("VERSION")); got != "1.2.3" {
		t.Errorf("a red check left VERSION at %q, want 1.2.3 restored", got)
	}
	if got := f.Read(".claude-plugin/plugin.json"); strings.Contains(got, "1.2.4") {
		t.Errorf("a red check left the plugin version bumped: %q", got)
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("a red check left the tree dirty: %q", got)
	}

	if err := os.Remove(filepath.Join(f.Base, "go-fail")); err != nil {
		t.Fatal(err)
	}
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("the resume after a green check failed:\n%s", out)
	}
	if got := f.Git(f.Root, "log", "--format=%s", "-1"); got != "checksums: v1.2.4" {
		t.Errorf("the resume's last commit = %q, want the catalog commit", got)
	}
	if !f.hasTag("v1.2.4") {
		t.Error("the resume did not tag v1.2.4")
	}
}

// T-640: --dry-run (make release V=… DRY=1) prints the steps and changes nothing.
func TestMakeReleaseDryRun(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4", "DRY=1")
	if failed {
		t.Fatalf("the dry run failed:\n%s", out)
	}
	for _, want := range []string{"would: set VERSION", "would: make check", "would: git commit", "would: git worktree add",
		"would: copy the four dist", "next (owner): make ship"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not print %q:\n%s", want, out)
		}
	}
	f.untouched(t, "1.2.4", head)
}

// T-640: a re-run whose dist files no longer match the local tag's catalog is refused with what to do: the tag is
// never moved, and nothing is committed.
func TestMakeReleaseRefusesChangedBytes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	head := f.Git(f.Root, "rev-parse", "HEAD")
	tagBefore := f.Git(f.Root, "rev-parse", "v1.2.4")
	writeFile(t, filepath.Join(f.Root, "dist", "obor-1.2.4-darwin-arm64"), "rebuilt\n")

	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "dist/obor-1.2.4-darwin-arm64") || !strings.Contains(out, "git tag -d v1.2.4") {
		t.Errorf("a changed file must be refused naming it and the way out (failed %v):\n%s", failed, out)
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := f.Git(f.Root, "rev-parse", "v1.2.4"); got != tagBefore {
		t.Errorf("the tag moved from %s to %s", tagBefore, got)
	}
}

// T-640: ship refuses a tag that is not a finished release before it pushes anything: a pushed tag never moves, so a
// wrong tag (here: main's dev marketplace) must stop on this machine.
func TestMakeShipRefusesWrongTagBeforePush(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	// The tag now names main's catalog commit: VERSION and the catalog are right, the marketplace is obor-kit-dev.
	f.Git(f.Root, "tag", "-f", "v1.2.4", "main")
	out, failed := f.make(t, "ship")
	if !failed || !strings.Contains(out, "marketplace.json name at v1.2.4 is 'obor-kit-dev'") {
		t.Errorf("ship must refuse the wrong tag naming it (failed %v):\n%s", failed, out)
	}
	for _, ref := range []string{"refs/heads/main", "refs/tags/v1.2.4"} {
		if exec.Command("git", "-C", f.origin, "rev-parse", "-q", "--verify", ref).Run() == nil {
			t.Errorf("%s was pushed despite the wrong tag", ref)
		}
	}
	if _, err := os.Stat(f.instlog); err == nil {
		t.Error("install.sh ran despite the wrong tag")
	}
}

// shipFixture is a kit copy with an origin (a bare repo on disk) and stubs for gh, obor and install.sh, all of them
// logging outside the repo so the release's clean-tree check still holds.
type shipFixture struct {
	*releaseFixture
	origin  string
	ghlog   string
	verfile string
	instlog string
	labels  string // launchctl list's output for the stub
}

func newShipFixture(t *testing.T) *shipFixture {
	t.Helper()
	f := newReleaseFixture(t)
	s := &shipFixture{
		releaseFixture: f,
		origin:         filepath.Join(f.Base, "origin.git"),
		ghlog:          filepath.Join(f.Base, "gh.log"),
		verfile:        filepath.Join(f.Base, "installed-version"),
		instlog:        filepath.Join(f.Base, "install.log"),
		labels:         filepath.Join(f.Base, "launchctl-list"),
	}
	f.Git(f.Base, "init", "-q", "--bare", s.origin)
	f.Git(f.Root, "remote", "add", "origin", s.origin)
	marker := filepath.Join(f.Base, "gh-release")
	writeExec(t, filepath.Join(f.Root, "stub", "gh"), `#!/bin/bash
echo "$*" >> "`+s.ghlog+`"
case "$1 $2" in
  "release view") [ -f "`+marker+`" ] ;;
  "release create") : > "`+marker+`" ;;
esac
`)
	writeFile(t, s.verfile, "v1.2.3\n")
	writeExec(t, filepath.Join(f.Root, "stub", "obor"), "#!/bin/bash\necho \"obor $(cat "+s.verfile+")\"\n")
	// launchctl list: the labels the follow-up line reads (none until a test writes them).
	writeExec(t, filepath.Join(f.Root, "stub", "launchctl"), "#!/bin/bash\ncat \""+s.labels+"\" 2> /dev/null\n")
	writeExec(t, filepath.Join(f.Root, "install.sh"), "#!/bin/sh\necho ran >> \""+s.instlog+"\"\nprintf '%s\\n' v1.2.4 > \""+s.verfile+"\"\n")
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "stubs")
	return s
}

// T-640: make ship pushes main and the tag, publishes and installs; a second run skips all three; a ship without a tag
// refuses.
func TestMakeShip(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)

	out, failed := f.make(t, "ship")
	if !failed || !strings.Contains(out, "no tag v1.2.3") {
		t.Errorf("ship without a tag must refuse naming it (failed %v):\n%s", failed, out)
	}

	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	out, failed = f.make(t, "ship")
	if failed {
		t.Fatalf("make ship failed:\n%s", out)
	}
	for _, want := range []string{"pushed main and v1.2.4", "published v1.2.4", "installed the kit",
		"restart serve: obor serve restart in each project"} {
		if !strings.Contains(out, want) {
			t.Errorf("ship does not say %q:\n%s", want, out)
		}
	}
	if got, want := f.Git(f.origin, "rev-parse", "refs/heads/main"), f.Git(f.Root, "rev-parse", "refs/heads/main"); got != want {
		t.Errorf("origin main = %s, want the local %s", got, want)
	}
	if got, want := f.Git(f.origin, "rev-parse", "refs/tags/v1.2.4"), f.Git(f.Root, "rev-parse", "refs/tags/v1.2.4"); got != want {
		t.Errorf("origin's v1.2.4 = %s, want the local %s", got, want)
	}
	gh := readFile(t, f.ghlog)
	if !strings.Contains(gh, "release create v1.2.4 --verify-tag dist/obor-1.2.4-darwin-arm64") {
		t.Errorf("gh was not given the release create:\n%s", gh)
	}
	if n := strings.Count(readFile(t, f.instlog), "\n"); n != 1 {
		t.Errorf("install.sh ran %d times, want once", n)
	}

	ghBefore, instBefore := gh, readFile(t, f.instlog)
	head := f.Git(f.Root, "rev-parse", "HEAD")
	// launchd's view of this machine's serves: the follow-up names those projects instead of saying "each project".
	writeFile(t, f.labels, "PID\tStatus\tLabel\n-\t0\tcom.apple.something\n123\t0\tobor.serve.obor-kit\n456\t0\tobor.serve.axis-alies\n")
	out, failed = f.make(t, "ship")
	if failed {
		t.Fatalf("the second ship failed:\n%s", out)
	}
	for _, want := range []string{"push skipped", "publish skipped", "install skipped",
		"restart serve: obor serve restart in obor-kit, axis-alies"} {
		if !strings.Contains(out, want) {
			t.Errorf("the second ship does not say %q:\n%s", want, out)
		}
	}
	if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("the second ship committed: HEAD %s, was %s", got, head)
	}
	if n := strings.Count(readFile(t, f.ghlog), "release create"); n != strings.Count(ghBefore, "release create") {
		t.Errorf("the second ship published again (%d release create calls)", n)
	}
	if got := readFile(t, f.instlog); got != instBefore {
		t.Errorf("the second ship installed again: %q", got)
	}
}
