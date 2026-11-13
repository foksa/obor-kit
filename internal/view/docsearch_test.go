package view

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/checks"
)

// TestDocSearchHits: a search asks for the q's words once and answers its list's matching lines, trimmed; a hit in a
// path the list does not have, or one whose file ReadDoc would refuse (gone, a symlink, a path through a symlinked
// directory out of the root, a file over MaxDoc), is none, and a q without words is no hit and searches nothing.
func TestDocSearchHits(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root, out := filepath.Join(base, "root"), filepath.Join(base, "out")
	for _, d := range []string{filepath.Join(root, "docs"), out} {
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
	write("docs/a.md", "one\ntwo\n\nthe alpha beta line\n")
	if err := os.WriteFile(filepath.Join(out, "secret.md"), []byte("alpha beta outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(out, "secret.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "esc")); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(root, "big.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(MaxDoc + 1); err != nil {
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "link.md")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("precondition: link.md is a symlink on disk (%v, %v)", fi, err)
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("precondition: README.md has no file on disk (%v)", err)
	}
	var searched []string
	c := &DocCache{
		Head:  func(context.Context, string) (string, error) { return "h1", nil },
		Index: func(string) (int64, int64, error) { return 1, 1, nil },
		Files: func(context.Context, string) ([]checks.TrackedDoc, error) {
			return []checks.TrackedDoc{{Path: "docs/a.md"}, {Path: "README.md"}, {Path: "link.md"},
				{Path: "esc/secret.md"}, {Path: "gone.md"}, {Path: "big.md"}}, nil
		},
		Grep: func(_ context.Context, root string, words []string) ([]checks.DocMatch, error) {
			searched = append(searched, root+": "+strings.Join(words, " "))
			var ms []checks.DocMatch
			for _, p := range []string{"docs/a.md", "notes.txt", "README.md", "link.md", "esc/secret.md", "gone.md", "big.md"} {
				ms = append(ms, checks.DocMatch{Path: p, Line: 7, Text: "  the alpha  beta line  "})
			}
			return ms, nil
		},
	}
	ctx := context.Background()
	h, err := c.Search(ctx, root, "   ")
	if err != nil || len(h.Hits) != 0 || h.Truncated || h.Q != "   " {
		t.Errorf("Search of a q without words = %+v, %v; want no hit and the q", h, err)
	}
	if len(searched) != 0 {
		t.Errorf("a q without words searched %v", searched)
	}
	h, err = c.Search(ctx, root, " alpha   beta ")
	if err != nil {
		t.Fatal(err)
	}
	if h.Q != " alpha   beta " || len(h.Hits) != 1 || h.Hits[0].Path != "docs/a.md" || h.Hits[0].Line != 7 ||
		h.Hits[0].Text != "the alpha  beta line" || h.Truncated {
		t.Errorf("Search(alpha beta) = %+v, want only docs/a.md:7 trimmed: the list lacks notes.txt, and the other "+
			"files are gone, symlinks, out of the root or over MaxDoc", h)
	}
	if len(searched) != 1 || searched[0] != root+": alpha beta" {
		t.Errorf("searched %v, want the q's words once", searched)
	}
}

// TestDocSearchCapsTheHits: the 200th hit is returned and the 201st sets Truncated instead, and a search of exactly 200
// hits is not truncated.
func TestDocSearchCapsTheHits(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("one word a line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	h, err := c.Search(ctx, root, "word")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Hits) != MaxDocHits || !h.Truncated || h.Hits[MaxDocHits-1].Line != MaxDocHits {
		t.Errorf("Search of %d hits = %d hits, truncated %v; want the first %d and truncated", len(matches), len(h.Hits),
			h.Truncated, MaxDocHits)
	}
	matches = matches[:MaxDocHits]
	if h, err := c.Search(ctx, root, "word"); err != nil || len(h.Hits) != MaxDocHits || h.Truncated {
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
