package view

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/checks"
)

// TestBuildDocsPinsThenGroups: the shallowest BACKLOG.md (the root's) and the pinned paths come first in their order,
// then the rest by directory (another BACKLOG.md among them) (the root's first, then by name), newest commit first within one.
func TestBuildDocsPinsThenGroups(t *testing.T) {
	t.Parallel()
	files := []checks.TrackedDoc{
		{Path: "README.md", Updated: 10}, {Path: "AGENTS.md", Updated: 9}, {Path: "CLAUDE.md", Updated: 8}, {Path: "docs/decisions.md", Updated: 7}, {Path: "docs/accepted.md", Updated: 6},
		{Path: "docs/plan/old.md", Updated: 1}, {Path: "docs/plan/new.md", Updated: 5}, {Path: "docs/plan/b.md", Updated: 3}, {Path: "docs/plan/a.md", Updated: 3},
		{Path: "docs/research/x.md", Updated: 2}, {Path: "docs/design.md", Updated: 4}, {Path: "sub/BACKLOG.md", Updated: 2}, {Path: "BACKLOG.md", Updated: 1}, {Path: "x/y/BACKLOG.md", Updated: 3},
	}
	got := BuildDocs("abc", files, nil)
	b, _ := json.Marshal(got)
	want := `{"head":"abc","pinned":[{"path":"BACKLOG.md","updated":1},` +
		`{"path":"docs/decisions.md","updated":7},{"path":"docs/accepted.md","updated":6},{"path":"AGENTS.md","updated":9},` +
		`{"path":"README.md","updated":10}],"groups":[{"dir":"","docs":[{"path":"CLAUDE.md","updated":8}]},` +
		`{"dir":"docs","docs":[{"path":"docs/design.md","updated":4}]},{"dir":"docs/plan","docs":[` +
		`{"path":"docs/plan/new.md","updated":5},{"path":"docs/plan/a.md","updated":3},{"path":"docs/plan/b.md","updated":3},` +
		`{"path":"docs/plan/old.md","updated":1}]},{"dir":"docs/research","docs":[{"path":"docs/research/x.md","updated":2}]},` +
		`{"dir":"sub","docs":[{"path":"sub/BACKLOG.md","updated":2}]},{"dir":"x/y","docs":[{"path":"x/y/BACKLOG.md","updated":3}]}]}`
	if string(b) != want {
		t.Errorf("BuildDocs:\n got %s\nwant %s", b, want)
	}
	if !got.Has("docs/plan/a.md") || !got.Has("BACKLOG.md") || got.Has("docs/plan") || got.Has("./BACKLOG.md") {
		t.Errorf("Has: a listed path is in, a directory or a respelled path is not")
	}
	if e, _ := json.Marshal(BuildDocs("h", nil, nil)); string(e) != `{"head":"h","pinned":[],"groups":[]}` {
		t.Errorf("no docs: %s, want empty lists (not null)", e)
	}
}

// TestBuildDocsTitles: a doc carries the title read for its path (pinned and grouped alike), and a doc with none has no
// title field in the JSON.
func TestBuildDocsTitles(t *testing.T) {
	t.Parallel()
	l := BuildDocs("h", []checks.TrackedDoc{{Path: "BACKLOG.md"}, {Path: "docs/a.md"}, {Path: "docs/b.md"}},
		map[string]string{"BACKLOG.md": "Backlog", "docs/a.md": "Design v2"})
	if len(l.Pinned) != 1 || l.Pinned[0].Title != "Backlog" {
		t.Errorf("pinned = %+v, want BACKLOG.md's title", l.Pinned)
	}
	if len(l.Groups) != 1 || len(l.Groups[0].Docs) != 2 {
		t.Fatalf("groups = %+v, want docs/ with a.md and b.md", l.Groups)
	}
	docs := l.Groups[0].Docs
	if docs[0].Path != "docs/a.md" || docs[0].Title != "Design v2" || docs[1].Path != "docs/b.md" || docs[1].Title != "" {
		t.Errorf("docs = %+v, want a.md's title and b.md's none", docs)
	}
	if b, err := json.Marshal(docs[1]); err != nil || strings.Contains(string(b), "title") {
		t.Errorf("a doc with no title marshals as %s, %v; want no title field", b, err)
	}
}

// TestDocTitle: a title is the text of the first "# " line, trimmed, found after front matter and text, and never a line
// inside a fenced code block (backticks or tildes). A doc with no such line, or whose first one is past maxTitleScan
// bytes, has none.
func TestDocTitle(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, text, want string }{
		{"line 1", "# Design v2\n\ntext\n", "Design v2"},
		{"after front matter and text", "---\ntitle: x\n---\n\nintro\n\n#  Design v2  \n\nbody\n", "Design v2"},
		{"inside a backtick fence", "```sh\n# not a title\n```\n## section\n# the title\n", "the title"},
		{"inside a tilde fence", "~~~~\n# not a title\n~~~~\n# the title\n", "the title"},
		{"unclosed fence", "```\n# not a title\n", ""},
		{"no heading", "just text\n## lower\n#no title\n", ""},
		{"empty", "", ""},
		{"past the scan limit", strings.Repeat("text\n", maxTitleScan/4) + "# too late\n", ""},
	}
	for _, c := range cases {
		if got := DocTitle(strings.NewReader(c.text)); got != c.want {
			t.Errorf("%s: DocTitle = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestDocCacheTitlesFollowTheCheckoutFile: a doc's title is read from the checkout when the list is read and read again
// when that file's stat (its mtime or size) moves, at the same HEAD and index key, so an unstaged edit shows on the next
// list read while the tracked docs are not listed again; a listed doc whose file is missing, is a symlink on disk, or
// whose path runs through a symlinked directory out of the root has none.
func TestDocCacheTitlesFollowTheCheckoutFile(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root, out := filepath.Join(base, "root"), filepath.Join(base, "out")
	for _, d := range []string{root, out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# one\n")
	if err := os.WriteFile(filepath.Join(out, "secret.md"), []byte("# outside the root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "secret.md")); err != nil || !strings.HasPrefix(string(b), "# ") {
		t.Fatalf("precondition: the file outside the root starts with a heading (%q, %v): an unguarded read would title it", b, err)
	}
	reads := 0
	c := &DocCache{
		Head:  func(context.Context, string) (string, error) { return "h", nil },
		Index: func(string) (int64, int64, error) { return 1, 0, nil },
		Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
			reads++
			return []checks.TrackedDoc{{Path: "a.md"}, {Path: "esc/secret.md"}, {Path: "gone.md"}, {Path: "link.md"}}, nil
		},
	}
	titles := func() map[string]string {
		t.Helper()
		l, err := c.List(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, g := range l.Groups {
			for _, d := range g.Docs {
				got[d.Path] = d.Title
			}
		}
		if len(got) != 4 {
			t.Fatalf("list = %+v, want a.md, esc/secret.md, gone.md and link.md", l.Groups)
		}
		return got
	}
	if got := titles(); got["a.md"] != "one" || got["esc/secret.md"] != "" || got["gone.md"] != "" || got["link.md"] != "" {
		t.Fatalf("titles = %v, want a.md's heading and none for the paths that leave or lack a file", got)
	}
	if reads != 1 {
		t.Fatalf("first list: %d ls-files reads, want 1", reads)
	}
	write("a.md", "# two\n")
	if got := titles(); got["a.md"] != "two" {
		t.Errorf("title after an unstaged edit = %q, want the file's new heading", got["a.md"])
	}
	if reads != 1 {
		t.Errorf("an edited title listed the docs again: %d ls-files reads, want the cached 1", reads)
	}
	fi, err := os.Stat(filepath.Join(root, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	write("a.md", "# six\n") // the same size as "# two\n"
	if err := os.Chtimes(filepath.Join(root, "a.md"), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := titles(); got["a.md"] != "two" {
		t.Errorf("title after an edit whose mtime and size did not move = %q, want the cached one", got["a.md"])
	}
	write("a.md", "# seven\n") // a size of its own, the mtime put back
	if err := os.Chtimes(filepath.Join(root, "a.md"), fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := titles(); got["a.md"] != "seven" {
		t.Errorf("title after an edit that moved the size alone = %q, want the file's new heading", got["a.md"])
	}
	write("gone.md", "# back\n")
	if got := titles(); got["gone.md"] != "back" {
		t.Errorf("title of a doc whose file appeared = %q, want its heading", got["gone.md"])
	}
	if err := os.Remove(filepath.Join(root, "gone.md")); err != nil {
		t.Fatal(err)
	}
	if got := titles(); got["gone.md"] != "" {
		t.Errorf("title of a deleted doc = %q, want none", got["gone.md"])
	}
	if reads != 1 {
		t.Errorf("title reads listed the docs %d times, want the ls-files once", reads)
	}
}

// TestDocCacheListsAgainOnlyForANewHeadOrIndex: the tracked docs are listed once per HEAD, index key and root; a moved
// HEAD or a changed index lists them again, and an unreadable index is an error.
func TestDocCacheListsAgainOnlyForANewHeadOrIndex(t *testing.T) {
	t.Parallel()
	head, stamp, reads := "h1", int64(1), 0
	c := &DocCache{
		Head:  func(context.Context, string) (string, error) { return head, nil },
		Index: func(string) (int64, int64, error) { return stamp, 10, nil },
		Files: func(_ context.Context, root string) ([]checks.TrackedDoc, error) {
			reads++
			return []checks.TrackedDoc{{Path: root + ".md"}}, nil
		},
	}
	ctx := context.Background()
	list := func(root string) DocList {
		t.Helper()
		l, err := c.List(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if l := list("a"); l.Head != "h1" || !l.Has("a.md") || reads != 1 {
		t.Fatalf("first read: %+v after %d reads, want a.md at h1 after 1", l, reads)
	}
	list("a")
	if reads != 1 {
		t.Errorf("same HEAD and index: %d reads, want the cached 1", reads)
	}
	stamp = 2
	if l := list("a"); l.Head != "h1" || !l.Has("a.md") || reads != 2 {
		t.Errorf("moved index at the same HEAD: %+v after %d reads, want a new read", l, reads)
	}
	if l := list("b"); !l.Has("b.md") || reads != 3 {
		t.Errorf("another root: %+v after %d reads, want its own list", l, reads)
	}
	head = "h2"
	if l := list("a"); l.Head != "h2" || reads != 4 {
		t.Errorf("moved HEAD: %+v after %d reads, want a new read at h2", l, reads)
	}
	c.Head = func(context.Context, string) (string, error) { return "", nil }
	if _, err := c.List(ctx, "a"); err == nil {
		t.Errorf("an empty HEAD is no error")
	}
	c.Head = func(context.Context, string) (string, error) { return "h2", nil }
	c.Index = func(string) (int64, int64, error) { return 0, 0, errors.New("no index") }
	if _, err := c.List(ctx, "a"); err == nil {
		t.Errorf("an unreadable index is no error")
	}
}

// TestReadDocServesOnlyWhatGitTracks: a listed doc the index tracks reads; a listed one it does not, one whose file on
// disk became a symlink, one whose directory became a symlink out of the root and a "../" are refused as ErrNotADoc,
// and an index failure is an error of its own.
func TestReadDocServesOnlyWhatGitTracks(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root, out := filepath.Join(base, "root"), filepath.Join(base, "out")
	for _, d := range []string{filepath.Join(root, "docs"), out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "docs", "a.md"), "# a")
	write(filepath.Join(root, "draft.md"), "dropped from the index")
	write(filepath.Join(out, "secret.md"), "outside")
	if err := os.Symlink(filepath.Join(root, "docs", "a.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	l := BuildDocs("h", []checks.TrackedDoc{{Path: "docs/a.md"}, {Path: "draft.md"}, {Path: "link.md"}, {Path: "esc/secret.md"}}, nil)
	indexed := map[string]bool{"docs/a.md": true, "link.md": true, "esc/secret.md": true}
	c := &DocCache{Tracked: func(_ context.Context, _, p string) (bool, error) { return indexed[p], nil }}
	ctx := context.Background()
	if s, err := c.ReadDoc(ctx, root, l, "docs/a.md"); err != nil || s != "# a" {
		t.Fatalf("listed doc: %q, %v", s, err)
	}
	for _, p := range []string{"draft.md", "link.md", "esc/secret.md", "../out/secret.md", "docs/../draft.md", "/etc/passwd"} {
		if s, err := c.ReadDoc(ctx, root, l, p); !errors.Is(err, ErrNotADoc) {
			t.Errorf("%s: %q, %v; want ErrNotADoc", p, s, err)
		}
	}
	c.Tracked = func(context.Context, string, string) (bool, error) { return false, errors.New("git broke") }
	if _, err := c.ReadDoc(ctx, root, l, "docs/a.md"); err == nil || errors.Is(err, ErrNotADoc) {
		t.Errorf("index failure: %v, want an error of its own", err)
	}
}
