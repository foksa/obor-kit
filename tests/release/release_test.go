//go:build release

package release

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

// TestMain isolates the tests and removes the project template New copies.
func TestMain(m *testing.M) { testkit.Main(m) }

// `make release V=x.y.z` (scripts/release) and `make ship` (scripts/ship). The fixtures are kit copies
// in a temp repo with a fake go that writes its -o file, so `make check` and `make release` are fast and observable.

// devMarketplace is main's marketplace, laid out like the kit's own (one "name" per line, so a line-wise edit
// reaches each of them).
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
// plugin entry's too, so obor@obor-kit does not resolve.
const v0172Marketplace = `{"name": "obor-kit","owner":{"name": "obor-kit"},"plugins":[{"name": "obor-kit","source":"./"}]}`

// releaseFixture is a kit copy ready for a release of 1.2.4 from VERSION 1.2.3: the Makefile, both scripts, the guide
// with its v1.2.4 heading, an old catalog, and a fake go. tmp is the TMPDIR its temporary files go to.
type releaseFixture struct {
	*testkit.Project
	tmp string
	log string
}

// scratch is where release's scratch worktree lives: this repo's own git dir.
func (f *releaseFixture) scratch(v string) string {
	return filepath.Join(f.Root, ".git", "obor-release-"+v)
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
	writeFile(t, filepath.Join(dir, ".gitignore"), ".task-done.md\ndist/\n.obor/state/\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), devMarketplace+"\n")
	writeFile(t, filepath.Join(dir, "docs", "UPGRADE-v1.2.md"), "# Upgrading obor from v1.1 to v1.2\n\n## v1.2.4 (patch)\n")
	writeFile(t, filepath.Join(dir, "docs", "releases", "v1.2.4.md"), releaseNotes)
	var catalog string
	for _, pl := range publishPlatforms {
		catalog += strings.Repeat("a", 64) + "  dist/obor-1.2.2-" + pl + "\n"
	}
	writeFile(t, filepath.Join(dir, "bin", "checksums.txt"), catalog)
	// A go that logs its argv and writes the -o path; `go test` fails while go-fail exists (a red make check), and a
	// release build (-o dist/obor-<v>-… or a kind's dist/obor-kind-<kind>-<v>-…) while build-fail exists (a red make
	// release in the scratch worktree).
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
case $out in dist/obor-[0-9]*|dist/obor-kind-*-[0-9]*) [ ! -f "`+filepath.Join(p.Base, "build-fail")+`" ] || exit 1 ;; esac
# go-fail-once: the first go test fails with the file's text as its output and removes the file (a flaky package);
# go-fail: every go test fails with the file's text (a red package); go-vet-fail: go vet fails; upgrade-fail: the upgrade
# check's go test (./tests/upgrade, make upgrade-check) fails with the file's text; check-release-fail: check-release's
# go test (./tests/release) fails with it.
if [ "$1" = test ]; then
  case "$*" in *./tests/upgrade*) [ ! -f "`+p.Base+`/upgrade-fail" ] || { cat "`+p.Base+`/upgrade-fail"; exit 1; } ;; esac
  case "$*" in *./tests/release*) [ ! -f "`+p.Base+`/check-release-fail" ] || { cat "`+p.Base+`/check-release-fail"; exit 1; } ;; esac
  for m in go-fail-once go-fail; do
    if [ -f "`+p.Base+`/$m" ]; then
      cat "`+p.Base+`/$m"
      [ "$m" != go-fail-once ] || rm -f "`+p.Base+`/$m"
      exit 1
    fi
  done
fi
[ "$1" != vet ] || [ ! -f "`+filepath.Join(p.Base, "go-vet-fail")+`" ]
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
	for _, kv := range plainMakeEnv() {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "UNREVIEWED=") || strings.HasPrefix(kv, "SKIP_UPGRADE_CHECK=") {
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
	if f.Exists(f.scratch(v)) {
		t.Errorf("the scratch worktree was created")
	}
	if got := strings.TrimSpace(f.Read("VERSION")); got != "1.2.3" {
		t.Errorf("VERSION became %q", got)
	}
}

// closeReview commits "RV-<n> closed · <sha> · approved" on main (the shape obor review closes with): the reviewed
// point release reads is the close commit itself, never the sha it names.
func (f *releaseFixture) closeReview(t *testing.T, n int, named string) string {
	t.Helper()
	return f.closeReviewCovered(t, n, named, "")
}

// closeReviewCovered is closeReview with the body obor review writes: the cycle's covered tasks and where its review started
// (start "": a close commit with no body); covered nil: none covered.
func (f *releaseFixture) closeReviewCovered(t *testing.T, n int, named, start string, covered ...string) string {
	t.Helper()
	writeFile(t, filepath.Join(f.Root, "docs", "reviews", fmt.Sprintf("RV-%d.md", n)), "# RV\n")
	f.Git(f.Root, "add", "-A")
	args := []string{"commit", "-q", "-m", fmt.Sprintf("RV-%d closed · %s · approved", n, named)}
	if start != "" {
		args = append(args, "-m", "Covered: "+strings.Join(covered, ", ")+"\nStart: "+start)
	}
	f.Git(f.Root, args...)
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
// a close line at the start of a line.
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

// a merge after the close whose files are all docs passes the check (docs/ of any kind, a .md anywhere), and so
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

// UNREVIEWED="<why>" (the owner's call) lets the release through, says what it lets through, and puts the reason
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

// make release's refusals share one fixture, a row each: a detached HEAD (the release
// commits land on the branch the main checkout has), a dirty tree, a V not greater
// than VERSION, a guide missing or without this version's heading, unreviewed merges (with and without a close, and
// --dry-run reporting them too), a close line quoted in a merge body (only a subject is a close commit), and a foreign
// directory at the scratch path. Each refusal leaves the repo as it was: nothing committed, no tag, no check, no
// worktree.
func TestMakeReleaseRefusals(t *testing.T) {
	f := newReleaseFixture(t)
	base := f.Git(f.Root, "rev-parse", "HEAD")
	for _, tc := range []struct {
		name    string
		version string
		prepare func(t *testing.T, f *releaseFixture)
		want    string
		dry     bool // the dry run reports the same refusal
	}{
		{"detached HEAD", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.Git(f.Root, "checkout", "-q", "--detach")
		}, "on a detached HEAD, not a branch", false},
		{"dirty tree", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.Write("notes.txt", "uncommitted\n")
		}, "dirty", false},
		{"V not greater", "1.2.2", nil, "must be greater", false},
		{"no guide", "1.3.1", nil, "docs/UPGRADE-v1.3.md", false},
		{"guide without this version's heading", "1.2.5", nil, "no v1.2.5 heading", false},
		{"unreviewed: no close commit, every merge counts", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.merge(t, "topic/c", "Merge T-7: code", map[string]string{"cmd/x.go": "package main\n"})
		}, `unreviewed since the first commit (no RV closed yet): T-7 (run obor review`, false},
		{"unreviewed: code merges after the close", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.closeReview(t, 1, f.headShort(t))
			f.merge(t, "topic/a", "Merge T-5: code", map[string]string{"internal/a.go": "package internal\n"})
			f.merge(t, "topic/b", "Merge T-6: more", map[string]string{"scripts/b": "#!/bin/sh\n"})
		}, `unreviewed since RV-1: T-5, T-6 (run obor review, close it, then release; or UNREVIEWED="<why>" make release V=1.2.4)`, true},
		{"close line in a merge body", "1.2.4", func(t *testing.T, f *releaseFixture) {
			f.closeReview(t, 1, f.headShort(t))
			body := "Spec quote:\n\nRV-99 closed · abc1234 · approved"
			f.mergeBody(t, "topic/e", "Merge T-20: code", body, map[string]string{"internal/e.go": "package internal\n"})
			// Precondition: the body really has the close line at a line start (else this is vacuous).
			if msg := f.Git(f.Root, "log", "-1", "--format=%B"); !strings.Contains(msg, "\nRV-99 closed · abc1234 · approved") {
				t.Fatalf("precondition: the merge body does not quote a close line: %q", msg)
			}
		}, `unreviewed since RV-1: T-20 (`, false},
		{"foreign scratch", "1.2.4", func(t *testing.T, f *releaseFixture) {
			writeFile(t, filepath.Join(f.scratch("1.2.4"), "someone-elses.txt"), "keep me\n")
		}, "is not this repo's worktree", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				f.Git(f.Root, "checkout", "-q", "-f", "main")
				f.Git(f.Root, "reset", "-q", "--hard", base)
				f.Git(f.Root, "clean", "-qfd")
				for _, v := range []string{"1.2.2", "1.2.4", "1.2.5", "1.3.1"} {
					os.RemoveAll(f.scratch(v))
				}
				os.Remove(f.log)
			})
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			head := f.Git(f.Root, "rev-parse", "HEAD")
			args := [][]string{{"V=" + tc.version}}
			if tc.dry {
				args = append(args, []string{"V=" + tc.version, "DRY=1"})
			}
			for _, a := range args {
				out, failed := f.release(t, a...)
				if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, tc.want) {
					t.Errorf("%q: want a FAIL naming %q (failed %v):\n%s", a, tc.want, failed, out)
					continue
				}
				if tc.name == "foreign scratch" {
					if !strings.Contains(out, f.scratch(tc.version)) {
						t.Errorf("the refusal does not name the foreign directory %s:\n%s", f.scratch(tc.version), out)
					}
					if got := readFile(t, filepath.Join(f.scratch(tc.version), "someone-elses.txt")); got != "keep me\n" {
						t.Errorf("the foreign directory's file is %q, want it untouched", got)
					}
					if f.hasTag("v" + tc.version) {
						t.Error("the refused release made the tag")
					}
					if got := f.Git(f.Root, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
						t.Errorf("the refused release registered a worktree:\n%s", got)
					}
					continue
				}
				f.untouched(t, tc.version, head)
			}
		})
	}
}

// a full run makes the two commits on main and tags v1.2.4 (marketplace obor-kit there, obor-kit-dev on main),
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
	// the flip renames only the marketplace: the owner and the plugin entry keep main's names.
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
	if len(main) != 12 {
		t.Errorf("the tag's catalog has %d lines, want 4 old and 8 new (obor and the OpenCode kind):\n%s", len(main), catalog)
	}
	for _, a := range releaseAssets("1.2.4") {
		if !strings.Contains(catalog, "  dist/"+a+"\n") && !strings.HasSuffix(catalog, "  dist/"+a) {
			t.Errorf("the tag's catalog has no line for %s:\n%s", a, catalog)
		}
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
		if !strings.Contains(fields[1], "-1.2.4-") {
			continue // the older version's files are not built here, only kept
		}
		if got := fileSum(t, filepath.Join(f.Root, fields[1])); got != fields[0] {
			t.Errorf("%s is sha256 %s, not its catalog line's %s", fields[1], got, fields[0])
		}
	}
	if got := f.Git(f.Root, "status", "--porcelain"); got != "" {
		t.Errorf("main is dirty after the release: %q", got)
	}
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the scratch worktree is still there")
	}
	if log := readFile(t, f.log); !strings.Contains(log, "build ") || !strings.Contains(log, "test ") {
		t.Errorf("the release did not run the full make check (go got: %s)", log)
	}
}

// a re-run after a failure resumes: the version commit and the local tag are skipped, and a run interrupted
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

	// The tag is there but the copy-back commit is not (main moved with it, as release does): main rebuilt from
	// the tag's files only.
	f.Git(f.Root, "reset", "--hard", "HEAD~1")
	f.Git(f.Root, "update-ref", "refs/heads/main", "HEAD")
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
	// a re-run with the tag already there cleans up its own worktree and leaves none behind.
	if f.Exists(f.scratch("1.2.4")) {
		t.Error("the re-run left a scratch worktree")
	}
}

// a red make check stops the release with nothing committed and puts VERSION and the plugin version back, so
// the re-run (check green) starts from a clean tree and finishes the release.
func TestMakeReleaseCheckFailsAndResumes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.Write(filepath.Join(f.Base, "go-fail"), "")
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4")
	if !failed || !strings.Contains(out, "make check failed") || !strings.Contains(out, "nothing is committed") {
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

// --dry-run (make release V=… DRY=1) prints the steps and changes nothing.
func TestMakeReleaseDryRun(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed := f.release(t, "V=1.2.4", "DRY=1")
	if failed {
		t.Fatalf("the dry run failed:\n%s", out)
	}
	for _, want := range []string{"would: set VERSION", "would: make check", "would: git commit", "would: git worktree add",
		"would: copy the 8 dist", "next (owner): make ship"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not print %q:\n%s", want, out)
		}
	}
	f.untouched(t, "1.2.4", head)
}

// a re-run whose dist files do not match the local tag's catalog is refused with what to do: the tag is
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

// ship refuses a tag that is not a finished release before it pushes anything: a pushed tag never moves, so a
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

// the release's nested makes start from a plain make environment. make exports its command-line variables
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
	// UNREVIEWED is scrubbed too, else the gate's own release tests run with the override.
	if got, want := readFile(t, check), "V=[] DRY=[] MAKEFLAGS=[] MFLAGS=[] MAKELEVEL=[1] UNREVIEWED=[]"; got != want {
		t.Errorf("the release's make check saw %q, want %q: the caller's make variables must not reach it", got, want)
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
	ghstate string // the stub gh's release: release, assets, draft
	sent    string // the log of the stub's `obor send` calls
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
    val=
    for a; do
      if [ -n "$val" ]; then val=; continue; fi
      case $a in
        --title | --notes-file) val=1 ;;
        --*) ;;
        *) basename "$a" >> "$st/assets" ;;
      esac
    done ;;
  "release upload")
    [ -f "$st/release" ] || exit 1
    shift 3
    for a; do basename "$a" >> "$st/assets"; done ;;
esac
`)
	writeFile(t, s.verfile, "v1.2.3\n")
	// obor: its version line, `obor projects` (this kit and two others) and `obor send` of the release notes;
	// axis-alies' send fails while send-broken exists.
	s.sent = filepath.Join(f.Base, "sent")
	writeExec(t, filepath.Join(f.Root, "stub", "obor"), `#!/bin/bash
if [ "$1" = projects ]; then
  printf '%s\n' "kit $(pwd -P) serve up" "axis-alies /nowhere/axis-alies serve down" "other /nowhere/other serve down"
  exit 0
fi
if [ "$1" = send ]; then
  echo "$2: $3" >> "`+s.sent+`"
  [ "$2" != axis-alies ] || [ ! -f "`+filepath.Join(f.Base, "send-broken")+`" ] ||
    { echo "FAIL axis-alies: send: serve down"; exit 1; }
  echo "SENT to $2's lead"
  exit 0
fi
echo "obor $(cat "`+s.verfile+`")"
`)
	// install.sh installs v1.2.4 unless install-broken exists (an install that does not land).
	writeExec(t, filepath.Join(f.Root, "install.sh"), "#!/bin/sh\necho ran >> \""+s.instlog+"\"\n"+
		"[ -f \""+filepath.Join(f.Base, "install-broken")+"\" ] || printf '%s\\n' v1.2.4 > \""+s.verfile+"\"\n")
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "stubs")
	return s
}

// one ship run covers the family: a ship without a tag refuses; the ship after a release
// pushes main and the tag, publishes and installs, and ends by telling the leads (one `obor send <project> "<the tag's
// docs/releases/v<version>.md>"` per project of `obor projects` but the kit's own); a send that fails fails the ship
// after the follow line, naming the repair, and the other projects are sent to anyway; a second run skips push,
// publish and install and tells the leads again.
func TestMakeShip(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t)

	out, failed := f.make(t, "ship")
	if !failed || !strings.Contains(out, "no tag v1.2.3") {
		t.Errorf("ship without a tag must refuse naming it (failed %v):\n%s", failed, out)
	}
	if f.Exists(f.sent) {
		t.Errorf("the refused ship sent to a lead: %s", readFile(t, f.sent))
	}

	if out, failed := f.release(t, "V=1.2.4"); failed {
		t.Fatalf("make release V=1.2.4 failed:\n%s", out)
	}
	notes := f.Git(f.Root, "show", "v1.2.4:docs/releases/v1.2.4.md")
	if strings.TrimSpace(notes) == "" {
		t.Fatalf("precondition: the tag has release notes, got %q", notes)
	}
	writeFile(t, filepath.Join(f.Base, "send-broken"), "")
	out, failed = f.make(t, "ship")
	if !failed {
		t.Errorf("a send that fails must fail the ship (T-715, T-719):\n%s", out)
	}
	// The ship did its work and then failed at the tell: its four lines are all there, then the repair.
	for _, want := range []string{"pushed main up to " + f.headShort(t) + " (checksums: v1.2.4) and v1.2.4", "published v1.2.4",
		"installed the kit", "serves and web follow the new binary within a minute", "FAIL axis-alies: send: serve down",
		"obor send <project>"} {
		if !strings.Contains(out, want) {
			t.Errorf("a failing notify must leave %q after the reminder (failed %v):\n%s", want, failed, out)
		}
	}
	// The failing send did not stop the others: axis-alies' send was attempted and other's went out; the kit is not told.
	if got := readFile(t, f.sent); !strings.Contains(got, "axis-alies: obor v1.2.4 is installed. ") ||
		!strings.Contains(got, "other: obor v1.2.4 is installed. ") || strings.Contains(got, "kit: ") {
		t.Errorf("the failing ship's sends = %q, want axis-alies and other, not kit", got)
	}
	if got, want := f.Git(f.origin, "rev-parse", "refs/heads/main"), f.Git(f.Root, "rev-parse", "refs/heads/main"); got != want {
		t.Errorf("origin main = %s, want the local %s", got, want)
	}
	if got, want := f.Git(f.origin, "rev-parse", "refs/tags/v1.2.4"), f.Git(f.Root, "rev-parse", "refs/tags/v1.2.4"); got != want {
		t.Errorf("origin's v1.2.4 = %s, want the local %s", got, want)
	}
	gh := readFile(t, f.ghlog)
	if !strings.Contains(gh, "release create v1.2.4 --verify-tag --title obor 1.2.4 --notes-file ") ||
		!strings.Contains(gh, "dist/obor-1.2.4-darwin-arm64") {
		t.Errorf("gh was not given the release create:\n%s", gh)
	}
	if n := strings.Count(readFile(t, f.instlog), "\n"); n != 1 {
		t.Errorf("install.sh ran %d times, want once", n)
	}

	if err := os.Remove(filepath.Join(f.Base, "send-broken")); err != nil {
		t.Fatal(err)
	}
	out, failed = f.make(t, "ship")
	if failed || !strings.Contains(out, "told the leads") ||
		!strings.Contains(out, "serves and web follow the new binary within a minute") {
		t.Fatalf("the ship with a working notify must succeed and tell the leads (failed %v):\n%s", failed, out)
	}
	if strings.Contains(out, "restarted the serves") {
		t.Errorf("ship claims to restart serves:\n%s", out)
	}

	ghBefore, instBefore := readFile(t, f.ghlog), readFile(t, f.instlog)
	head := f.Git(f.Root, "rev-parse", "HEAD")
	out, failed = f.make(t, "ship")
	if failed {
		t.Fatalf("the second ship failed:\n%s", out)
	}
	for _, want := range []string{"push skipped", "publish skipped", "install skipped",
		"serves and web follow the new binary within a minute", "told the leads"} {
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
	log := readFile(t, f.sent)
	// One send per other project per ship: two ships plus the failing one that got through, the kit never.
	if n := strings.Count(log, ": obor v1.2.4 is installed. "); n != 6 {
		t.Errorf("obor send ran %d times over three ships (two other projects each), want 6:\n%s", n, log)
	}
	for _, name := range []string{"axis-alies", "other"} {
		if n := strings.Count(log, name+": "); n != 3 {
			t.Errorf("%s was told %d times over three ships, want once each:\n%s", name, n, log)
		}
	}
	if strings.Contains(log, "kit: ") {
		t.Errorf("the kit told its own lead:\n%s", log)
	}
	if !strings.Contains(log, strings.Fields(notes)[0]) {
		t.Errorf("the sends were not given the tag's notes %q:\n%s", notes, log)
	}
}

// a close commit's body records its cycle's Covered set and the sha its review started at. The check runs from
// that sha: a task merged while the review ran that no cycle covered is unreviewed, a task a verify covered is not, and
// the cycle that covers it clears it.
func TestMakeReleaseCountsCoveredTasks(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	f.merge(t, "topic/a", "Merge T-1: one", map[string]string{"internal/a.go": "package internal\n"})
	f.merge(t, "topic/b", "Merge T-2: two", map[string]string{"internal/b.go": "package internal\n"})
	start := f.Git(f.Root, "rev-parse", "--short", "HEAD") // RV-1 starts: T-1 and T-2 are in its scope
	// T-3 merges while RV-1 runs (no cycle covers it); T-4 is the fix its verify covers.
	f.merge(t, "topic/c", "Merge T-3: during the review", map[string]string{"internal/c.go": "package internal\n"})
	f.merge(t, "topic/d", "Merge T-4: the fix", map[string]string{"internal/d.go": "package internal\n"})
	f.closeReviewCovered(t, 1, f.headShort(t), start, "T-1", "T-2", "T-4")
	out, failed := f.release(t, "V=1.2.4", "DRY=1")
	if !failed || !strings.Contains(out, "FAIL unreviewed since RV-1: T-3 (") {
		t.Fatalf("a task no cycle covered: failed %v:\n%s", failed, out)
	}
	if strings.Contains(out, "T-4") {
		t.Errorf("the verify's covered task is counted unreviewed:\n%s", out)
	}
	// RV-2's scope has T-3 (the merges since RV-1's start it did not cover): covering it clears the check.
	start2 := f.Git(f.Root, "rev-parse", "--short", "HEAD")
	f.closeReviewCovered(t, 2, start2, start2, "T-3")
	if out, failed := f.release(t, "V=1.2.4", "DRY=1"); failed {
		t.Fatalf("after a cycle covered T-3:\n%s", out)
	}
}
