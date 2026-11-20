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

// EnvLine is the shell line that changes to cwd ("": stays where it is) and sets env, NAME=VALUE entries: every name
// with a value exported, every name with none ("NAME=") unset, in env's order. The one builder of the env line obor
// types at a pane's shell: launch's and the hc adapter's for a terminal herdr-app started.
func EnvLine(cwd string, env []string) string {
	var unset, export []string
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		if value == "" {
			unset = append(unset, name)
		} else {
			export = append(export, name+"="+ShellQuote(value))
		}
	}
	var parts []string
	if cwd != "" {
		parts = append(parts, "cd "+ShellQuote(cwd))
	}
	if len(unset) > 0 {
		parts = append(parts, "unset "+strings.Join(unset, " "))
	}
	if len(export) > 0 {
		parts = append(parts, "export "+strings.Join(export, " "))
	}
	return strings.Join(parts, " && ")
}
