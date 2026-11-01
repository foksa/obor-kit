package main

import (
	"encoding/json"
	"fmt"
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

// devMarketplace is main's marketplace, laid out like the kit's own (one "name" per line: v0.17.2's line-wise sed
// renamed each of them, T-645).
const devMarketplace = `{
  "name": "obor-kit-dev",
  "owner": { "name": "x" },
  "plugins": [
    {
      "name": "obor",
      "source": "./"
    }
  ]
}`

// v0172Marketplace is what v0.17.2's flip made of devMarketplace: every "name" set to obor-kit, the owner's and the
// plugin entry's too, so obor@obor-kit no longer resolved (T-645).
const v0172Marketplace = `{"name": "obor-kit","owner":{"name": "obor-kit"},"plugins":[{"name": "obor-kit","source":"./"}]}`

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
	writeFile(t, filepath.Join(dir, "scripts", "release-lib.sh"), string(treeFile(t, "scripts/release-lib.sh")))
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
	// A go that logs its argv and writes the -o path; `go test` fails while go-fail exists (a red make check), and a
	// release build (-o dist/obor-<v>-…) while build-fail exists (a red make release in the scratch worktree).
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
case $out in dist/obor-*) [ ! -f "`+filepath.Join(p.Base, "build-fail")+`" ] || exit 1 ;; esac
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
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "UNREVIEWED=") {
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
	return f.makeEnv(t, nil, args...)
}

// makeEnv is make with more environment (UNREVIEWED=… the way the owner types it: in the shell, not as a make variable).
func (f *releaseFixture) makeEnv(t *testing.T, extra []string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s", "-C", f.Root}, args...)...)
	cmd.Env = append(f.env(), extra...)
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

// closeReview commits "RV-<n> closed · <sha> · approved" on main (the shape obor review closes with): the reviewed
// point T-642 reads is the close commit itself, never the sha it names.
func (f *releaseFixture) closeReview(t *testing.T, n int, named string) string {
	t.Helper()
	writeFile(t, filepath.Join(f.Root, "docs", "reviews", fmt.Sprintf("RV-%d.md", n)), "# RV\n")
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", fmt.Sprintf("RV-%d closed · %s · approved", n, named))
	return f.Git(f.Root, "rev-parse", "HEAD")
}

// headShort is main's short sha: the sha a close commit names.
func (f *releaseFixture) headShort(t *testing.T) string {
	t.Helper()
	return f.Git(f.Root, "rev-parse", "--short", "HEAD")
}

// merge brings files in on a topic branch and merges it on main with subject: a first-parent merge commit.
func (f *releaseFixture) merge(t *testing.T, branch, subject string, files map[string]string) string {
	t.Helper()
	return f.mergeBody(t, branch, subject, "", files)
}

// mergeBody is merge with a message body after the subject: main's merge messages carry whole specs, so a body may quote
// a close line at the start of a line (T-642).
func (f *releaseFixture) mergeBody(t *testing.T, branch, subject, body string, files map[string]string) string {
	t.Helper()
	f.Git(f.Root, "checkout", "-q", "-b", branch)
	for name, content := range files {
		writeFile(t, filepath.Join(f.Root, name), content)
	}
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "work on "+branch)
	f.Git(f.Root, "checkout", "-q", "main")
	args := []string{"merge", "-q", "--no-ff", "-m", subject}
	if body != "" {
		args = append(args, "-m", body)
	}
	f.Git(f.Root, append(args, branch)...)
	if got := f.Git(f.Root, "log", "-1", "--format=%s"); got != subject {
		t.Fatalf("the merge's subject is %q, want %q", got, subject)
	}
	return f.Git(f.Root, "rev-parse", "HEAD")
}

// T-642: make release refuses while a merge after the last "RV-n closed" commit changes anything but docs, naming the
// tasks a review has not seen; --dry-run reports it too, and nothing runs or changes.
func TestMakeReleaseRefusesUnreviewed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *releaseFixture)
		want    string
	}{
		{"code merges after the close", func(t *testing.T, f *releaseFixture) {
			f.closeReview(t, 1, f.headShort(t))
			f.merge(t, "topic/a", "Merge T-5: code", map[string]string{"internal/a.go": "package internal\n"})
			f.merge(t, "topic/b", "Merge T-6: more", map[string]string{"scripts/b": "#!/bin/sh\n"})
		}, `unreviewed since RV-1: T-5, T-6 (run obor review, close it, then release; or UNREVIEWED="<why>" make release V=1.2.4)`},
		{"no close commit: every merge counts", func(t *testing.T, f *releaseFixture) {
			f.merge(t, "topic/c", "Merge T-7: code", map[string]string{"cmd/x.go": "package main\n"})
		}, `unreviewed since the first commit (no RV closed yet): T-7 (run obor review`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t)
			tc.prepare(t, f)
			head := f.Git(f.Root, "rev-parse", "HEAD")
			for _, args := range [][]string{{"V=1.2.4"}, {"V=1.2.4", "DRY=1"}} {
				out, failed := f.release(t, args...)
				if !failed || !strings.Contains(out, "FAIL "+tc.want) {
					t.Errorf("%q: want a FAIL %q (failed %v):\n%s", args, tc.want, failed, out)
				}
				f.untouched(t, "1.2.4", head)
			}
		})
	}
}

// T-642: a merge after the close whose files are all docs passes the check (docs/ of any kind, a .md anywhere), and so
// does a code merge the close commit came after.
func TestMakeReleaseAllowsDocsAndReviewedMerges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *releaseFixture)
	}{
		{"docs-only merge after the close", func(t *testing.T, f *releaseFixture) {
			f.closeReview(t, 1, f.headShort(t))
			f.merge(t, "topic/docs", "Merge T-8: docs", map[string]string{
				"docs/notes.md": "notes\n", "docs/diagram.png": "png\n", "NOTES.md": "top\n"})
		}},
		{"code merge after the sha the close names, before the close", func(t *testing.T, f *releaseFixture) {
			named := f.headShort(t)
			f.merge(t, "topic/a", "Merge T-5: code", map[string]string{"internal/a.go": "package internal\n"})
			f.closeReview(t, 1, named)
			f.merge(t, "topic/docs", "Merge T-9: docs", map[string]string{"docs/more.md": "more\n"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t)
			tc.prepare(t, f)
			head := f.Git(f.Root, "rev-parse", "HEAD")
			out, failed := f.release(t, "V=1.2.4", "DRY=1")
			if failed {
				t.Fatalf("the dry run was refused:\n%s", out)
			}
			if !strings.Contains(out, "would: set VERSION") {
				t.Errorf("the dry run does not pass the unreviewed check:\n%s", out)
			}
			f.untouched(t, "1.2.4", head)
		})
	}
}

// T-642: UNREVIEWED="<why>" (the owner's call) lets the release through, says what it lets through, and puts the reason
// in the tag message.
func TestMakeReleaseUnreviewedOverride(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.closeReview(t, 1, f.headShort(t))
	f.merge(t, "topic/a", "Merge T-5: code", map[string]string{"internal/a.go": "package internal\n"})
	out, failed := f.release(t, "V=1.2.4", "UNREVIEWED=the owner ships it anyway")
	if failed {
		t.Fatalf("UNREVIEWED must let the release through:\n%s", out)
	}
	if !strings.Contains(out, "unreviewed since RV-1: T-5 (UNREVIEWED: the owner ships it anyway)") {
		t.Errorf("the release does not say what it let through:\n%s", out)
	}
	msg := f.Git(f.Root, "tag", "-l", "--format=%(contents)", "v1.2.4")
	if !strings.Contains(msg, "Unreviewed: T-5 — the owner ships it anyway") {
		t.Errorf("the tag message does not carry the unreviewed line: %q", msg)
	}
	if !strings.Contains(msg, "obor v1.2.4") {
		t.Errorf("the tag message lost its subject: %q", msg)
	}
}

// T-642 review finding: only a subject is a close commit. A merge message carries a whole spec, which may quote a close
// line at the start of a line; that must not become the newest review (it would silently disable the check).
func TestMakeReleaseIgnoresCloseLinesInMergeBodies(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.closeReview(t, 1, f.headShort(t))
	body := "Spec quote:\n\nRV-99 closed · abc1234 · approved"
	f.mergeBody(t, "topic/a", "Merge T-5: code", body, map[string]string{"internal/a.go": "package internal\n"})
	// Precondition: the body really has the close line at a line start (else this is vacuous).
	if msg := f.Git(f.Root, "log", "-1", "--format=%B"); !strings.Contains(msg, "\nRV-99 closed · abc1234 · approved") {
		t.Fatalf("precondition: the merge body does not quote a close line: %q", msg)
	}
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "FAIL unreviewed since RV-1: T-5 (") {
		t.Fatalf("a close line in a merge body must not count as the review (failed %v):\n%s", failed, out)
	}
	f.untouched(t, "1.2.4", head)
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
	// T-645: the flip renames only the marketplace: the owner and the plugin entry keep main's names.
	tagMarket := f.Git(f.Root, "show", "v1.2.4:.claude-plugin/marketplace.json")
	var market struct {
		Name  string `json:"name"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Plugins []struct {
			Name string `json:"name"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(tagMarket), &market); err != nil {
		t.Fatalf("the tag's marketplace does not parse: %v\n%s", err, tagMarket)
	}
	if market.Name != "obor-kit" {
		t.Errorf("the tag's marketplace name is %q, want obor-kit: %s", market.Name, tagMarket)
	}
	if market.Owner.Name != "x" {
		t.Errorf("the tag's marketplace owner is %q, want main's x: %s", market.Owner.Name, tagMarket)
	}
	if len(market.Plugins) != 1 || market.Plugins[0].Name != "obor" {
		t.Errorf("the tag's plugin entries are %+v, want one named obor (plugin.json's name): %s", market.Plugins, tagMarket)
	}
	if want := releaseMarketplace; tagMarket != want {
		t.Errorf("the tag's marketplace = %s, want main's with only the top-level name flipped: %s", tagMarket, want)
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

// T-641: the release's nested makes start from a plain make environment. make exports its command-line variables
// (V=…) into every recipe and reads them back from MAKEFLAGS, so a nested `make check` that inherited them would run
// the gate's tests with V set, and their own `make release` would take the V= path (`make release V=x` fails its own
// check).
func TestMakeReleasePlainMakeEnv(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	caller, check := filepath.Join(f.Base, "caller.env"), filepath.Join(f.Base, "check.env")
	writeFile(t, filepath.Join(f.Root, "Makefile"),
		"release:\n"+
			"\t@printf 'V=[%s] MAKEFLAGS=[%s] UNREVIEWED=[%s]' \"$${V-}\" \"$${MAKEFLAGS-}\" \"$${UNREVIEWED-}\" > "+caller+"\n"+
			"\t@sh scripts/release \"$(V)\"\n"+
			"\ncheck:\n"+
			"\t@printf 'V=[%s] DRY=[%s] MAKEFLAGS=[%s] MFLAGS=[%s] MAKELEVEL=[%s] UNREVIEWED=[%s]' \"$${V-}\" \"$${DRY-}\" \"$${MAKEFLAGS-}\" \"$${MFLAGS-}\" \"$${MAKELEVEL-}\" \"$${UNREVIEWED-}\" > "+check+"\n"+
			"\t@exit 1\n")
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "a check that records the make environment it was run with")

	out, failed := f.makeEnv(t, []string{"UNREVIEWED=the owner ships it"}, "release", "V=1.2.4")
	if !failed || !strings.Contains(out, "make check failed") {
		t.Fatalf("the recording check must stop the release (failed %v):\n%s", failed, out)
	}
	// Precondition: the caller's make does leak its V and the owner's UNREVIEWED into the script's environment and V
	// into MAKEFLAGS (else this is vacuous).
	got := readFile(t, caller)
	if !strings.Contains(got, "V=[1.2.4]") || !strings.Contains(got, "MAKEFLAGS=[") || !strings.Contains(got, "V=1.2.4") ||
		!strings.Contains(got, "UNREVIEWED=[the owner ships it]") {
		t.Fatalf("precondition: the caller's make exports V and UNREVIEWED, got %q", got)
	}
	// T-643 (RV-70 #1): UNREVIEWED is scrubbed too, else the gate's own release tests run with the override.
	if got, want := readFile(t, check), "V=[] DRY=[] MAKEFLAGS=[] MFLAGS=[] MAKELEVEL=[1] UNREVIEWED=[]"; got != want {
		t.Errorf("the release's make check saw %q, want %q: the caller's make variables must not reach it", got, want)
	}
}

// shipFixture is a kit copy with an origin (a bare repo on disk) and stubs for gh, obor and install.sh, all of them
// logging outside the repo so the release's clean-tree check still holds.
type shipFixture struct {
	*releaseFixture
	origin   string
	ghlog    string
	verfile  string
	instlog  string
	labels   string // launchctl list's output for the stub
	ghstate  string // the stub gh's release: release, assets, draft
	projects string // `obor projects`' output for the stub (T-646)
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
		ghstate:        filepath.Join(f.Base, "gh-state"),
	}
	f.Git(f.Base, "init", "-q", "--bare", s.origin)
	f.Git(f.Root, "remote", "add", "origin", s.origin)
	// gh keeps the release in ghstate: "release" when it exists, "assets" its asset names, "draft" true for a draft.
	st := s.ghstate
	writeExec(t, filepath.Join(f.Root, "stub", "gh"), `#!/bin/bash
echo "$*" >> "`+s.ghlog+`"
st="`+st+`"
mkdir -p "$st"
case "$1 $2" in
  "release view")
    [ -f "$st/release" ] || { echo "release not found" >&2; exit 1; }
    case "$*" in
      *isDraft*) cat "$st/draft" 2> /dev/null || echo false ;;
      *assets*) cat "$st/assets" 2> /dev/null ;;
    esac ;;
  "release create")
    : > "$st/release"
    shift 3
    for a; do case $a in --*) ;; *) basename "$a" >> "$st/assets" ;; esac; done ;;
  "release upload")
    [ -f "$st/release" ] || exit 1
    shift 3
    for a; do basename "$a" >> "$st/assets"; done ;;
esac
`)
	writeFile(t, s.verfile, "v1.2.3\n")
	// obor: its version line, and `obor projects` as the machine's project list prints it (T-646).
	s.projects = filepath.Join(f.Base, "projects-out")
	writeExec(t, filepath.Join(f.Root, "stub", "obor"), `#!/bin/bash
if [ "$1" = projects ]; then
  cat "`+s.projects+`" 2> /dev/null
  exit 0
fi
echo "obor $(cat "`+s.verfile+`")"
`)
	// launchctl list: the labels the follow-up line reads (none until a test writes them).
	writeExec(t, filepath.Join(f.Root, "stub", "launchctl"), "#!/bin/bash\ncat \""+s.labels+"\" 2> /dev/null\n")
	// install.sh installs v1.2.4 unless install-broken exists (an install that does not land).
	writeExec(t, filepath.Join(f.Root, "install.sh"), "#!/bin/sh\necho ran >> \""+s.instlog+"\"\n"+
		"[ -f \""+filepath.Join(f.Base, "install-broken")+"\" ] || printf '%s\\n' v1.2.4 > \""+s.verfile+"\"\n")
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

// T-646: the restart reminder names the projects of the machine's list of obor projects, through obor projects, when
// that list exists (not launchd's labels): only the ones whose serve is up.
func TestMakeShipRestartNamesTheProjectList(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)
	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	writeFile(t, filepath.Join(f.Base, "home", ".obor", "projects.json"), "{}\n")
	writeFile(t, f.projects, "obor-kit /w/obor-kit serve up\naxis-alies /w/axis-alies serve up\nquiet /w/quiet serve down\n")
	// launchd knows another project: the list wins, so its reminder is not the one printed.
	writeFile(t, f.labels, "PID\tStatus\tLabel\n123\t0\tobor.serve.launchd-only\n")
	out, failed := f.make(t, "ship")
	if failed {
		t.Fatalf("make ship failed:\n%s", out)
	}
	if want := "restart serve: obor serve restart in obor-kit, axis-alies"; !strings.Contains(out, want) {
		t.Errorf("ship does not say %q:\n%s", want, out)
	}
	for _, name := range []string{"launchd-only", "quiet"} {
		if strings.Contains(out, name) {
			t.Errorf("the reminder names %s (a launchd label, a serve that is down):\n%s", name, out)
		}
	}
}
