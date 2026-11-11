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
