// report.go: a worker's report as its readers parse it: its sections, the questions it leaves open, the ask of a
// Blocked: report.
package app

import (
	"regexp"
	"slices"
	"strings"

	"obor/internal/config"
)

// blockedAsk is what a Blocked: report asks for (the rest of its first Blocked: line, else the next non-empty line),
// and whether report has a Blocked: line.
func blockedAsk(report string) (string, bool) {
	lines := strings.Split(report, "\n")
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, "Blocked:")
		if !ok {
			continue
		}
		for _, n := range append([]string{rest}, lines[i+1:]...) {
			if n = strings.TrimSpace(n); n != "" {
				return n, true
			}
		}
		return "(see its pane)", true
	}
	return "", false
}

// reportRefusal is why report does not submit its task ("": it does): empty, or a section of sections with no heading
// in it.
func reportRefusal(report string, sections []string) string {
	if report == "" {
		return "the report is empty: pass your final report (" + strings.Join(sections, ", ") + ") as report"
	}
	if lack := MissingSections(report, sections); len(lack) > 0 {
		return "the report has no section " + strings.Join(lack, ", ") + " (a Name: label each)"
	}
	return ""
}

// MissingSections is the sections text has no heading for (a Name: label, a ## Name or **Name** heading at the start
// of a line, any case).
func MissingSections(text string, sections []string) []string {
	lines := strings.Split(text, "\n")
	var lack []string
	for _, name := range sections {
		if name != "" && !slices.ContainsFunc(lines, headingRe(name).MatchString) {
			lack = append(lack, name)
		}
	}
	return lack
}

// headingRe is the heading of a report's labelled section name: a Name: label, a ## Name or **Name** heading at the
// start of a line, any case.
func headingRe(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)` + headingPattern(regexp.QuoteMeta(name)))
}

// headingPattern is headingRe's pattern for name already quoted, or for several names as an alternation.
func headingPattern(q string) string {
	return `^\s*(?:#{1,6}\s+` + q + `|\*\*` + q + `:?\*\*|` + q + `\s*:)`
}

// reportSections are the headings a report's section ends at: the report's own sections (config.ReportSections, the
// worker template's list, one place), and Blocked and Concerns, which a worker writes but a submitted report's section
// list does not name. Concerns is optional, so the template spells it "Concerns (optional):" too.
var reportSections = slices.Concat(config.ReportSections, []string{"Blocked", "Concerns", "Concerns (optional)"})

// reportHeadingRe is the heading of any of reportSections, in headingRe's forms: a Name: label, a ## Name or **Name**
// heading at the start of a line, any case.
var reportHeadingRe = func() *regexp.Regexp {
	q := make([]string, len(reportSections))
	for i, n := range reportSections {
		q[i] = regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?i)` + headingPattern("(?:"+strings.Join(q, "|")+")"))
}()

// reportSection is the body of text's labelled section name: the rest of its heading line, then the lines up to the
// next heading that names one of the report's own sections (reportSections), trimmed. Labels and nested headings inside
// the section stay in it: a Summary's own "Tests:" or "## Notes" is no section. "" when text has no heading for
// name.
func reportSection(text, name string) string {
	lines := strings.Split(text, "\n")
	re := headingRe(name)
	start := slices.IndexFunc(lines, re.MatchString)
	if start < 0 {
		return ""
	}
	loc := re.FindStringIndex(lines[start])
	body := []string{strings.TrimSpace(strings.TrimLeft(lines[start][loc[1]:], ":*# "))}
	for _, l := range lines[start+1:] {
		if reportHeadingRe.MatchString(l) {
			break
		}
		body = append(body, l)
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

// noOpenQuestionRe is an Open questions section that leaves nothing open: its first word is none or n/a (whatever
// follows: "none new.", "none (the lead decided X).", "None — answered"), a lone - (a "- item" is a bullet list), or
// exactly no followed by nothing or punctuation ("No."; "no idea whether X" stays a question). Case-insensitive; a
// longer word ("Nonetheless") is no match.
var noOpenQuestionRe = regexp.MustCompile(`(?i)^(?:(?:none|n/a)(?:$|[^\p{L}\p{N}])|-$|no(?:$|\s*[^\p{L}\p{N}\s]))`)

// openQuestions is report's Open questions: section when it leaves something open — anything not starting with the
// word none, n/a, - or no (see noOpenQuestionRe), verbatim; "" when it leaves nothing open.
func openQuestions(report string) string {
	q := reportSection(report, "Open questions")
	if q == "" || noOpenQuestionRe.MatchString(q) {
		return ""
	}
	return q
}
