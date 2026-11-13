package view

import (
	"context"
	"strings"
	"testing"

	"obor/internal/checks"
)

// TestDocSearchHits: a search asks for the q's words and answers its list's matching lines, trimmed; a line of a path
// the list does not have (a symlink git grep reads) is no hit, and a q without words is no hit and searches nothing.
func TestDocSearchHits(t *testing.T) {
	t.Parallel()
	var searched []string
	c := &DocCache{
		Head:  func(context.Context, string) (string, error) { return "h1", nil },
		Index: func(string) (int64, int64, error) { return 1, 1, nil },
		Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
			return []checks.TrackedDoc{{Path: "docs/a.md"}, {Path: "README.md"}}, nil
		},
		Grep: func(_ context.Context, root string, words []string) ([]checks.DocMatch, error) {
			searched = append(searched, root+": "+strings.Join(words, " "))
			return []checks.DocMatch{
				{Path: "docs/a.md", Line: 7, Text: "  the alpha  beta line  "},
				{Path: "link.md", Line: 1, Text: "alpha beta, a symlink git grep reads"},
			}, nil
		},
	}
	ctx := context.Background()
	h, err := c.Search(ctx, "/pj", "   ")
	if err != nil || len(h.Hits) != 0 || h.Truncated || h.Q != "   " {
		t.Errorf("Search of a q without words = %+v, %v; want no hit and the q", h, err)
	}
	if len(searched) != 0 {
		t.Errorf("a q without words searched %v", searched)
	}
	h, err = c.Search(ctx, "/pj", " alpha   beta ")
	if err != nil {
		t.Fatal(err)
	}
	if h.Q != " alpha   beta " || len(h.Hits) != 1 || h.Hits[0].Path != "docs/a.md" || h.Hits[0].Line != 7 ||
		h.Hits[0].Text != "the alpha  beta line" || h.Truncated {
		t.Errorf("Search(alpha beta) = %+v, want docs/a.md:7 trimmed and no link.md (the list has no such doc)", h)
	}
	if len(searched) != 1 || searched[0] != "/pj: alpha beta" {
		t.Errorf("searched %v, want the q's words once", searched)
	}
}

// TestDocSearchCapsTheHits: the 200th hit is returned and the 201st sets Truncated instead, and a search of exactly 200
// hits is not truncated.
func TestDocSearchCapsTheHits(t *testing.T) {
	t.Parallel()
	matches := make([]checks.DocMatch, MaxDocHits+1)
	for i := range matches {
		matches[i] = checks.DocMatch{Path: "docs/a.md", Line: i + 1, Text: "one word a line"}
	}
	c := &DocCache{
		Head:  func(context.Context, string) (string, error) { return "h1", nil },
		Index: func(string) (int64, int64, error) { return 1, 1, nil },
		Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
			return []checks.TrackedDoc{{Path: "docs/a.md"}}, nil
		},
		Grep: func(context.Context, string, []string) ([]checks.DocMatch, error) { return matches, nil },
	}
	ctx := context.Background()
	h, err := c.Search(ctx, "/pj", "word")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Hits) != MaxDocHits || !h.Truncated || h.Hits[MaxDocHits-1].Line != MaxDocHits {
		t.Errorf("Search of %d hits = %d hits, truncated %v; want the first %d and truncated", len(matches), len(h.Hits),
			h.Truncated, MaxDocHits)
	}
	matches = matches[:MaxDocHits]
	if h, err := c.Search(ctx, "/pj", "word"); err != nil || len(h.Hits) != MaxDocHits || h.Truncated {
		t.Errorf("Search of exactly %d hits = %d hits, truncated %v, %v; want all of them, not truncated", MaxDocHits,
			len(h.Hits), h.Truncated, err)
	}
}

// TestDocSearchSnippets: a hit's text is the line trimmed and, when it is longer, the 200 characters around the earliest
// of the words, a cut of the line and nothing rewritten.
func TestDocSearchSnippets(t *testing.T) {
	t.Parallel()
	if got := snippet("  short line  ", []string{"short"}); got != "short line" {
		t.Errorf("snippet of a short line = %q, want it trimmed", got)
	}
	long := strings.Repeat("x", 240) + " needle " + strings.Repeat("y", 100)
	got := snippet(long, []string{"needle"})
	if n := len([]rune(got)); n != snippetCut {
		t.Errorf("snippet = %d characters, want %d", n, snippetCut)
	}
	if !strings.Contains(got, "needle") || !strings.Contains(long, got) {
		t.Errorf("snippet = %q, want %d characters of the line around its needle", got, snippetCut)
	}
	if got := snippet("needle "+strings.Repeat("x", 300), []string{"needle"}); !strings.HasPrefix(got, "needle ") {
		t.Errorf("snippet of a line whose hit is first = %q, want its head", got)
	}
	// beta starts at 251 (250 x's and a space): the window opens snippetCut/4 characters before it
	late := strings.Repeat("x", 250) + " beta " + strings.Repeat("y", 300) + " alpha"
	got = snippet(late, []string{"alpha", "beta"})
	if at := strings.Index(late, got); at != 251-snippetCut/4 {
		t.Errorf("snippet of a line with beta before alpha = %q at %d, want the window around beta at %d", got, at,
			251-snippetCut/4)
	}
	if !strings.Contains(got, "beta") {
		t.Errorf("snippet = %q, want the earliest word beta in it", got)
	}
}
