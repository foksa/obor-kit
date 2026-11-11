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
// sha rev-parse names.
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
}
