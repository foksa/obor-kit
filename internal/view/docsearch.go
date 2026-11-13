package view

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"obor/internal/checks"
)

// DocHit is one line of a doc a search finds: the doc's path from the root (slash-separated), the line's number
// (1-based) and the line, trimmed and cut around the hit.
type DocHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// DocHits is a docs search's answer: the text searched and its hits, at most MaxDocHits; Truncated says there were
// more.
type DocHits struct {
	Q         string   `json:"q"`
	Hits      []DocHit `json:"hits"`
	Truncated bool     `json:"truncated"`
}

// MaxDocHits is how many hits one search returns.
const MaxDocHits = 200

// snippetCut is how many characters of a line a hit's text keeps.
const snippetCut = 200

// Search is the lines of root's tracked docs that hold every word of q, case aside: at most MaxDocHits of them, each
// text trimmed and cut to snippetCut characters around its first hit. The docs are the list /api/docs serves, and the
// search is checks.GrepDocs over git's index; a hit in a path the list does not have (a symlink git grep reads) is
// none, and a q without words is no hit at all.
func (c *DocCache) Search(ctx context.Context, root, q string) (DocHits, error) {
	out := DocHits{Q: q, Hits: []DocHit{}}
	words := strings.Fields(q)
	if len(words) == 0 {
		return out, nil
	}
	l, err := c.List(ctx, root)
	if err != nil {
		return DocHits{}, err
	}
	grep := c.Grep
	if grep == nil {
		grep = checks.GrepDocs
	}
	ms, err := grep(ctx, root, words)
	if err != nil {
		return DocHits{}, fmt.Errorf("%s: search: %w", root, err)
	}
	for _, m := range ms {
		if !l.Has(m.Path) {
			continue
		}
		if len(out.Hits) == MaxDocHits {
			out.Truncated = true
			break
		}
		out.Hits = append(out.Hits, DocHit{Path: m.Path, Line: m.Line, Text: snippet(m.Text, words)})
	}
	return out, nil
}

// snippet is line trimmed, and when it is longer than snippetCut the snippetCut characters around the first of words it
// holds (git matched the line, so one is in it).
func snippet(line string, words []string) string {
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) <= snippetCut {
		return line
	}
	start := firstWord(line, words) - snippetCut/4
	if start < 0 {
		start = 0
	}
	if start+snippetCut > len(r) {
		start = len(r) - snippetCut
	}
	return string(r[start : start+snippetCut])
}

// firstWord is the character index of the first of words line holds, case aside (-1: none).
func firstWord(line string, words []string) int {
	lower := strings.ToLower(line)
	found := -1
	for _, w := range words {
		i := strings.Index(lower, strings.ToLower(w))
		if i >= 0 && (found < 0 || i < found) {
			found = i
		}
	}
	if found < 0 {
		return -1
	}
	return utf8.RuneCountInString(line[:found])
}
