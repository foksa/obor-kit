package app

import (
	"strings"
	"testing"
)

// T-355: the subject is the spec's title (T-360: read from the target branch); without one the done file's summary,
// list marker stripped, cut on a word.
func TestSubjectOf(t *testing.T) {
	t.Parallel()
	long := "- " + strings.Repeat("word ", 30) // 150 characters
	cases := []struct {
		name, title, done, want string
	}{
		{"spec title", "Cutover follow-ups: clean subjects", "Summary: ignored", "Cutover follow-ups: clean subjects"},
		{"no spec, marker stripped", "", "Summary: - did the thing", "did the thing"},
		{"spec without a title", "", "Summary: * did it", "did it"},
		{"no spec, long summary cut on a word", "", "Summary: " + long, strings.TrimSpace(strings.Repeat("word ", 20))},
		{"no spec, no summary", "", "", "merged by obor serve"},
	}
	for _, c := range cases {
		got := subjectOf(c.title, c.done)
		if got != c.want {
			t.Errorf("%s: subject %q, want %q", c.name, got, c.want)
		}
		if c.title == "" && len(got) > 100 {
			t.Errorf("%s: %d characters, want at most 100", c.name, len(got))
		}
	}
}
