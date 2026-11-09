// Package spec reads a task spec's text once (Parse) into the fields its readers use, refuses what obor spec set may
// not store (Validate); and matches path globs (config globs: protected_files, merge classes). The text is the store's
// (internal/store specs, T-440): a row carries the parsed value (T-1022).
package spec

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Spec is a task spec's parsed head (Parse): every field a reader takes from the text, read once.
type Spec struct {
	Title       string   // the text after the task id of the first "# " heading ("" when there is no title)
	Level       string   // the Difficulty: level, as written ("" when no line)
	LevelKnown  bool     // whether Level is one of Levels
	Models      []string // the Model: refs, as written, first occurrence of each
	OldType     bool     // a Type: line where Model: belongs (T-765)
	Review      string   // the Review: value ("" when no line)
	ReviewOK    bool     // a Review: line
	OnMerge     bool     // asks a review on merge: Review: on merge or Difficulty: hard, and no Fixes: (T-878, T-932)
	FixesReview string   // Fixes: the review it fixes (T-924)
	FixesNote   string   // and the free text after it
	FixesOK     bool     // a Fixes: line
	Protected   []string // the exact paths of the "## Protected files this task may edit" section
	Ref         string   // Ref: an external tracker's id ("" when none)
	RefOK       bool     // a Ref: line
	Priority    string   // the Priority: value, as written ("" when no line)
	PriorityOK  bool     // a Priority: line
}

// Parse reads a spec's text into its head: never fails, an old stored spec parses as its lines read today.
func Parse(text string) Spec {
	level, known := difficulty(text)
	review, reviewOK := reviewValue(text)
	fixesReview, fixesNote, fixesOK := fixes(text)
	ref, refOK := headValue(text, "Ref:")
	priority, priorityOK := headValue(text, "Priority:")
	return Spec{
		Title: title(text), Level: level, LevelKnown: known, Models: models(text), OldType: oldTypeLine(text),
		Review: review, ReviewOK: reviewOK, OnMerge: reviewOnMerge(text),
		FixesReview: fixesReview, FixesNote: fixesNote, FixesOK: fixesOK, Protected: protected(text),
		Ref: ref, RefOK: refOK, Priority: priority, PriorityOK: priorityOK,
	}
}

// Validate is what obor spec set refuses, in one place (T-764, T-790, T-878, T-932, T-1022, T-1030): a spec without a
// title or a known difficulty, one with the old Type: line, a Review: value but "on merge", a fix with Review: on
// merge, an empty Ref:, or a Priority: value but high. task names the spec in the messages.
func Validate(task string, s Spec) error {
	switch {
	case s.Title == "":
		return fmt.Errorf("the spec needs a title: `# %s — <title>`", task)
	case !s.LevelKnown:
		return fmt.Errorf("the spec needs a \"Difficulty: <level>\" line right under its title (%s)", strings.Join(Levels,
			", "))
	case s.OldType:
		return errors.New(`the "Type:" line is now "Model: <model>[:<effort>], …" (T-765): rename it`)
	case s.ReviewOK && s.Review != ReviewOnMergeValue:
		return fmt.Errorf("the \"Review:\" line takes one value: %q", ReviewOnMergeValue)
	case s.FixesOK && s.ReviewOK && s.Review == ReviewOnMergeValue:
		return errors.New("a fix (Fixes:) is reviewed by its verify; drop the Review: on merge line")
	case s.RefOK && s.Ref == "":
		return errors.New(`the "Ref:" line needs an id (an external tracker's id)`)
	case s.PriorityOK && s.Priority != PriorityHigh:
		return fmt.Errorf("the \"Priority:\" line takes one value: %q", PriorityHigh)
	}
	return nil
}

// ProtectedHeading is the spec section whose backticked exact paths unlock protected files for that one task (T-238,
// T-496).
const ProtectedHeading = "Protected files this task may edit"

// protected is the exact paths of the spec's "## Protected files this task may edit" section: its backticked spans
// without a wildcard (*, ?, [): a glob unlocks nothing.
func protected(text string) []string {
	var out []string
	for _, line := range section(text, ProtectedHeading) {
		for _, m := range backticked.FindAllString(line, -1) {
			if p := strings.Trim(m[1:len(m)-1], " "); p != "" && !strings.ContainsAny(p, "*?[") {
				out = append(out, p)
			}
		}
	}
	return out
}

// section is the non-blank lines of the spec's "## <heading>" section (heading followed only by blanks), as written.
func section(text, heading string) []string {
	h := "## " + heading
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.HasPrefix(line, h) && strings.Trim(line[len(h):], " \t") == ""
			continue
		}
		if in && strings.Trim(line, " \t") != "" {
			out = append(out, line)
		}
	}
	return out
}

var backticked = regexp.MustCompile("`[^`]*`")

// Match follows obor_glob_match: * and ** both span '/', and **/ also matches zero directories.
func Match(path, glob string) bool {
	re, err := compileGlob(glob)
	return err == nil && re.MatchString(path)
}

func compileGlob(glob string) (*regexp.Regexp, error) { return regexp.Compile(globRegexp(glob)) }

func globRegexp(glob string) string {
	var b strings.Builder
	b.WriteByte('^')
	for i := 0; i < len(glob); {
		switch glob[i] {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i += 2
				if i < len(glob) && glob[i] == '/' {
					b.WriteString("(.*/)?")
					i++
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString(".*")
				i++
			}
		case '?':
			b.WriteByte('.')
			i++
		case '[':
			end := strings.IndexByte(glob[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				i++
				continue
			}
			end += i + 1
			class := glob[i+1 : end]
			b.WriteByte('[')
			if strings.HasPrefix(class, "!") {
				b.WriteByte('^')
				class = class[1:]
			}
			b.WriteString(class)
			b.WriteByte(']')
			i = end + 1
		default:
			if strings.ContainsRune(`.+()|{}^$\\`, rune(glob[i])) {
				b.WriteByte('\\')
			}
			b.WriteByte(glob[i])
			i++
		}
	}
	b.WriteByte('$')
	return b.String()
}

// title is a spec text's title: the text after the task id of its first "# <task> <sep> <title>" heading, for a sep of
// "—", "–", "-" or ":" ("" when it has no such heading or no title).
func title(text string) string {
	for _, l := range strings.Split(text, "\n") {
		h, ok := strings.CutPrefix(l, "# ")
		if !ok {
			continue
		}
		rest := strings.TrimSpace(h[idEnd(h):])
		for _, sep := range []string{"—", "–", "-", ":"} {
			if title, ok := strings.CutPrefix(rest, sep); ok {
				return strings.TrimSpace(title)
			}
		}
		return ""
	}
	return ""
}

// idEnd is where a heading's task id ends: the first space, ":" or long dash after it, or a "-" that follows a digit.
func idEnd(h string) int {
	for i, r := range h {
		switch {
		case r == ' ' || r == ':' || r == '—' || r == '–':
			return i
		case r == '-' && i > 0 && h[i-1] >= '0' && h[i-1] <= '9':
			return i
		}
	}
	return len(h)
}

// Overlap reports whether globs a and b may match a common path (false: provably disjoint). Read as Match reads them,
// with * and ** matching any run (/ included) and **/ folded into a *: a superset of the per-segment reading, so an
// overlap there is one here. A [ bracket in either → may overlap (fail closed).
func Overlap(a, b string) bool {
	if strings.ContainsRune(a, '[') || strings.ContainsRune(b, '[') {
		return true
	}
	a, b = flat(a), flat(b)
	n, m := len(a), len(b)
	// d[i][j]: a[i:] and b[j:] can match a common string; filled from the ends.
	d := make([][]bool, n+1)
	for i := range d {
		d[i] = make([]bool, m+1)
	}
	for i := n; i >= 0; i-- {
		for j := m; j >= 0; j-- {
			switch {
			case i == n && j == m:
				d[i][j] = true
			case i < n && a[i] == '*': // a's * matches nothing, or swallows b's next character
				d[i][j] = d[i+1][j] || (j < m && d[i][j+1])
			case j < m && b[j] == '*':
				d[i][j] = d[i][j+1] || (i < n && d[i+1][j])
			case i < n && j < m && (a[i] == b[j] || a[i] == '?' || b[j] == '?'):
				d[i][j] = d[i+1][j+1]
			}
		}
	}
	return d[0][0]
}

// flat folds **/ and ** into *.
func flat(g string) string {
	g = strings.ReplaceAll(g, "**/", "*")
	for strings.Contains(g, "**") {
		g = strings.ReplaceAll(g, "**", "*")
	}
	return g
}

// Levels are the difficulty levels a spec's `Difficulty:` line may name, hardest first.
var Levels = []string{"hard", "medium", "simple"}

// difficulty is the level of the spec's `Difficulty: <level>` line: the first non-blank line under the title (the
// `# ` line). ok is false when there is no such line or it names no level in Levels; level is then what it wrote ("":
// no line).
func difficulty(text string) (level string, ok bool) {
	_, v, found := difficultyLine(text)
	if !found {
		return "", false
	}
	level = strings.TrimSpace(v)
	return level, slices.Contains(Levels, level)
}

// models is the model refs of the spec's optional `Model: <model>[, <model>…]` line (T-765; it was `Type:`): the
// first non-blank line after the `Difficulty:` line. Each is a model ref, `<model>[:<effort>]` as written
// (config.ParseModelRef, T-695). nil when there is no such line (a `Model:` elsewhere is ignored).
func models(text string) []string {
	v, ok := lineAfterDifficulty(text, "Model:")
	if !ok {
		return nil
	}
	var out []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// WithoutModelLine is text without its `Model:` line, the metadata line just under `Difficulty:` that models reads: the
// stored spec a merge message carries drops that line (T-1112, T-1133); the spec's content is not redacted, so a model
// name written in it stays. A `Model:` line anywhere else stays, and text with no such line is returned unchanged.
func WithoutModelLine(text string) string {
	rest, _, found := difficultyLine(text)
	if !found {
		return text
	}
	at := slices.IndexFunc(rest, func(l string) bool { return strings.TrimSpace(l) != "" })
	if at < 0 || !strings.HasPrefix(rest[at], "Model:") {
		return text
	}
	lines := strings.Split(text, "\n")
	at += len(lines) - len(rest) // its index in lines (rest is the tail of lines)
	return strings.Join(slices.Delete(lines, at, at+1), "\n")
}

// oldTypeLine: the first non-blank line after the spec's `Difficulty:` line is the `Type:` line before T-765 (now
// `Model:`): obor spec set refuses it, so it is not silently ignored.
func oldTypeLine(text string) bool {
	_, ok := lineAfterDifficulty(text, "Type:")
	return ok
}

// ReviewOnMergeValue is the one value of the spec's optional `Review:` line (T-878).
const ReviewOnMergeValue = "on merge"

// PriorityHigh is the one value of the spec's optional `Priority:` line (T-1030): the task's waiting dispatch goes
// first for the next free lane.
const PriorityHigh = "high"

// reviewOnMerge reports whether the spec asks for a review once the task merges (T-878, T-932): the line
// `Review: on merge` between its title and its first "## " heading, or `Difficulty: hard`; never for a spec with a
// `Fixes:` line (its verify is its review).
func reviewOnMerge(text string) bool {
	if _, _, ok := fixes(text); ok {
		return false
	}
	if v, ok := reviewValue(text); ok && v == ReviewOnMergeValue {
		return true
	}
	level, ok := difficulty(text)
	return ok && level == "hard"
}

// reviewValue is the value of the spec's first `Review:` line between its title and its first "## " heading: one line
// there (T-878). ok is false when there is no such line; a `Review:` below a "## " heading is ignored. obor spec set
// refuses a value other than ReviewOnMergeValue.
func reviewValue(text string) (value string, ok bool) { return headValue(text, "Review:") }

// fixes is the spec's first `Fixes: RV-<n>[ <free text>]` line between its title and its first "## " heading (T-924):
// the review it fixes (its first word, as written) and the free text after it, the note the verify brief carries for
// this task. ok is false when there is no such line. obor dispatch refuses a review that is not the open one.
func fixes(text string) (review, note string, ok bool) {
	v, ok := headValue(text, "Fixes:")
	if !ok {
		return "", "", false
	}
	review, note, _ = strings.Cut(v, " ")
	return review, strings.TrimSpace(note), true
}

// headValue is the trimmed rest of the first line starting with key between the spec's title and its first "## "
// heading; ok false when there is none. The Review:, Fixes:, Ref: and Priority: lines are read this way.
func headValue(text, key string) (string, bool) {
	in := false
	for _, l := range strings.Split(text, "\n") {
		if !in {
			in = strings.HasPrefix(l, "# ")
			continue
		}
		if strings.HasPrefix(l, "## ") {
			break
		}
		if v, cut := strings.CutPrefix(l, key); cut {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// lineAfterDifficulty is the rest of the first non-blank line after the `Difficulty:` line when it starts with
// prefix; ok false otherwise.
func lineAfterDifficulty(text, prefix string) (string, bool) {
	rest, _, found := difficultyLine(text)
	if !found {
		return "", false
	}
	for _, next := range rest {
		if strings.TrimSpace(next) == "" {
			continue
		}
		return strings.CutPrefix(next, prefix)
	}
	return "", false
}

// difficultyLine finds the `Difficulty:` line (the first non-blank line under the title): its value, and the lines
// after it. found is false when the title has no such line.
func difficultyLine(text string) (rest []string, value string, found bool) {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "# ") {
			continue
		}
		for j, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			v, ok := strings.CutPrefix(next, "Difficulty:")
			if !ok {
				return nil, "", false
			}
			return lines[i+1+j+1:], v, true
		}
		break
	}
	return nil, "", false
}
