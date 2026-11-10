package spec

import (
	"reflect"
	"strings"
	"testing"
)

// T-1022: one Parse reads a spec's head, every field a reader uses. Every case the per-line tests had (T-440's title,
// T-538's difficulty, T-689/T-765's models, T-878/T-932's review, T-924's fixes, T-496's protected
// paths) is a row, plus the Ref: line.
func TestParse(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want Spec
	}{
		// Title (T-440, T-790): the text after the task id of the first "# " heading; no separator or no heading is "".
		{"title: em dash", "# T-1 — a", Spec{Title: "a"}},
		{"title: colon", "# T-1: a", Spec{Title: "a"}},
		{"title: hyphen", "# T-1 - a", Spec{Title: "a"}},
		{"title: en dash", "# T-1 – a", Spec{Title: "a"}},
		{"title: none", "# T-1", Spec{}},
		{"title: empty after the colon", "# T-1:", Spec{}},
		{"title: no heading", "no heading\n## T-1 — a section\n", Spec{}},
		{"title: a section follows", "# T-05 — Committed title\n\n## Files you may touch\n", Spec{Title: "Committed title"}},
		{"title: second line heading", "intro\n# T-06 — Second line heading\n# T-06 — later\n",
			Spec{Title: "Second line heading"}},
		{"title: no separator", "# T-07 no dash\n# T-07 — later\n", Spec{}},
		{"title: blank", "# T-09 —   \n", Spec{}},

		// Difficulty (T-538): the level is the Difficulty line right under the title (blank lines between are fine).
		{"difficulty: medium", "# T-1 — A\n\nDifficulty: medium\n\n## Do\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"difficulty: spaces", "# T-1 — A\nDifficulty:   hard  \n", Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},
		{"difficulty: no final newline", "# T-1 — A\n\nDifficulty: simple",
			Spec{Title: "A", Level: "simple", LevelKnown: true}},
		{"difficulty: unknown level", "# T-1 — A\n\nDifficulty: easy\n", Spec{Title: "A", Level: "easy"}},
		{"difficulty: not under the title", "# T-1 — A\n\nwhy\n\nDifficulty: hard\n", Spec{Title: "A"}},
		{"difficulty: missing", "# T-1 — A\n", Spec{Title: "A"}},
		{"difficulty: no title", "Difficulty: hard\n", Spec{}},
		{"difficulty: empty text", "", Spec{}},

		// Models (T-689, T-765): the optional Model line is the next non-blank line after Difficulty.
		{"models: none", "# T-1 — A\n\nDifficulty: medium\n\n## Do\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"models: one", "# T-1 — A\n\nDifficulty: medium\nModel: opus\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Models: []string{"opus"}}},
		{"models: effort and model", "# T-1 — A\n\nDifficulty: hard\nModel: opus:high, opus\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, Models: []string{"opus:high", "opus"}, OnMerge: true}},
		{"models: blanks and a duplicate", "# T-1 — A\n\nDifficulty: hard\n\n\nModel:  opus ,opus:high, opus\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, Models: []string{"opus", "opus:high"}, OnMerge: true}},
		{"models: efforts", "# T-1 — A\n\nDifficulty: hard\nModel: opus:high, sonnet ,luna:xhigh\n", // T-695
			Spec{Title: "A", Level: "hard", LevelKnown: true, Models: []string{"opus:high", "sonnet", "luna:xhigh"}, OnMerge: true}},
		{"models: not the next line", "# T-1 — A\n\nDifficulty: hard\nwhy\nModel: opus\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},
		{"models: before difficulty", "# T-1 — A\n\nModel: opus\nDifficulty: hard\n", Spec{Title: "A"}},
		{"models: below a section", "# T-1 — A\n\nDifficulty: hard\n\n## Do\nModel: opus\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},
		{"models: a Type line is plain text", "# T-1 — A\n\nDifficulty: hard\nType: opus\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},
		{"models: no title", "Difficulty: hard\nModel: opus\n", Spec{}},
		{"models: a Type line further down", "# T-1 — A\n\nDifficulty: hard\nwhy\nType: x\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},

		// Review (T-878, T-932): the first Review: line between the title and the first "## " heading; only "on merge"
		// turns it on, a review is asked for by it or by `Difficulty: hard`, and never beside a Fixes: line.
		{"review: on merge", "# T-1 — A\n\nDifficulty: medium\nReview: on merge\n\n## Do\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Review: "on merge", ReviewOK: true, OnMerge: true}},
		{"review: none", "# T-1 — A\n\nDifficulty: medium\n\n## Do\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"review: another value", "# T-1 — A\n\nDifficulty: medium\nReview: now\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Review: "now", ReviewOK: true}},
		{"review: spaces", "# T-1 — A\n\nDifficulty: medium\nReview:   on merge  \n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Review: "on merge", ReviewOK: true, OnMerge: true}},
		{"review: below a section", "# T-1 — A\n\nDifficulty: medium\n\n## Do\nReview: on merge\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"review: before the title", "Review: on merge\n", Spec{}},
		{"review: hard asks for one", "# T-1 — A\n\nDifficulty: hard\n\n## Do\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, OnMerge: true}},
		{"review: never beside fixes", "# T-1 — A\n\nDifficulty: hard\nFixes: RV-12\n\n## Do\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, FixesReview: "RV-12", FixesOK: true}},
		{"review: simple asks for none", "# T-1 — A\n\nDifficulty: simple\n\n## Do\n",
			Spec{Title: "A", Level: "simple", LevelKnown: true}},
		{"review: hard, on merge and a fix", "# T-1 — A\n\nDifficulty: hard\nReview: on merge\nFixes: RV-12\n",
			Spec{Title: "A", Level: "hard", LevelKnown: true, Review: "on merge", ReviewOK: true,
				FixesReview: "RV-12", FixesOK: true}},
		{"review: medium, on merge and a fix", "# T-1 — A\n\nDifficulty: medium\nReview: on merge\nFixes: RV-12\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Review: "on merge", ReviewOK: true,
				FixesReview: "RV-12", FixesOK: true}},

		// Fixes (T-924): the review (its first word) and the free text after it.
		{"fixes: id alone", "# T-1 — A\n\nDifficulty: medium\nFixes: RV-12\n\n## Do\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, FixesReview: "RV-12", FixesOK: true}},
		{"fixes: id and a note", "# T-1 — A\n\nFixes: RV-12 #1, #3 — re-run with an old status JSON\nReview: on merge\n",
			Spec{Title: "A", Review: "on merge", ReviewOK: true, FixesReview: "RV-12",
				FixesNote: "#1, #3 — re-run with an old status JSON", FixesOK: true}},
		{"fixes: spaces", "# T-1 — A\n\nFixes:   RV-3    two  spaces \n",
			Spec{Title: "A", FixesReview: "RV-3", FixesNote: "two  spaces", FixesOK: true}},
		{"fixes: below a section", "# T-1 — A\n\nDifficulty: medium\n\n## Do\nFixes: RV-12\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"fixes: before the title", "Fixes: RV-12\n", Spec{}},

		// Ref (T-1022): a head line like Fixes:, between the title and the first "## " heading; the first one wins.
		{"ref: an external id", "# T-1 — A\n\nDifficulty: medium\nRef: JIRA-12\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Ref: "JIRA-12", RefOK: true}},
		{"ref: empty", "# T-1 — A\n\nDifficulty: medium\nRef:\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, RefOK: true}},
		{"ref: below a section", "# T-1 — A\n\nDifficulty: medium\n\n## Do\nRef: JIRA-12\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"ref: the first one wins", "# T-1 — A\n\nDifficulty: medium\nRef: JIRA-12\nRef: JIRA-13\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Ref: "JIRA-12", RefOK: true}},

		// Priority (T-1030): a head line like Ref:, between the title and the first "## " heading, as written (spec set
		// refuses any value but high).
		{"priority: high", "# T-1 — A\n\nDifficulty: medium\nPriority: high\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Priority: "high", PriorityOK: true}},
		{"priority: spaces", "# T-1 — A\n\nDifficulty: medium\nPriority:   high  \n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Priority: "high", PriorityOK: true}},
		{"priority: another value", "# T-1 — A\n\nDifficulty: medium\nPriority: urgent\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, Priority: "urgent", PriorityOK: true}},
		{"priority: empty", "# T-1 — A\n\nDifficulty: medium\nPriority:\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true, PriorityOK: true}},
		{"priority: below a section", "# T-1 — A\n\nDifficulty: medium\n\n## Do\nPriority: high\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},
		{"priority: none", "# T-1 — A\n\nDifficulty: medium\n",
			Spec{Title: "A", Level: "medium", LevelKnown: true}},

		// Protected (T-496): only the exact paths of that section; a glob, a blank span, another section (a longer
		// heading too) and the old files section unlock nothing.
		{"protected: exact paths only", "# T-02\n\n## Files you may touch\n`old.md`\n" +
			"## Protected files this task may edit (old)\n`no.md`\n" +
			"## Protected files this task may edit  \n`docs/design.md`, `docs/*.md` (a glob)\n\n" +
			"`` and `  ` are blank, `a[b].md` `x?`\n` AGENTS.md `\n## Tests\n`not/this.md`\n",
			Spec{Protected: []string{"docs/design.md", "AGENTS.md"}}},
		{"protected: no section", "## Files you may touch\n`docs/design.md`\n", Spec{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.text); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse(%q) = %#v, want %#v", c.text, got, c.want)
			}
		})
	}
}

// T-1133: the one function that strips the metadata line under Difficulty: the first non-blank line after it (the one
// Parse's models reads); a `Model:` anywhere else stays.
func TestWithoutModelLine(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"model line", "# T-1 — A\n\nDifficulty: simple\nModel: opus:high, haiku\n\n## Do\nfeature.txt\n",
			"# T-1 — A\n\nDifficulty: simple\n\n## Do\nfeature.txt\n"},
		{"blanks before it", "# T-1 — A\n\nDifficulty: simple\n\n\nModel: opus\n",
			"# T-1 — A\n\nDifficulty: simple\n\n\n"},
		{"empty value", "# T-1 — A\n\nDifficulty: simple\nModel:\n", "# T-1 — A\n\nDifficulty: simple\n"},
		{"no model line", "# T-1 — A\n\nDifficulty: simple\n\n## Do\n", "# T-1 — A\n\nDifficulty: simple\n\n## Do\n"},
		{"not the next line", "# T-1 — A\n\nDifficulty: simple\nwhy\nModel: opus\n",
			"# T-1 — A\n\nDifficulty: simple\nwhy\nModel: opus\n"},
		{"below a section", "# T-1 — A\n\nDifficulty: simple\n\n## Do\nModel: opus\n",
			"# T-1 — A\n\nDifficulty: simple\n\n## Do\nModel: opus\n"},
		{"no title", "Difficulty: simple\nModel: opus\n", "Difficulty: simple\nModel: opus\n"},
		{"no difficulty", "# T-1 — A\n\nModel: opus\n", "# T-1 — A\n\nModel: opus\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := WithoutModelLine(c.text); got != c.want {
				t.Errorf("WithoutModelLine(%q) = %q, want %q", c.text, got, c.want)
			}
		})
	}
}

// T-1022, T-1030: obor spec set's refusals are one function over the parsed spec: no title, no known Difficulty level,
// a Review: value but "on merge", a fix with Review: on merge, an empty Ref:, a Priority: value but high.
func TestValidate(t *testing.T) {
	for _, c := range []struct{ name, text, want string }{
		{"valid", "# T-1 — A\n\nDifficulty: medium\n", ""},
		{"valid with a Ref", "# T-1 — A\n\nDifficulty: medium\nRef: JIRA-12\n", ""},
		{"no title", "# T-1\n\nDifficulty: medium\n", "the spec needs a title: `# T-1 — <title>`"},
		{"unknown difficulty", "# T-1 — A\n\nDifficulty: easy\n", "hard, medium, simple"},
		{"no difficulty", "# T-1 — A\n", "hard, medium, simple"},
		{"a Type line is plain text", "# T-1 — A\n\nDifficulty: hard\nType: opus\n", ""},
		{"another Review value", "# T-1 — A\n\nDifficulty: medium\nReview: now\n",
			`the "Review:" line takes one value: "on merge"`},
		{"a fix with Review: on merge", "# T-1 — A\n\nDifficulty: hard\nFixes: RV-12\nReview: on merge\n",
			"a fix (Fixes:) is reviewed by its verify; drop the Review: on merge line"},
		{"empty Ref", "# T-1 — A\n\nDifficulty: medium\nRef:\n", `the "Ref:" line needs an id`},
		{"Priority: high", "# T-1 — A\n\nDifficulty: medium\nPriority: high\n", ""},
		{"another Priority value", "# T-1 — A\n\nDifficulty: medium\nPriority: urgent\n",
			`the "Priority:" line takes one value: "high"`},
		{"empty Priority", "# T-1 — A\n\nDifficulty: medium\nPriority:\n",
			`the "Priority:" line takes one value: "high"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := Validate("T-1", Parse(c.text))
			switch {
			case c.want == "" && err != nil:
				t.Errorf("Validate(%q) = %v, want nil", c.text, err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("Validate(%q) = %v, want %q", c.text, err, c.want)
			}
		})
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
