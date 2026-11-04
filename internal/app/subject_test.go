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

// T-807: the merge's body is the report's whole Summary section (an opening sentence and its bullets stay whole),
// while summaryOf is still the one-line subject; a report with no Summary heading gives its first paragraph.
func TestSummarySection(t *testing.T) {
	t.Parallel()
	const report = "Summary: Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/app/subject_test.go: these cases\n" +
		"\nSelf-check: ok.\nSkipped: none.\nOpen questions: none."
	want := "Cut the subject at 100.\n" +
		"- internal/app/steps.go: the whole section into GateInput.Body\n" +
		"- internal/app/subject_test.go: these cases"
	if got := summarySection(report); got != want {
		t.Errorf("summarySection = %q, want the whole section %q", got, want)
	}
	if got := summaryOf(report); got != "Cut the subject at 100." {
		t.Errorf("summaryOf = %q, want the one-line subject", got)
	}
	if got, want := summarySection("Summary:\n- one\n- two\nSelf-check: ok."), "- one\n- two"; got != want {
		t.Errorf("a heading alone on its line: %q, want its bullets %q", got, want)
	}
	if got, want := summarySection("Did it in two files.\nAnd a test.\n\nSelf-check: ok."), "Did it in two files.\nAnd a test."; got != want {
		t.Errorf("no Summary heading: %q, want the first paragraph %q", got, want)
	}
}
