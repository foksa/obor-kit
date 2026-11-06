package spec

import (
	"reflect"
	"strings"
	"testing"
)

// T-496: only the exact paths of the "Protected files this task may edit" section; a glob, a blank span, another
// section (a longer heading too) and the old files section unlock nothing.
func TestProtected(t *testing.T) {
	text := "# T-02\n\n## Files you may touch\n`old.md`\n## Protected files this task may edit (old)\n`no.md`\n" +
		"## Protected files this task may edit  \n`docs/design.md`, `docs/*.md` (a glob)\n\n`` and `  ` are blank, " +
		"`a[b].md` `x?`\n` AGENTS.md `\n## Tests\n`not/this.md`\n"
	if got, want := Protected(text), []string{"docs/design.md", "AGENTS.md"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Protected = %q, want %q", got, want)
	}
	if got := Protected("## Files you may touch\n`docs/design.md`\n"); got != nil {
		t.Errorf("no section: %q", got)
	}
}

func TestGlobMatchMirrorsOborStarCases(t *testing.T) {
	cases := []struct {
		path, glob string
		match      bool
	}{
		{"src/ai/deep/x.ts", "src/ai/**", true},
		{"src/ai/x.ts", "src/**/x.ts", true},
		{"lib/obor.sh", "lib/*.sh", true},
		{"lib/obor.sh", "lib/obor.sh", true},
		{"lib/obor.sh", "lib/other.sh", false},
		{"src/x.ts", "lib/**", false},
		{"lib/obor.shx", "lib/*.sh", false},
		{"src/ui/x.ts", "src/ui/**/*.ts", true},
		{"src/ui/a/x.ts", "src/ui/**/*.ts", true},
		{"src/ui/a/b/x.ts", "src/ui/**/*.ts", true},
		{"a/b", "a/**/b", true},
		{"x", "**/x", true},
		{"d/x", "**/x", true},
		{"d/e/x", "**/x", true},
		{"xx", "**/x", false},
		{"src/ui/x.js", "src/ui/**/*.ts", false},
		{"src/uix.ts", "src/ui/**/*.ts", false},
		{"a/z", "a/**/b/**/c", false},
		{"a/c", "a/**/b/**/c", false},
		{"a/b/c", "a/**/b/**/c", true},
		{"a/x/b/y/z/c", "a/**/b/**/c", true},
		{"src/ui", "src/ui/**", false},
		{"src/ui/a/b", "src/ui/**", true},
	}
	for _, tc := range cases {
		if got := Match(tc.path, tc.glob); got != tc.match {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.path, tc.glob, got, tc.match)
		}
	}
}

// bash lib.test.sh case_glob_overlap (obor_glob_overlap, the dispatch lead guard's reading): both argument orders.
func TestOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"src/**/foo.ts", "src/ui/**", true},
		{"src/*/foo.ts", "src/ui/**", true},
		{"**/*.ts", "src/ui/**", true},
		{"src/ui/*", "src/ui/game.svelte.ts", true},
		{"src/ui/x.ts", "src/ui/x.ts", true},
		{"src/?i/x.ts", "src/u*", true},
		{"src/[ab]/x.ts", "docs/**", true}, // a bracket: fail closed
		{"docs/**", "src/ui/**", false},
		{"src/api/*.ts", "src/ui/**", false},
		{"src/ui/*.sh", "src/**/*.ts", false},
		{"src/x.ts", "src/ui/**", false},
		{"src/ui", "src/ui/**/x", false},
		{"src/ui/**/*.ts", "src/ui/x.ts", true},
		{"src/ui/**/*.ts", "src/ui/net/x.ts", true},
		{"**/x", "x", true},
		{"**/**/x", "x", true},
		{"a/**/b", "a/b", true},
		{"src/ui/**/*.ts", "src/ui/x.js", false},
	} {
		if got, rev := Overlap(c.a, c.b), Overlap(c.b, c.a); got != c.want || rev != c.want {
			t.Errorf("Overlap(%q, %q) = %v / reversed %v, want %v", c.a, c.b, got, rev, c.want)
		}
	}
}

// T-440 (T-360's heading rule, moved from checks; T-790 a ":" or "-" separates too): a spec's title is the text after
// the task id of its first "# " heading; no separator or no heading is "".
func TestTitle(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"# T-1 — a", "a"},
		{"# T-1: a", "a"},
		{"# T-1 - a", "a"},
		{"# T-1 – a", "a"},
		{"# T-1", ""},
		{"# T-1:", ""},
		{"no heading\n## T-1 — a section\n", ""},
		{"# T-05 — Committed title\n\n## Files you may touch\n", "Committed title"},
		{"intro\n# T-06 — Second line heading\n# T-06 — later\n", "Second line heading"},
		{"# T-07 no dash\n# T-07 — later\n", ""},
		{"# T-09 —   \n", ""},
	} {
		if got := Title(c.text); got != c.want {
			t.Errorf("Title(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

// T-538: the level is the Difficulty line right under the title (blank lines between are fine).
func TestDifficulty(t *testing.T) {
	for _, c := range []struct {
		text, level string
		ok          bool
	}{
		{"# T-1 — A\n\nDifficulty: medium\n\n## Do\n", "medium", true},
		{"# T-1 — A\nDifficulty:   hard  \n", "hard", true},
		{"# T-1 — A\n\nDifficulty: simple", "simple", true},
		{"# T-1 — A\n\nDifficulty: easy\n", "easy", false},
		{"# T-1 — A\n\nwhy\n\nDifficulty: hard\n", "", false},
		{"# T-1 — A\n", "", false},
		{"Difficulty: hard\n", "", false},
		{"", "", false},
	} {
		if level, ok := Difficulty(c.text); level != c.level || ok != c.ok {
			t.Errorf("Difficulty(%q) = %q, %v; want %q, %v", c.text, level, ok, c.level, c.ok)
		}
	}
}

// T-689, T-765: the optional Model line is the next non-blank line after Difficulty; the Type line it replaced is
// told apart (OldTypeLine) so spec set can refuse it.
func TestModels(t *testing.T) {
	for _, c := range []struct {
		text string
		want []string
	}{
		{"# T-1 — A\n\nDifficulty: medium\n\n## Do\n", nil},
		{"# T-1 — A\n\nDifficulty: medium\nModel: opus\n", []string{"opus"}},
		{"# T-1 — A\n\nDifficulty: hard\nModel: opus:high, opus\n", []string{"opus:high", "opus"}},
		{"# T-1 — A\n\nDifficulty: hard\n\n\nModel:  opus ,opus:high, opus\n", []string{"opus", "opus:high"}},
		{"# T-1 — A\n\nDifficulty: hard\nModel: opus:high, sonnet ,luna:xhigh\n", []string{"opus:high", "sonnet", "luna:xhigh"}}, // T-695
		{"# T-1 — A\n\nDifficulty: hard\nwhy\nModel: opus\n", nil},
		{"# T-1 — A\n\nModel: opus\nDifficulty: hard\n", nil},
		{"# T-1 — A\n\nDifficulty: hard\n\n## Do\nModel: opus\n", nil},
		{"# T-1 — A\n\nDifficulty: hard\nType: opus\n", nil},
		{"Difficulty: hard\nModel: opus\n", nil},
	} {
		if got := Models(c.text); strings.Join(got, "|") != strings.Join(c.want, "|") || (got == nil) != (c.want == nil) {
			t.Errorf("Models(%q) = %q; want %q", c.text, got, c.want)
		}
	}
	for text, want := range map[string]bool{
		"# T-1 — A\n\nDifficulty: hard\nType: opus\n":   true,
		"# T-1 — A\n\nDifficulty: hard\nModel: opus\n":  false,
		"# T-1 — A\n\nDifficulty: hard\n\nType: x\n":    true,
		"# T-1 — A\n\nDifficulty: hard\nwhy\nType: x\n": false,
	} {
		if got := OldTypeLine(text); got != want {
			t.Errorf("OldTypeLine(%q) = %v, want %v", text, got, want)
		}
	}
}

// T-878, T-932: the Review line is the spec's first "Review:" line between its title and its first "## " heading; only
// the value "on merge" turns the line on, and a line below a section (or before the title) is ignored. A review is
// asked for by that line or by `Difficulty: hard` (T-932), and never beside a `Fixes:` line (its verify is its review).
func TestReview(t *testing.T) {
	for _, c := range []struct {
		text, value string
		ok, merge   bool
	}{
		{"# T-1 — A\n\nDifficulty: medium\nReview: on merge\n\n## Do\n", "on merge", true, true},
		{"# T-1 — A\n\nDifficulty: medium\n\n## Do\n", "", false, false},
		{"# T-1 — A\n\nDifficulty: medium\nReview: now\n", "now", true, false},
		{"# T-1 — A\n\nDifficulty: medium\nReview:   on merge  \n", "on merge", true, true},
		{"# T-1 — A\n\nDifficulty: medium\n\n## Do\nReview: on merge\n", "", false, false},
		{"Review: on merge\n", "", false, false},
		{"", "", false, false},
		{"# T-1 — A\n\nDifficulty: hard\n\n## Do\n", "", false, true},
		{"# T-1 — A\n\nDifficulty: hard\nFixes: RV-12\n\n## Do\n", "", false, false},
		{"# T-1 — A\n\nDifficulty: simple\n\n## Do\n", "", false, false},
		{"# T-1 — A\n\nDifficulty: hard\nReview: on merge\nFixes: RV-12\n", "on merge", true, false},
		{"# T-1 — A\n\nDifficulty: medium\nReview: on merge\nFixes: RV-12\n", "on merge", true, false},
	} {
		value, ok := ReviewValue(c.text)
		if value != c.value || ok != c.ok {
			t.Errorf("ReviewValue(%q) = %q, %v; want %q, %v", c.text, value, ok, c.value, c.ok)
		}
		if got := ReviewOnMerge(c.text); got != c.merge {
			t.Errorf("ReviewOnMerge(%q) = %v, want %v", c.text, got, c.merge)
		}
	}
}

// T-924: the Fixes: line between the title and the first "## " heading: the review (its first word) and the free text.
func TestFixes(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text, review, note string
		ok                 bool
	}{
		{"# T-1 — A\n\nDifficulty: medium\nFixes: RV-12\n\n## Do\n", "RV-12", "", true},
		{"# T-1 — A\n\nFixes: RV-12 #1, #3 — re-run with an old status JSON\nReview: on merge\n", "RV-12",
			"#1, #3 — re-run with an old status JSON", true},
		{"# T-1 — A\n\nFixes:   RV-3    two  spaces \n", "RV-3", "two  spaces", true},
		{"# T-1 — A\n\nDifficulty: medium\n\n## Do\nFixes: RV-12\n", "", "", false},
		{"Fixes: RV-12\n", "", "", false},
		{"", "", "", false},
	} {
		review, note, ok := Fixes(c.text)
		if review != c.review || note != c.note || ok != c.ok {
			t.Errorf("Fixes(%q) = %q, %q, %v; want %q, %q, %v", c.text, review, note, ok, c.review, c.note, c.ok)
		}
	}
}
