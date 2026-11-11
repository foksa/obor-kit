package web

import (
	"context"
	"net/http"
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
