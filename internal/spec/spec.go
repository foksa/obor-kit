// Package spec reads a task spec's text: its title and the exact paths of its "## Protected files this task may edit"
// section; and matches path globs (config globs: protected_files, merge classes). The text is the store's
// (internal/store specs, T-440).
package spec

import (
	"regexp"
	"slices"
	"strings"
)

// ProtectedHeading is the spec section whose backticked exact paths unlock protected files for that one task (T-238,
// T-496).
const ProtectedHeading = "Protected files this task may edit"

// Protected is the exact paths of the spec's "## Protected files this task may edit" section: its backticked spans
// without a wildcard (*, ?, [): a glob unlocks nothing.
func Protected(text string) []string {
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

// Title is a spec text's title: the text after the task id of its first "# <task> <sep> <title>" heading, for a sep of
// "—", "–", "-" or ":" ("" when it has no such heading or no title).
func Title(text string) string {
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

// Difficulty is the level of the spec's `Difficulty: <level>` line: the first non-blank line under the title (the
// `# ` line). ok is false when there is no such line or it names no level in Levels; level is then what it wrote ("":
// no line).
func Difficulty(text string) (level string, ok bool) {
	_, v, found := difficultyLine(text)
	if !found {
		return "", false
	}
	level = strings.TrimSpace(v)
	return level, slices.Contains(Levels, level)
}

// Models is the model refs of the spec's optional `Model: <model>[, <model>…]` line (T-765; it was `Type:`): the
// first non-blank line after the `Difficulty:` line. Each is a model ref, `<model>[:<effort>]` as written
// (config.ParseModelRef, T-695). nil when there is no such line (a `Model:` elsewhere is ignored).
func Models(text string) []string {
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

// OldTypeLine: the first non-blank line after the spec's `Difficulty:` line is the `Type:` line before T-765 (now
// `Model:`): obor spec set refuses it, so it is not silently ignored.
func OldTypeLine(text string) bool {
	_, ok := lineAfterDifficulty(text, "Type:")
	return ok
}

// ReviewOnMergeValue is the one value of the spec's optional `Review:` line (T-878).
const ReviewOnMergeValue = "on merge"

// ReviewOnMerge reports whether the spec has the line `Review: on merge` between its title and its first "## " heading
// (T-878): serve requests a review once the task merges.
func ReviewOnMerge(text string) bool {
	v, ok := ReviewValue(text)
	return ok && v == ReviewOnMergeValue
}

// ReviewValue is the value of the spec's first `Review:` line between its title and its first "## " heading: one line
// there (T-878). ok is false when there is no such line; a `Review:` below a "## " heading is ignored. obor spec set
// refuses a value other than ReviewOnMergeValue.
func ReviewValue(text string) (value string, ok bool) {
	in := false
	for _, l := range strings.Split(text, "\n") {
		if !in {
			in = strings.HasPrefix(l, "# ")
			continue
		}
		if strings.HasPrefix(l, "## ") {
			break
		}
		if v, cut := strings.CutPrefix(l, "Review:"); cut {
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
