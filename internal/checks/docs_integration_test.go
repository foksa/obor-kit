//go:build integration

package checks

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepDocs: a search's hit is a line of a markdown file git's index tracks that holds every word, case aside, as a
// fixed string; a line holding only some of them is no hit, a file git does not track (an untracked .md) or one that is
// not markdown is not searched, and no match is no error.
func TestGrepDocs(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, text string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	write("docs/a.md", "# a\nalpha beta on one line\nalpha only here\nbeta only here\nMiXeD CaSe both\n")
	write("notes.txt", "alpha beta, not markdown\n")
	git("add", ".")
	git("commit", "-q", "-m", "one")
	write("docs/draft.md", "untracked alpha beta\n")

	ctx := context.Background()
	grep := func(words ...string) []DocMatch {
		t.Helper()
		ms, err := GrepDocs(ctx, root, words)
		if err != nil {
			t.Fatalf("GrepDocs%v: %v", words, err)
		}
		return ms
	}
	want := DocMatch{Path: "docs/a.md", Line: 2, Text: "alpha beta on one line"}
	if got := grep("alpha", "beta"); len(got) != 1 || got[0] != want {
		t.Errorf("GrepDocs(alpha, beta) = %+v, want only %+v: a line holding both words (lines 3 and 4 hold one each)", got, want)
	}
	if got := grep("mixed", "CASE"); len(got) != 1 || got[0].Path != "docs/a.md" || got[0].Line != 5 {
		t.Errorf("GrepDocs(mixed, CASE) = %+v, want docs/a.md:5: case aside", got)
	}
	got := grep("alpha")
	if len(got) != 2 || got[0].Line != 2 || got[1].Line != 3 {
		t.Errorf("GrepDocs(alpha) = %+v, want docs/a.md's lines 2 and 3, and neither the untracked draft nor notes.txt", got)
	}
	for _, words := range [][]string{{"untracked"}, {"alpha", "zzz"}, {"alpha", "-n"}, nil} {
		if got := grep(words...); len(got) != 0 {
			t.Errorf("GrepDocs%v = %+v, want no hit", words, got)
		}
	}
}

// TestGrepDocsReadsTheCheckout: a search reads the tracked docs' files in the checkout, not the index's blobs: a line
// appended without staging is a hit at its checkout line number, a committed line removed without staging is no longer
// one, and a tracked doc deleted from the checkout, or one replaced by a symlink, is skipped without an error.
func TestGrepDocsReadsTheCheckout(t *testing.T) {
	root := t.TempDir()
	gitOut := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git := func(args ...string) {
		t.Helper()
		gitOut(args...)
	}
	write := func(path, text string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@example.com")
	git("config", "user.name", "t")
	write("docs/a.md", "# a\nalpha one\nalpha two\nalpha three\n")
	write("docs/b.md", "committed beta\n")
	git("add", ".")
	git("commit", "-q", "-m", "one")

	ctx := context.Background()
	grep := func(words ...string) []DocMatch {
		t.Helper()
		ms, err := GrepDocs(ctx, root, words)
		if err != nil {
			t.Fatalf("GrepDocs%v: %v", words, err)
		}
		return ms
	}
	if got := grep("beta"); len(got) != 1 || got[0] != (DocMatch{Path: "docs/b.md", Line: 1, Text: "committed beta"}) {
		t.Fatalf("GrepDocs(beta) = %+v, want docs/b.md:1 before its file is deleted", got)
	}

	// A line appended without staging: a hit at its checkout line, though no commit has it.
	write("docs/a.md", "# a\nalpha one\nalpha two\nalpha three\nunstaged zeta\n")
	want := DocMatch{Path: "docs/a.md", Line: 5, Text: "unstaged zeta"}
	if got := grep("zeta"); len(got) != 1 || got[0] != want {
		t.Fatalf("GrepDocs(zeta) = %+v, want only %+v: the checkout's line 5, which the index does not have", got, want)
	}

	// A committed line removed without staging: no longer a hit, and the hits below it moved up.
	write("docs/a.md", "# a\nalpha one\nalpha three\nunstaged zeta\n")
	if got := grep("two"); len(got) != 0 {
		t.Errorf("GrepDocs(two) = %+v, want no hit: the checkout file no longer has the line", got)
	}
	if got := grep("alpha", "three"); len(got) != 1 || got[0].Line != 3 {
		t.Errorf("GrepDocs(alpha, three) = %+v, want docs/a.md:3 in the checkout", got)
	}

	// A tracked doc deleted from the checkout: skipped, and the search is no error.
	if err := os.Remove(filepath.Join(root, "docs", "b.md")); err != nil {
		t.Fatal(err)
	}
	if st := gitOut("status", "--porcelain"); !strings.Contains(st, " D docs/b.md") {
		t.Fatalf("precondition: git status has the deletion (%q), so the file is gone from the checkout", st)
	}
	if ls := gitOut("ls-files", "-z", "--", "docs/b.md"); !strings.HasPrefix(ls, "docs/b.md\x00") {
		t.Fatalf("precondition: the index still tracks docs/b.md (%q)", ls)
	}
	if got := grep("beta"); len(got) != 0 {
		t.Errorf("GrepDocs(beta) = %+v, want no hit: the checkout has no such file", got)
	}

	// A tracked doc replaced by a symlink: not read, so nothing outside the root is a hit.
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("outside secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "docs", "a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "docs", "a.md")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(outside); err != nil || !strings.Contains(string(b), "secret") {
		t.Fatalf("precondition: the file outside the root holds the word (%q, %v)", b, err)
	}
	if got := grep("secret"); len(got) != 0 {
		t.Errorf("GrepDocs(secret) = %+v, want no hit: a symlink is not a doc's file", got)
	}
}

// TestTrackedDocs: the doc list is the .md files git tracks under the root, in every directory, each with its newest
// commit's time; an untracked file, a gitignored one, a symlink and a non-markdown file are not in it, and HEAD is the
// sha rev-parse names. IndexFile is true only for a path the index tracks as a regular file (a staged one too, one git
// rm --cached dropped not), and DocsIndex is the index file's key, new after a git add.
func TestTrackedDocs(t *testing.T) {
	root := t.TempDir()
	git := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	write := func(path, text string) {
		t.Helper()
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	at := func(ts string) []string {
		return []string{"GIT_AUTHOR_DATE=" + ts + " +0000", "GIT_COMMITTER_DATE=" + ts + " +0000"}
	}
	git(nil, "init", "-q", "-b", "main")
	git(nil, "config", "user.email", "t@example.com")
	git(nil, "config", "user.name", "t")
	write("README.md", "# readme\n")
	write("docs/plan/a.md", "# a\n")
	write("main.go", "package main\n")
	write(".gitignore", "docs/reviews/\n")
	git(nil, "add", ".")
	git(at("1700000000"), "commit", "-q", "-m", "one")
	write("docs/plan/a.md", "# a, again\n")
	write("BACKLOG.md", "# backlog\n")
	if err := os.Symlink("README.md", filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	git(nil, "add", ".")
	git(at("1700000500"), "commit", "-q", "-m", "two")
	write("docs/staged.md", "staged, not committed\n")
	git(nil, "add", "docs/staged.md")
	write("draft.md", "untracked\n")
	write("docs/reviews/RV-1.md", "gitignored\n")

	ctx := context.Background()
	st := git(nil, "status", "--porcelain", "--ignored")
	if !strings.Contains(st, "!! docs/reviews/") || !strings.Contains(st, "?? draft.md") {
		t.Fatalf("precondition: git status has the ignored review and the untracked draft:\n%s", st)
	}
	docs, err := TrackedDocs(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, d := range docs {
		got[d.Path] = d.Updated
	}
	want := map[string]int64{"BACKLOG.md": 1700000500, "README.md": 1700000000, "docs/plan/a.md": 1700000500,
		"docs/staged.md": 0}
	if len(got) != len(want) {
		t.Errorf("TrackedDocs = %v, want %v", got, want)
	}
	for p, ts := range want {
		if u, ok := got[p]; !ok || u != ts {
			t.Errorf("%s: updated %d (listed %v), want %d", p, u, ok, ts)
		}
	}
	for _, p := range []string{"draft.md", "docs/reviews/RV-1.md", "link.md", "main.go"} {
		if _, ok := got[p]; ok {
			t.Errorf("%s is listed: untracked, gitignored, symlinked and non-markdown files are not", p)
		}
	}
	head, err := DocsHead(ctx, root)
	if want := git(nil, "rev-parse", "HEAD"); err != nil || head+"\n" != want {
		t.Errorf("DocsHead = %q, %v; want %q", head, err, want)
	}
	for _, c := range []struct {
		path string
		want bool
	}{
		{"README.md", true}, {"docs/staged.md", true}, {"draft.md", false}, {"link.md", false},
		{"docs/reviews/RV-1.md", false}, {"docs", false}, {"missing.md", false},
	} {
		if got, err := IndexFile(ctx, root, c.path); err != nil || got != c.want {
			t.Errorf("IndexFile(%s) = %v, %v; want %v", c.path, got, err, c.want)
		}
	}
	mtime, size, err := DocsIndex(root)
	if err != nil || mtime == 0 || size == 0 {
		t.Fatalf("DocsIndex = %d, %d, %v; want the index file's key", mtime, size, err)
	}
	write("docs/later.md", "later\n")
	git(nil, "add", "docs/later.md")
	if m2, s2, err := DocsIndex(root); err != nil || (m2 == mtime && s2 == size) {
		t.Errorf("DocsIndex after git add = %d, %d, %v; want a new key", m2, s2, err)
	}
	if got, err := IndexFile(ctx, root, "docs/later.md"); err != nil || !got {
		t.Errorf("IndexFile of a newly staged doc = %v, %v; want true", got, err)
	}
	write("docs/a[1].md", "brackets\n")
	git(nil, "add", "docs/a[1].md")
	if got, err := IndexFile(ctx, root, "docs/a[1].md"); err != nil || !got {
		t.Errorf("IndexFile of a staged name with glob characters = %v, %v; want true", got, err)
	}
	git(nil, "rm", "-q", "--cached", "README.md")
	if got, err := IndexFile(ctx, root, "README.md"); err != nil || got {
		t.Errorf("IndexFile of a doc git rm --cached dropped = %v, %v; want false", got, err)
	}
}
