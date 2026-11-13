package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"obor/internal/checks"
	"obor/internal/project"
	"obor/internal/view"
)

// TestDocSearchAPI: /api/docsearch answers the project's tracked docs searched for q's words (view.DocCache.Search); a
// missing q is an empty one with no hits, so nothing is searched, and an unknown project is the docs routes' 404.
func TestDocSearchAPI(t *testing.T) {
	t.Parallel()
	var searched []string
	s := &Server{
		Projects: func() []project.Project { return []project.Project{{Name: "pj", Root: "/roots/pj"}} },
		Docs: view.DocCache{
			Head:  func(context.Context, string) (string, error) { return "h1", nil },
			Index: func(string) (int64, int64, error) { return 1, 1, nil },
			Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
				return []checks.TrackedDoc{{Path: "docs/a.md"}}, nil
			},
			Grep: func(_ context.Context, root string, words []string) ([]checks.DocMatch, error) {
				searched = append(searched, root+": "+strings.Join(words, " "))
				return []checks.DocMatch{{Path: "docs/a.md", Line: 3, Text: "the alpha beta line"}}, nil
			},
		},
	}
	h := s.Handler()
	r := get(t, h, "/api/docsearch?project=pj&q=alpha+beta")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/docsearch: %d %s", r.Code, r.Body.String())
	}
	var got view.DocHits
	decode(t, r, &got)
	if got.Q != "alpha beta" || len(got.Hits) != 1 || got.Hits[0] != (view.DocHit{Path: "docs/a.md", Line: 3,
		Text: "the alpha beta line"}) || got.Truncated {
		t.Errorf("/api/docsearch = %+v, want docs/a.md:3's line for alpha beta", got)
	}
	if len(searched) != 1 || searched[0] != "/roots/pj: alpha beta" {
		t.Errorf("searched %v, want the project's root and the two words once", searched)
	}
	r = get(t, h, "/api/docsearch?project=pj")
	if r.Code != http.StatusOK {
		t.Fatalf("/api/docsearch without q: %d %s", r.Code, r.Body.String())
	}
	decode(t, r, &got)
	if len(got.Hits) != 0 || got.Truncated {
		t.Errorf("/api/docsearch without q = %+v, want no hits", got)
	}
	if len(searched) != 1 {
		t.Errorf("a missing q searched %v, want no search", searched)
	}
	if r := get(t, h, "/api/docsearch?project=nope&q=alpha"); r.Code != http.StatusNotFound {
		t.Errorf("/api/docsearch of an unknown project: %d, want 404", r.Code)
	}
}
