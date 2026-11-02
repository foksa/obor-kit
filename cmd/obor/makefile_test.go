package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// Tests for the Makefile's check target, the merge gate (T-281): build first, a sandbox-safe GOCACHE. A fake go logs
// each call (args and the GOCACHE it got) and answers `go env GOCACHE` with $FAKE_GOCACHE; `go build` fails.

// makeCheck runs `make -s check` on a kit copy with the fake go (no GOCACHE in the environment, TMPDIR the copy); it
// returns the output, whether make failed, the fake go's log lines and TMPDIR.
func makeCheck(t *testing.T, gocache string) (string, bool, []string, string) {
	t.Helper()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	log := filepath.Join(dir, "go.log")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, `#!/bin/bash
echo "$* GOCACHE=$GOCACHE" >> "`+log+`"
case "$1" in
  env) echo "$FAKE_GOCACHE" ;;
  build) echo "cmd/obor: broken" >&2; exit 1 ;;
esac
`)
	cmd := exec.Command("make", "-s", "-C", dir, "check", "GO="+fake)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GOCACHE=") && !strings.HasPrefix(kv, "TMPDIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "FAKE_GOCACHE="+gocache, "TMPDIR="+dir)
	out, err := cmd.CombinedOutput()
	data, _ := os.ReadFile(log)
	return string(out), err != nil, strings.Split(strings.TrimSpace(string(data)), "\n"), dir
}

// writeKit makes dir a kit copy for `make build`: the tree's Makefile, VERSION 1.2.3 and a fake go at dir/bin/go that
// logs its argv to dir/go.log. dir/bin goes first on PATH, so a test can stub git there too.
func writeKit(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeExec(t, filepath.Join(dir, "bin", "go"), "#!/bin/bash\necho \"$*\" >> \""+filepath.Join(dir, "go.log")+"\"\n")
}

// makeBuild runs `make -s -C dir build` on a kit copy (writeKit) and returns the fake go's logged argv.
func makeBuild(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("make", "-s", "-C", dir, "build", "GO="+filepath.Join(dir, "bin", "go"))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("make build: %v\n%s", err, out)
	}
	return strings.TrimSpace(readFile(t, filepath.Join(dir, "go.log")))
}

// stamp is the version makeBuild handed the linker's -X.
func stamp(t *testing.T, build string) string {
	t.Helper()
	const key = "obor/internal/version.Version="
	i := strings.Index(build, key)
	if i < 0 {
		t.Fatalf("precondition: the build stamps %s: %q", key, build)
	}
	v := build[i+len(key):]
	if j := strings.IndexAny(v, " \t"); j >= 0 {
		v = v[:j]
	}
	return v
}

// repo is a throwaway repo that is a kit copy: its files committed, so the test can tag and commit on top.
func repo(t *testing.T) *testkit.Project {
	t.Helper()
	p := testkit.New(t)
	writeKit(t, p.Root)
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "kit")
	return p
}

// releaseRepo is main past the old tag v0.4.0 while the newest release tag, v0.14.0, sits on a release commit off main
// (the release procedure's shape); it returns the repo and main's short sha. It fails unless the fixture reproduces
// the bug the stamp fixes: `git describe` on main names v0.4.0.
func releaseRepo(t *testing.T) (*testkit.Project, string) {
	t.Helper()
	p := repo(t)
	p.Git(p.Root, "tag", "v0.4.0")
	p.Git(p.Root, "checkout", "-q", "-b", "release")
	writeFile(t, filepath.Join(p.Root, "RELEASE"), "0.14.0\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "release v0.14.0")
	p.Git(p.Root, "tag", "v0.14.0")
	p.Git(p.Root, "checkout", "-q", "main")
	writeFile(t, filepath.Join(p.Root, "main.txt"), "main moves on\n")
	p.Git(p.Root, "add", "-A")
	p.Git(p.Root, "commit", "-q", "-m", "main goes past v0.4.0")
	if d := p.Git(p.Root, "describe", "--tags", "--always", "--dirty"); !strings.HasPrefix(d, "v0.4.0-") {
		t.Fatalf("precondition: describe on main names the old tag (the bug), got %q", d)
	}
	return p, p.Git(p.Root, "rev-parse", "--short", "HEAD")
}

// T-559: the build stamps the tag when HEAD is exactly at one, else the newest release tag by version order plus
// -dev+<short sha> (-dirty when the tree is modified): release tags sit on release commits off main, so describe on
// main names an older tag. Outside a git checkout it falls back to the VERSION file.
func TestMakeBuildStampsVersion(t *testing.T) {
	t.Parallel()
	t.Run("HEAD exactly at a tag", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		p.Git(p.Root, "tag", "v0.14.0")
		if got := stamp(t, makeBuild(t, p.Root)); got != "v0.14.0" {
			t.Errorf("HEAD at the tag v0.14.0 stamps %q, want %q", got, "v0.14.0")
		}
	})
	t.Run("dirty tree at a tag", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		p.Git(p.Root, "tag", "v0.14.0")
		writeFile(t, filepath.Join(p.Root, "VERSION"), "1.2.4\n")
		if got := stamp(t, makeBuild(t, p.Root)); got != "v0.14.0-dirty" {
			t.Errorf("a modified tracked file at the tag stamps %q, want %q", got, "v0.14.0-dirty")
		}
	})
	t.Run("past a tag: newest release tag, dev sha", func(t *testing.T) {
		t.Parallel()
		p, sha := releaseRepo(t)
		want := "v0.14.0-dev+" + sha
		if got := stamp(t, makeBuild(t, p.Root)); got != want {
			t.Errorf("main past v0.4.0 stamps %q, want %q", got, want)
		}
	})
	t.Run("dirty tree", func(t *testing.T) {
		t.Parallel()
		p, sha := releaseRepo(t)
		writeFile(t, filepath.Join(p.Root, "main.txt"), "uncommitted\n")
		want := "v0.14.0-dev+" + sha + "-dirty"
		if got := stamp(t, makeBuild(t, p.Root)); got != want {
			t.Errorf("a modified tracked file stamps %q, want %q", got, want)
		}
	})
	t.Run("not a git checkout: VERSION", func(t *testing.T) {
		t.Parallel()
		dir := testkit.Dir(t)
		writeKit(t, dir)
		writeExec(t, filepath.Join(dir, "bin", "git"), "#!/bin/bash\nexit 128\n")
		if got := stamp(t, makeBuild(t, dir)); got != "v1.2.3" {
			t.Errorf("outside a checkout the VERSION file stamps %q, want %q", got, "v1.2.3")
		}
	})
	t.Run("a checkout with no release tag: VERSION", func(t *testing.T) {
		t.Parallel()
		p := repo(t)
		if got := stamp(t, makeBuild(t, p.Root)); got != "v1.2.3" {
			t.Errorf("a checkout with no v* tag stamps %q, want the VERSION file's %q", got, "v1.2.3")
		}
	})
}

func TestMakeCheckFailsOnBrokenBuild(t *testing.T) {
	t.Parallel()
	out, failed, calls, _ := makeCheck(t, t.TempDir())
	if !strings.Contains(strings.Join(calls, "\n"), "build ") {
		t.Fatalf("precondition: make check runs go build: %q", calls)
	}
	if !failed || !slices.Contains(strings.Split(out, "\n"), "FAIL make build") {
		t.Errorf("a failing build fails make check with FAIL make build (failed %v):\n%s", failed, out)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "vet ") || strings.HasPrefix(c, "test ") {
			t.Errorf("make check goes on after a failed build: %s", c)
		}
	}
}

func TestMakeCheckGocacheFallback(t *testing.T) {
	t.Parallel()
	writable := filepath.Join(t.TempDir(), "cache")
	file := filepath.Join(t.TempDir(), "file")
	writeFile(t, file, "")
	for _, tc := range []struct {
		name, gocache string
		fallback      bool
	}{
		{"writable kept", writable, false},
		{"unwritable falls back", filepath.Join(file, "cache"), true}, // under a regular file: mkdir fails
		{"empty falls back", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, calls, tmp := makeCheck(t, tc.gocache)
			var build string
			for _, c := range calls {
				if strings.HasPrefix(c, "build ") {
					build = c
				}
			}
			if build == "" {
				t.Fatalf("precondition: make check runs go build: %q", calls)
			}
			got := build[strings.LastIndex(build, "GOCACHE=")+len("GOCACHE="):]
			if tc.fallback {
				if got != filepath.Join(tmp, "obor-go-build") {
					t.Errorf("GOCACHE %q: the build gets the $TMPDIR/obor-go-build fallback, got %q", tc.gocache, got)
				}
			} else if got != "" {
				t.Errorf("writable GOCACHE: the build keeps go's default (no override), got %q", got)
			}
		})
	}
}

// P7-1: make release refuses a plugin.json version that differs from VERSION, before it builds anything (the plugin
// version keys Claude's cache dir: a mismatch serves the old copy).
func TestMakeReleaseVersionMismatch(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"0.9.9"}`+"\n")
	log := filepath.Join(dir, "go.log")
	fake := filepath.Join(dir, "fake-go")
	writeExec(t, fake, "#!/bin/bash\necho \"$*\" >> \""+log+"\"\n")
	cmd := exec.Command("make", "-s", "-C", dir, "release", "GO="+fake)
	cmd.Env = plainMakeEnv()
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("make release with plugin 0.9.9 and VERSION 1.2.3 succeeded:\n%s", out)
	}
	for _, want := range []string{"0.9.9", "1.2.3"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the failure names %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Errorf("make release built before failing the version check: %s", readFile(t, log))
	}
}

var publishPlatforms = []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"}

// releaseMarketplace is the marketplace name a release tag ships (T-592; main says obor-kit-dev).
// It is devMarketplace with only its top-level name flipped (T-645).
var releaseMarketplace = strings.Replace(devMarketplace, `"name": "obor-kit-dev"`, `"name": "obor-kit"`, 1)

// releaseNotes is a release's notes file (docs/releases/v1.2.3.md in publishFixture): a summary line, bold-led bullets
// and an Upgrading line, no Full Changelog line (publish adds that).
const releaseNotes = "A release of the kit.\n\n- **New:** something changed.\n- **Fixed:** a bug.\n\nUpgrading: re-run the installer.\n"

// publishFixture is a repo shaped like a finished release of 1.2.3: the four dist files, bin/checksums.txt listing them,
// VERSION and plugin.json at 1.2.3, the release notes at docs/releases/v1.2.3.md, all committed and tagged v1.2.3; a
// logging gh stub is first on PATH. The stub reads the releases to compare with from releases (one tag per line) and the
// repo slug from slug, both outside the repo, and keeps the notes file gh release create got in notes.
type publishFixture struct {
	*testkit.Project
	log      string
	notes    string
	releases string
	slug     string
}

func newPublishFixture(t *testing.T) *publishFixture {
	t.Helper()
	p := testkit.New(t)
	dir := p.Root
	f := &publishFixture{
		Project:  p,
		log:      filepath.Join(p.Base, "gh.log"),
		notes:    filepath.Join(p.Base, "created-notes"),
		releases: filepath.Join(p.Base, "releases"),
		slug:     filepath.Join(p.Base, "slug"),
	}
	writeFile(t, filepath.Join(dir, "Makefile"), string(treeFile(t, "Makefile")))
	writeExec(t, filepath.Join(dir, "scripts", "publish"), string(treeFile(t, "scripts/publish")))
	writeFile(t, filepath.Join(dir, "scripts", "release-lib.sh"), string(treeFile(t, "scripts/release-lib.sh")))
	writeFile(t, filepath.Join(dir, "VERSION"), "1.2.3\n")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
	writeFile(t, filepath.Join(dir, "docs", "releases", "v1.2.3.md"), releaseNotes)
	// The tag's source parent: main's dev marketplace (T-645 compares the tag's against it).
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), devMarketplace+"\n")
	p.Git(dir, "add", "-A")
	p.Git(dir, "commit", "-q", "-m", "obor v1.2.3: VERSION, plugin version")
	writeFile(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
	var catalog string
	for _, pl := range publishPlatforms {
		writeFile(t, filepath.Join(dir, "dist", "obor-1.2.3-"+pl), "binary "+pl)
		catalog += fileSum(t, filepath.Join(dir, "dist", "obor-1.2.3-"+pl)) + "  dist/obor-1.2.3-" + pl + "\n"
	}
	writeFile(t, filepath.Join(dir, "bin", "checksums.txt"), catalog)
	log := f.log
	writeExec(t, filepath.Join(dir, "stub", "gh"), `#!/bin/bash
printf '%s\n' "$*" | sed 's/--notes-file [^ ]*/--notes-file <notes>/' >> "`+log+`"
case "$1 $2" in
  "release list") [ ! -f "`+filepath.Join(p.Base, "list-fail")+`" ] || exit 1; cat "`+f.releases+`" 2> /dev/null || true ;;
  "repo view") cat "`+f.slug+`" 2> /dev/null ;;
  "release create")
    prev=
    for a; do
      [ "$prev" != --notes-file ] || cat "$a" > "`+f.notes+`"
      prev=$a
    done ;;
esac
`)
	p.Git(dir, "add", "-A")
	p.Git(dir, "commit", "-q", "-m", "release 1.2.3")
	p.Git(dir, "tag", "v1.2.3")
	return f
}

// fileSum is the sha256 hex of a file.
func fileSum(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(readFile(t, path)))
	return hex.EncodeToString(sum[:])
}

// publish runs `make publish` (plus extra make args) and returns the output, whether it failed and gh's calls, one line
// each (the notes file's path as <notes>; nil when gh was not called).
func (f *publishFixture) publish(t *testing.T, extra ...string) (string, bool, []string) {
	t.Helper()
	cmd := exec.Command("make", append([]string{"-s", "-C", f.Root, "publish"}, extra...)...)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(f.Root, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	data, rerr := os.ReadFile(f.log)
	if rerr != nil {
		return string(out), err != nil, nil
	}
	return string(out), err != nil, strings.Split(strings.TrimSpace(string(data)), "\n")
}

// publishCheck runs `sh scripts/publish <v> --check` in the fixture (the tag checks ship runs before it pushes).
func (f *publishFixture) publishCheck(t *testing.T, v string) (string, bool) {
	t.Helper()
	cmd := exec.Command("sh", "scripts/publish", v, "--check")
	cmd.Dir = f.Root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(f.Root, "stub")+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err != nil
}

// wantFail asserts a failed publish: FAIL naming want, gh never called.
func wantPublishFail(t *testing.T, out string, failed bool, gh []string, want string) {
	t.Helper()
	if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, want) {
		t.Errorf("want a FAIL naming %q (failed %v):\n%s", want, failed, out)
	}
	if gh != nil {
		t.Errorf("gh was called despite the failure: %q", gh)
	}
}

// commitAll commits the working tree as a replacement of the tag's commit, on the same source parent (the tag stays
// where it was until a test moves it): the tag's marketplace is still compared with main's dev one (T-645).
func (f *publishFixture) commitAll(t *testing.T) {
	t.Helper()
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "--amend", "-m", "release 1.2.3, changed")
}

// RV-63: make publish uploads exactly the four checked binaries of the tag: a literal platform list passed to gh as
// explicit arguments (no glob, no GOOS_LIST override), each file's sha256 equal to its line in the tag's catalog.
// T-682: the create carries the release's title and its notes from the tag.
func TestMakePublish(t *testing.T) {
	t.Parallel()
	create := "release create v1.2.3 --verify-tag --title obor 1.2.3 --notes-file <notes>"
	for _, pl := range publishPlatforms {
		create += " dist/obor-1.2.3-" + pl
	}
	wantCalls := []string{"release list --limit 100 --json tagName --jq .[].tagName", create}
	t.Run("control: release, commit catalog, tag, publish", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		out, failed, gh := f.publish(t)
		if failed || !slices.Equal(gh, wantCalls) {
			t.Errorf("publish failed %v, gh got %q; want %q\n%s", failed, gh, wantCalls, out)
		}
	})
	t.Run("an extra .bak file is not uploaded", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64.bak"), "old")
		if !f.Exists(filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64.bak")) {
			t.Fatal("precondition: the extra file exists")
		}
		out, failed, gh := f.publish(t)
		if failed || !slices.Equal(gh, wantCalls) {
			t.Errorf("publish failed %v, gh got %q; want exactly %q\n%s", failed, gh, wantCalls, out)
		}
	})
	t.Run("GOOS_LIST/GOARCH_LIST overrides do not narrow the four", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		for _, pl := range publishPlatforms[1:] {
			if err := os.Remove(filepath.Join(f.Root, "dist", "obor-1.2.3-"+pl)); err != nil {
				t.Fatal(err)
			}
		}
		out, failed, gh := f.publish(t, "GOOS_LIST=darwin", "GOARCH_LIST=arm64")
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-darwin-amd64")
	})
	t.Run("a missing file", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		if err := os.Remove(filepath.Join(f.Root, "dist", "obor-1.2.3-linux-amd64")); err != nil {
			t.Fatal(err)
		}
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-amd64")
	})
	t.Run("an artifact altered after release", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "dist", "obor-1.2.3-linux-arm64"), "tampered")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-arm64")
	})
	t.Run("the tag's catalog lacks a line", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		catalog := readFile(t, filepath.Join(f.Root, "bin", "checksums.txt"))
		var kept []string
		for _, l := range strings.Split(strings.TrimSpace(catalog), "\n") {
			if !strings.HasSuffix(l, "-linux-amd64") {
				kept = append(kept, l)
			}
		}
		writeFile(t, filepath.Join(f.Root, "bin", "checksums.txt"), strings.Join(kept, "\n")+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		// the working catalog is complete again: only the tag's lacks the line
		writeFile(t, filepath.Join(f.Root, "bin", "checksums.txt"), catalog)
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-linux-amd64")
	})
	t.Run("the working catalog is right but the tag's differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		path := filepath.Join(f.Root, "bin", "checksums.txt")
		good := readFile(t, path)
		writeFile(t, path, strings.Replace(good, fileSum(t, filepath.Join(f.Root, "dist", "obor-1.2.3-darwin-arm64")), strings.Repeat("0", 64), 1))
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, path, good)
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "dist/obor-1.2.3-darwin-arm64")
	})
	t.Run("the tag's plugin version differs from VERSION", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.2"}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"), `{"name":"obor","version":"1.2.3"}`+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "plugin.json")
	})
	t.Run("the tag's marketplace name differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		// main's name: the dev checkout's marketplace, which a release tag must rename before publishing.
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), `{"name":"obor-kit-dev","plugins":[{"name":"obor","source":"./"}]}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "marketplace.json")
	})
	// T-645: v0.17.2's flip renamed every "name": the plugin entry too, so obor@obor-kit no longer resolved.
	t.Run("the tag's flip renamed the plugin entry too", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), v0172Marketplace+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "plugin name at v1.2.3 is 'obor-kit', want 'obor'")
	})
	t.Run("the tag's marketplace differs from main's beyond the name", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"),
			`{"name": "obor-kit","owner":{"name":"obor-kit"},"plugins":[{"name":"obor","source":"./"}]}`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "differs from its source's in more than the top-level name")
	})
	t.Run("the tag's marketplace does not parse", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), `{"name": "obor-kit",`+"\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "does not parse")
	})
	t.Run("the tag's VERSION differs", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, "VERSION"), "1.2.2\n")
		f.commitAll(t)
		f.Git(f.Root, "tag", "-f", "v1.2.3")
		writeFile(t, filepath.Join(f.Root, "VERSION"), "1.2.3\n")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "VERSION")
	})
	t.Run("no tag", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		f.Git(f.Root, "tag", "-d", "v1.2.3")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "v1.2.3")
	})
}
