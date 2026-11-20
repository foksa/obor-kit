package launch

import "strings"

// oneLine is text with every run of newlines (and \r) one space: a start's argument is typed as one command line at
// the pane's shell (herdr refuses one with a newline), and a typed job's text may be many lines.
// The text is otherwise unchanged.
func oneLine(text string) string {
	var b strings.Builder
	gap := false
	for _, r := range text {
		if r == '\n' || r == '\r' {
			gap = true
			continue
		}
		if gap {
			b.WriteByte(' ')
			gap = false
		}
		b.WriteRune(r)
	}
	if gap {
		b.WriteByte(' ')
	}
	return b.String()
}
