package spec

import (
	"reflect"
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

func TestGlobMatchMirrorsHerdStarCases(t *testing.T) {
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

// bash lib.test.sh case_glob_overlap (herd_glob_overlap, the dispatch lead guard's reading): both argument orders.
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

// T-440 (T-360's heading rule, moved from checks): a spec's title is the text after the dash of its first "# " heading;
// no dash or no heading is "".
func TestTitle(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"# T-05 — Committed title\n\n## Files you may touch\n", "Committed title"},
		{"intro\n# T-06 — Second line heading\n# T-06 — later\n", "Second line heading"},
		{"# T-07 no dash\n# T-07 — later\n", ""},
		{"no heading\n## T-08 — a section\n", ""},
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
