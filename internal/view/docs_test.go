package view

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	got := BuildDocs("abc", files)
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
	if e, _ := json.Marshal(BuildDocs("h", nil)); string(e) != `{"head":"h","pinned":[],"groups":[]}` {
		t.Errorf("no docs: %s, want empty lists (not null)", e)
	}
}

// TestDocCacheReadsAgainOnlyForANewHeadOrIndex: the list is read once per HEAD, index key and root; a moved HEAD or a
// changed index reads it again, and an unreadable index is an error.
func TestDocCacheReadsAgainOnlyForANewHeadOrIndex(t *testing.T) {
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
	l := BuildDocs("h", []checks.TrackedDoc{{Path: "docs/a.md"}, {Path: "draft.md"}, {Path: "link.md"}, {Path: "esc/secret.md"}})
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
