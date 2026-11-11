//go:build integration

package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"obor/internal/project"
	"obor/internal/testkit"
	"obor/internal/view"
)

// TestDocsAPIFollowsTheIndex: /api/docs and /api/doc read git's index at the request. A doc git rm --cached drops (its
// file still on disk, HEAD unmoved) is refused and left out of the list, a newly staged .md is listed, and a tracked
// file replaced on disk by a symlink is refused; a "../" is refused too.
func TestDocsAPIFollowsTheIndex(t *testing.T) {
	p := testkit.New(t)
	p.Write("README.md", "# readme\n")
	p.Write("docs/a.md", "# a\n")
	p.Write("docs/gone.md", "# gone\n")
	p.Git(p.Root, "add", "README.md", "docs")
	p.Git(p.Root, "commit", "-q", "-m", "docs")

	s := &Server{Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: p.Root}} }}
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
		t.Fatalf("/api/docs = %+v, want the committed docs", l)
	}
	if got := text("docs/gone.md"); got != "# gone\n" {
		t.Fatalf("/api/doc of docs/gone.md = %q, want its text", got)
	}

	head := p.Git(p.Root, "rev-parse", "HEAD")
	p.Git(p.Root, "rm", "-q", "--cached", "docs/gone.md")
	if !p.Exists("docs/gone.md") || p.Git(p.Root, "rev-parse", "HEAD") != head {
		t.Fatal("precondition: git rm --cached keeps the file on disk and leaves HEAD where it was")
	}
	if l := list(); l.Has("docs/gone.md") {
		t.Errorf("/api/docs still lists docs/gone.md after git rm --cached: %+v", l)
	}
	if r := doc("docs/gone.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a doc git rm --cached dropped: %d %s, want 403", r.Code, r.Body.String())
	}

	p.Write("docs/new.md", "# new\n")
	p.Git(p.Root, "add", "docs/new.md")
	if p.Git(p.Root, "rev-parse", "HEAD") != head {
		t.Fatal("precondition: staging a new doc leaves HEAD where it was")
	}
	l := list()
	if !l.Has("docs/new.md") {
		t.Errorf("/api/docs omits a newly staged .md: %+v", l)
	}
	if got := text("docs/new.md"); got != "# new\n" {
		t.Errorf("/api/doc of a newly staged doc = %q, want its text", got)
	}

	if err := os.Remove(p.Path("docs/a.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p.Path("README.md"), p.Path("docs/a.md")); err != nil {
		t.Fatal(err)
	}
	if r := doc("docs/a.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a tracked file replaced on disk by a symlink: %d %s, want 403", r.Code, r.Body.String())
	}
	if r := doc("../" + filepath.Base(p.Root) + "/README.md"); r.Code != http.StatusForbidden {
		t.Errorf("/api/doc of a ../ path: %d %s, want 403", r.Code, r.Body.String())
	}
}
