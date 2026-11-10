//go:build release

package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// a release gets a title `obor <x.y.z>` and the notes the lead writes in docs/releases/v<x.y.z>.md, plus
// a Full Changelog line to the newest earlier GitHub release. scripts/release refuses without the notes file; scripts/
// publish reads it at the tag (so what is published is what was tagged) and appends the changelog link.

// notesPath is the release's notes file in a fixture of v...
func notesPath(v string) string { return filepath.Join("docs", "releases", "v"+v+".md") }

// dropNotes commits the fixture's tree without its notes file, on the tag's source parent, and moves the tag there: the
// working tree gets the notes back, so only the tag lacks them (the shape of an old release).
func (f *publishFixture) dropNotes(t *testing.T, v string) {
	t.Helper()
	path := filepath.Join(f.Root, notesPath(v))
	notes := readFile(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	f.commitAll(t)
	f.Git(f.Root, "tag", "-f", "v"+v)
	writeFile(t, path, notes)
}

// scripts/release refuses a version without its notes file, or with an empty one, naming the file and changing
// nothing; --dry-run reports the same refusal.
func TestMakeReleaseRequiresNotes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, f *releaseFixture)
		want    string
	}{
		{"no notes file", func(t *testing.T, f *releaseFixture) {
			if err := os.Remove(filepath.Join(f.Root, notesPath("1.2.4"))); err != nil {
				t.Fatal(err)
			}
			f.Git(f.Root, "add", "-A")
			f.Git(f.Root, "commit", "-q", "-m", "no notes")
		}, "no docs/releases/v1.2.4.md"},
		{"an empty notes file", func(t *testing.T, f *releaseFixture) {
			writeFile(t, filepath.Join(f.Root, notesPath("1.2.4")), "\n \n")
			f.Git(f.Root, "add", "-A")
			f.Git(f.Root, "commit", "-q", "-m", "empty notes")
		}, "docs/releases/v1.2.4.md is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReleaseFixture(t)
			tc.prepare(t, f)
			head := f.Git(f.Root, "rev-parse", "HEAD")
			// Precondition: the guide's heading is there, so the notes file is what refuses.
			if guide := f.Read("docs/UPGRADE-v1.2.md"); !strings.Contains(guide, "## v1.2.4 (patch)") {
				t.Fatalf("precondition: the guide has no v1.2.4 heading: %q", guide)
			}
			for _, args := range [][]string{{"V=1.2.4"}, {"V=1.2.4", "DRY=1"}} {
				out, failed := f.release(t, args...)
				if !failed || !strings.Contains(out, tc.want) {
					t.Errorf("%q: want a FAIL naming %q (failed %v):\n%s", args, tc.want, failed, out)
				}
				f.untouched(t, "1.2.4", head)
			}
		})
	}
}

// publish creates the release with the title `obor <version>` and a notes file made of the tag's
// docs/releases/v<version>.md plus the Full Changelog line; the baseline is the newest GitHub release older than this
// tag, so a tag that was never released is skipped; with no earlier release there is no line.
func TestMakePublishNotes(t *testing.T) {
	t.Parallel()
	tagNotes := func(t *testing.T, f *publishFixture) string { return strings.TrimRight(releaseNotes, "\n") }

	t.Run("title and the tag's notes, no earlier release", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		out, failed, gh := f.publish(t)
		if failed {
			t.Fatalf("publish failed:\n%s", out)
		}
		if want := " --title obor 1.2.3 --notes-file <notes>"; !strings.Contains(strings.Join(gh, "\n"), want) {
			t.Errorf("gh's create lacks %q:\n%s", want, strings.Join(gh, "\n"))
		}
		if got := readFile(t, f.notes); got != tagNotes(t, f)+"\n" {
			t.Errorf("the published notes = %q, want the tag's notes file", got)
		}
		if strings.Contains(readFile(t, f.notes), "Full Changelog") {
			t.Errorf("the notes have a Full Changelog line with no earlier release: %q", readFile(t, f.notes))
		}
		for _, call := range gh {
			if strings.HasPrefix(call, "repo view") {
				t.Errorf("gh was asked for the repo slug with no changelog line to build: %q", call)
			}
		}
	})

	t.Run("the newest earlier release, skipping a tag that was never released", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		// v1.2.2 is a local tag with no GitHub release (like v0.17.3/v0.17.4); v1.3.0 is a release newer than this tag.
		f.Git(f.Root, "tag", "v1.2.2")
		writeFile(t, f.releases, "v1.3.0\nv1.2.1\n")
		writeFile(t, f.slug, "foksa/obor-kit\n")
		out, failed, _ := f.publish(t)
		if failed {
			t.Fatalf("publish failed:\n%s", out)
		}
		want := tagNotes(t, f) + "\n\n**Full Changelog**: https://github.com/foksa/obor-kit/compare/v1.2.1...v1.2.3\n"
		if got := readFile(t, f.notes); got != want {
			t.Errorf("the published notes = %q, want %q", got, want)
		}
	})

	t.Run("a final release beats a prerelease in the list", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		// a prerelease is not an x.y.z baseline; version_lt says so, so v1.2.2 wins over v1.2.2-rc1.
		writeFile(t, f.releases, "v1.2.2-rc1\nv1.2.2\nv1.2.1\n")
		writeFile(t, f.slug, "foksa/obor-kit\n")
		out, failed, _ := f.publish(t)
		if failed {
			t.Fatalf("publish failed:\n%s", out)
		}
		want := tagNotes(t, f) + "\n\n**Full Changelog**: https://github.com/foksa/obor-kit/compare/v1.2.2...v1.2.3\n"
		if got := readFile(t, f.notes); got != want {
			t.Errorf("the published notes = %q, want the final v1.2.2 as the baseline: %q", got, want)
		}
	})

	t.Run("the notes come from the tag, not the working tree", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		writeFile(t, filepath.Join(f.Root, notesPath("1.2.3")), "uncommitted draft\n")
		// Precondition: the working tree really differs from the tag.
		if got := f.Git(f.Root, "show", "v1.2.3:"+notesPath("1.2.3")); got != strings.TrimRight(releaseNotes, "\n") {
			t.Fatalf("precondition: the tag's notes are %q", got)
		}
		out, failed, _ := f.publish(t)
		if failed {
			t.Fatalf("publish failed:\n%s", out)
		}
		if got := readFile(t, f.notes); strings.Contains(got, "uncommitted") {
			t.Errorf("the published notes came from the working tree: %q", got)
		}
	})

	t.Run("no notes at the tag", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		f.dropNotes(t, "1.2.3")
		out, failed, gh := f.publish(t)
		wantPublishFail(t, out, failed, gh, "docs/releases/v1.2.3.md")
	})

	t.Run("--check refuses without the notes at the tag", func(t *testing.T) {
		t.Parallel()
		f := newPublishFixture(t)
		// Control: the fixture as it is passes the check publish --check runs.
		if out, failed := f.publishCheck(t, "1.2.3"); failed {
			t.Fatalf("precondition: publish --check refuses the good tag:\n%s", out)
		}
		f.dropNotes(t, "1.2.3")
		out, failed := f.publishCheck(t, "1.2.3")
		if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, "docs/releases/v1.2.3.md") {
			t.Errorf("publish --check must refuse the tag without notes, naming the file (failed %v):\n%s", failed, out)
		}
		if _, err := os.Stat(f.log); err == nil {
			t.Errorf("publish --check called gh: %s", readFile(t, f.log))
		}
	})
}

// a failed `gh release list` refuses before anything is created (a created release without the Full
// Changelog line is not repaired by re-running ship); the retry with a working list publishes with the line.
func TestMakePublishRefusesFailedReleaseList(t *testing.T) {
	t.Parallel()
	f := newPublishFixture(t)
	writeFile(t, f.releases, "v1.2.1\n")
	writeFile(t, f.slug, "foksa/obor-kit\n")
	writeFile(t, filepath.Join(f.Base, "list-fail"), "")

	out, failed, gh := f.publish(t)
	if !failed || !strings.Contains(out, "FAIL") || !strings.Contains(out, "gh release list failed") {
		t.Fatalf("a failed release list must refuse (failed %v):\n%s", failed, out)
	}
	if strings.Contains(strings.Join(gh, "\n"), "release create") {
		t.Errorf("publish created the release without knowing the Full Changelog baseline: %q", gh)
	}
	if _, err := os.Stat(f.notes); err == nil {
		t.Errorf("publish wrote a release's notes despite the failure: %q", readFile(t, f.notes))
	}

	if err := os.Remove(filepath.Join(f.Base, "list-fail")); err != nil {
		t.Fatal(err)
	}
	out, failed, gh = f.publish(t)
	if failed {
		t.Fatalf("the retry with a working list failed:\n%s", out)
	}
	if !strings.Contains(strings.Join(gh, "\n"), "release create v1.2.3") {
		t.Errorf("the retry did not create the release: %q", gh)
	}
	want := strings.TrimRight(releaseNotes, "\n") +
		"\n\n**Full Changelog**: https://github.com/foksa/obor-kit/compare/v1.2.1...v1.2.3\n"
	if got := readFile(t, f.notes); got != want {
		t.Errorf("the retry's notes = %q, want %q", got, want)
	}
}

// tagWithoutNotes makes v a finished release tag whose tree lacks docs/releases/v<v>.md, then commits the notes on main
// after it: a release resumed once the lead wrote the notes. Everything else of the tag
// is finished (VERSION, plugin, marketplace, catalog, the four files), so the notes are the only reason to refuse.
func tagWithoutNotes(t *testing.T, f *releaseFixture, v string) {
	t.Helper()
	notes := notesPath(v)
	text := readFile(t, filepath.Join(f.Root, notes))
	if err := os.Remove(filepath.Join(f.Root, notes)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.Root, "VERSION"), v+"\n")
	plugin := readFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"))
	writeFile(t, filepath.Join(f.Root, ".claude-plugin", "plugin.json"),
		strings.Replace(plugin, `"version":"1.2.3"`, `"version":"`+v+`"`, 1))
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "obor v"+v+": VERSION, plugin version")
	// The eight files and their catalog lines: dist/ is ignored, so main stays clean.
	var fresh string
	for _, a := range releaseAssets(v) {
		path := filepath.Join(f.Root, "dist", a)
		writeFile(t, path, "binary "+a)
		fresh += fileSum(t, path) + "  dist/" + a + "\n"
	}
	// The tag's own commit: the marketplace name flipped and the catalog (tag_neutral), nothing else.
	w := filepath.Join(f.Base, "tag-edit")
	f.Git(f.Root, "worktree", "add", "-q", "--detach", w, "HEAD")
	writeFile(t, filepath.Join(w, ".claude-plugin", "marketplace.json"), releaseMarketplace+"\n")
	writeFile(t, filepath.Join(w, "bin", "checksums.txt"), readFile(t, filepath.Join(w, "bin", "checksums.txt"))+fresh)
	f.Git(w, "add", "-A")
	f.Git(w, "commit", "-q", "-m", "obor v"+v+": marketplace name, checksums")
	f.Git(w, "tag", "-a", "v"+v, "-m", "obor v"+v)
	f.Git(f.Root, "worktree", "remove", w)
	writeFile(t, filepath.Join(f.Root, notes), text)
	f.Git(f.Root, "add", "-A")
	f.Git(f.Root, "commit", "-q", "-m", "docs: v"+v+" release notes")
}

// resuming an existing tag whose tree has no notes is refused with the never-moves guidance, even when
// the notes are committed on main after the tag: the immutable tag cannot be repaired, so a changed release is a new
// patch version. --dry-run refuses the same way.
func TestMakeReleaseExistingTagWithoutNotes(t *testing.T) {
	t.Parallel()
	f := newReleaseFixture(t)
	tagWithoutNotes(t, f, "1.2.4")
	// Preconditions: the notes are committed on main and absent at the tag (else this is vacuous).
	if !f.Exists(filepath.Join(f.Root, notesPath("1.2.4"))) {
		t.Fatal("precondition: main has no notes file")
	}
	if exec.Command("git", "-C", f.Root, "cat-file", "-e", "v1.2.4:"+notesPath("1.2.4")).Run() == nil {
		t.Fatal("precondition: the tag has the notes file")
	}
	head := f.Git(f.Root, "rev-parse", "HEAD")
	tagBefore := f.Git(f.Root, "rev-parse", "v1.2.4")

	for _, args := range [][]string{{"V=1.2.4"}, {"V=1.2.4", "DRY=1"}} {
		out, failed := f.release(t, args...)
		if !failed || !strings.Contains(out, "FAIL no docs/releases/v1.2.4.md at v1.2.4") ||
			!strings.Contains(out, "release a new patch version") {
			t.Errorf("%q: want a FAIL naming the missing tag's notes and the way out (failed %v):\n%s", args, failed, out)
		}
		if got := f.Git(f.Root, "rev-parse", "HEAD"); got != head {
			t.Errorf("%q: HEAD moved from %s to %s", args, head, got)
		}
		if got := f.Git(f.Root, "rev-parse", "v1.2.4"); got != tagBefore {
			t.Errorf("%q: the tag moved from %s to %s", args, tagBefore, got)
		}
		if f.Exists(f.log) {
			t.Errorf("%q: go ran: %s", args, readFile(t, f.log))
		}
		if f.Exists(f.scratch("1.2.4")) {
			t.Errorf("%q: the scratch worktree was created", args)
		}
	}
}

// version_lt is false for anything but x.y.z on either side, as its comment says, so a prerelease or
// malformed tag never wins the changelog baseline.
func TestReleaseLibVersionLt(t *testing.T) {
	t.Parallel()
	dir := testkit.Dir(t)
	writeFile(t, filepath.Join(dir, "release-lib.sh"), string(treeFile(t, "scripts/release-lib.sh")))
	lt := func(a, b string) bool {
		t.Helper()
		return exec.Command("sh", "-c", `. "$1/release-lib.sh"; version_lt "$2" "$3"`, "sh", dir, a, b).Run() == nil
	}
	// Control: an x.y.z pair still compares (a version_lt that always failed would pass the cases below).
	if !lt("0.18.0", "0.18.1") {
		t.Fatalf("version_lt 0.18.0 0.18.1 is false, want true")
	}
	for _, a := range []string{"0.18.0-rc1", "0.18.0-garbage", "1.2"} {
		if lt(a, "0.18.1") {
			t.Errorf("version_lt %q 0.18.1 is true, want false: both operands must be x.y.z", a)
		}
	}
	if lt("0.18.1", "0.18.0-rc1") {
		t.Errorf("version_lt 0.18.1 0.18.0-rc1 is true, want false: both operands must be x.y.z")
	}
}
