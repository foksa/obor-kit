package launch

import "strings"

// oneLine is text with every run of newlines (and \r) one space: herdr refuses an agent.start argument with a newline
// ("agent arguments cannot be encoded safely for the target shell", T-444), and a typed job's text may be many lines.
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
