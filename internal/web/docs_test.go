package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"obor/internal/checks"
	"obor/internal/project"
	"obor/internal/view"
)

// TestDocsAPI: /api/docs is the project's tracked markdown files, pinned first, then by directory; /api/doc is one
// listed file's text, and a path the list does not have (an untracked file on disk, a "../", a directory) is 403.
func TestDocsAPI(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for p, s := range map[string]string{"BACKLOG.md": "# Backlog\n", "docs/plan/a.md": "# a\n", "draft.md": "untracked\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{
		Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: root}} },
		Docs: view.DocCache{
			Head:  func(context.Context, string) (string, error) { return "h1", nil },
			Index: func(string) (int64, int64, error) { return 1, 1, nil },
			Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
				return []checks.TrackedDoc{{Path: "docs/plan/a.md", Updated: 5}, {Path: "BACKLOG.md", Updated: 9}}, nil
			},
			Tracked: func(_ context.Context, _, p string) (bool, error) {
				return p == "docs/plan/a.md" || p == "BACKLOG.md", nil
			},
		},
	}
	h := s.Handler()
	r := get(t, h, "/api/docs?project=pj")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/docs: %d %s", r.Code, r.Body.String())
	}
	var l view.DocList
	decode(t, r, &l)
	if l.Head != "h1" || len(l.Pinned) != 1 || l.Pinned[0].Path != "BACKLOG.md" || len(l.Groups) != 1 ||
		l.Groups[0].Dir != "docs/plan" || len(l.Groups[0].Docs) != 1 || l.Groups[0].Docs[0].Path != "docs/plan/a.md" {
		t.Errorf("/api/docs = %+v, want BACKLOG.md pinned and docs/plan/a.md in its directory", l)
	}
	r = get(t, h, "/api/doc?project=pj&path="+url.QueryEscape("docs/plan/a.md"))
	var d DocText
	if r.Code != http.StatusOK {
		t.Fatalf("/api/doc of a listed doc: %d %s", r.Code, r.Body.String())
	}
	decode(t, r, &d)
	if d.Path != "docs/plan/a.md" || d.Text != "# a\n" {
		t.Errorf("/api/doc = %+v, want docs/plan/a.md's text", d)
	}
	for _, p := range []string{"draft.md", "../" + filepath.Base(root) + "/BACKLOG.md", "docs/plan/../../BACKLOG.md", "docs/plan"} {
		if r := get(t, h, "/api/doc?project=pj&path="+url.QueryEscape(p)); r.Code != http.StatusForbidden {
			t.Errorf("/api/doc of %s: %d %s, want 403", p, r.Code, r.Body.String())
		}
	}
	if r := get(t, h, "/api/doc?project=pj"); r.Code != http.StatusBadRequest {
		t.Errorf("/api/doc without a path: %d, want 400", r.Code)
	}
	if r := get(t, h, "/api/docs?project=nope"); r.Code != http.StatusNotFound {
		t.Errorf("/api/docs of an unknown project: %d, want 404", r.Code)
	}
}

// TestDocsAPIFollowsTheIndex: /api/docs and /api/doc read the index at the request. A doc the index drops (its file
// still on disk) is left out of the list and refused, a newly staged .md is listed and served, a listed file replaced
// on disk by a symlink is refused, and a "../" is refused too.
func TestDocsAPIFollowsTheIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# readme\n")
	write("docs/a.md", "# a\n")
	write("docs/gone.md", "# gone\n")

	// index stands for git's index: the files it lists, the paths it tracks, and the key the list is cached under.
	index := struct {
		files   []checks.TrackedDoc
		tracked map[string]bool
		stamp   int64
	}{
		files:   []checks.TrackedDoc{{Path: "README.md"}, {Path: "docs/a.md"}, {Path: "docs/gone.md"}},
		tracked: map[string]bool{"README.md": true, "docs/a.md": true, "docs/gone.md": true},
		stamp:   1,
	}
	s := &Server{
		Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: root}} },
		Docs: view.DocCache{
			Head:  func(context.Context, string) (string, error) { return "h1", nil },
			Index: func(string) (int64, int64, error) { return index.stamp, 0, nil },
			Files: func(context.Context, string) ([]checks.TrackedDoc, error) { return index.files, nil },
			Tracked: func(_ context.Context, _, p string) (bool, error) {
				return index.tracked[p], nil
			},
		},
	}
	h := s.Handler()
	list := func() view.DocList {
		t.Helper()
		r := get(t, h, "/api/docs?project=pj")
		if r.Code != http.StatusOK {
			t.Fatalf("/api/docs: %d %s", r.Code, r.Body.String())
		}
		var l view.DocList
		decode(t, r, &l)
		return l
	}
	doc := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		return get(t, h, "/api/doc?project=pj&path="+url.QueryEscape(path))
	}
	text := func(path string) string {
		t.Helper()
		r := doc(path)
		if r.Code != http.StatusOK {
			t.Fatalf("/api/doc of %s: %d %s", path, r.Code, r.Body.String())
		}
		var d DocText
		decode(t, r, &d)
		return d.Text
	}

	if l := list(); !l.Has("README.md") || !l.Has("docs/a.md") || !l.Has("docs/gone.md") {
		t.Fatalf("/api/docs = %+v, want the tracked docs", l)
	}
	if got := text("docs/gone.md"); got != "# gone\n" {
		t.Fatalf("/api/doc of docs/gone.md = %q, want its text", got)
	}

	index.files = []checks.TrackedDoc{{Path: "README.md"}, {Path: "docs/a.md"}}
	delete(index.tracked, "docs/gone.md")
	index.stamp++
	if _, err := os.Stat(filepath.Join(root, "docs/gone.md")); err != nil {
		t.Fatalf("precondition: dropping a doc from the index leaves its file on disk: %v", err)
	}
	if l := list(); l.Head != "h1" || l.Has("docs/gone.md") {
		t.Errorf("/api/docs still lists docs/gone.md after the index dropped it: %+v", l)
	}
	if r := doc("docs/gone.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a doc the index dropped: %d %s, want 403", r.Code, r.Body.String())
	}

	write("docs/new.md", "# new\n")
	index.files = append(index.files, checks.TrackedDoc{Path: "docs/new.md"})
	index.tracked["docs/new.md"] = true
	index.stamp++
	l := list()
	if !l.Has("docs/new.md") {
		t.Errorf("/api/docs omits a newly staged .md: %+v", l)
	}
	if got := text("docs/new.md"); got != "# new\n" {
		t.Errorf("/api/doc of a newly staged doc = %q, want its text", got)
	}

	if err := os.Remove(filepath.Join(root, "docs/a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "docs/a.md")); err != nil {
		t.Fatal(err)
	}
	if !list().Has("docs/a.md") {
		t.Fatal("precondition: the index still tracks docs/a.md, so the list still has it")
	}
	if r := doc("docs/a.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a listed file replaced on disk by a symlink: %d %s, want 403", r.Code, r.Body.String())
	}
	if r := doc("../" + filepath.Base(root) + "/README.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a ../ path: %d %s, want 403", r.Code, r.Body.String())
	}
}
