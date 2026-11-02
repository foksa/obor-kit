package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for T-682: a release gets a title `obor <x.y.z>` and the notes the lead writes in docs/releases/v<x.y.z>.md, plus
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

// T-682: scripts/release refuses a version without its notes file, or with an empty one, naming the file and changing
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

// T-682: publish creates the release with the title `obor <version>` and a notes file made of the tag's
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
