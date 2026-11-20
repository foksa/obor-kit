package project

import (
	"regexp"
	"strings"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// ShellQuote is s as one word of a POSIX shell's command line: as it is when it holds only characters no shell reads
// specially, else in single quotes (a quote in it closed, escaped and reopened). The one quoting of what obor types
// at a pane's shell: launch's env line and the hc adapter's agent command line.
func ShellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
