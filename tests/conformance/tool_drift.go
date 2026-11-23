package main

// The tool drift rows' pure half (the live tier): the tools a CLI offers against the transport's table. The
// live tier reads each CLI's list (OpenCode's ctx.tool.list(), agy's P13 enumeration) and prints one WARN per tool the
// table has no row for, so a tool a CLI update adds shows as a classify-me list the morning after, not as a lane's
// failed run. This file carries no build tag, so its tests run in `go test ./...`; the live tier (live_opencode.go,
// live_agy.go) feeds it.

import (
	"encoding/json"
	"regexp"
	"strings"
)

// toolNameRE is one tool name on a line of an enumeration's answer: agy's names are lowercase (read_file, run_command),
// and a description or a sentence has a space in it. A name may carry a dot or a hyphen (an MCP tool's name can), so a
// trailing one is prose's punctuation, not a name.
var toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)

// listMarkerRE is a list's marker before a name: "- ", "* ", "1. ", "2) ".
var listMarkerRE = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)

// toolNamesIn are the tool names answer lists, one per line, each once and in order: a line trimmed of its list marker
// that is one tool name. Anything else (a heading, a sentence, the end marker) is no name.
func toolNamesIn(answer string) []string {
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(answer, "\n") {
		name := strings.TrimSpace(listMarkerRE.ReplaceAllString(strings.TrimSpace(line), ""))
		punctuated := strings.HasSuffix(name, ".") || strings.HasSuffix(name, "-") // a sentence's punctuation, not a name's
		if !toolNameRE.MatchString(name) || punctuated || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// answerAfter is a session record's text after the last step carrying prompt, and whether the record has that step: an
// enumeration's prompt is unique (agy records a prompt as a step of the conversation), so its answer is what follows
// it, however many steps the answer takes, and no earlier turn's text is read.
func answerAfter(record, prompt string) (string, bool) {
	var contents []string
	from := -1
	for _, line := range strings.Split(record, "\n") {
		var step struct {
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(line), &step) != nil {
			continue
		}
		contents = append(contents, step.Content)
		if strings.Contains(step.Content, prompt) {
			from = len(contents) - 1
		}
	}
	if from < 0 {
		return "", false
	}
	return strings.Join(contents[from+1:], "\n"), true
}

// noRow are names, in order and each once, that hasRow (the transport's table) has no row for: the tools a CLI's list
// carries that the transport cannot name, which the guard decides as unguarded.
func noRow(names []string, hasRow func(string) bool) []string {
	var unknown []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || hasRow(name) {
			continue
		}
		seen[name] = true
		unknown = append(unknown, name)
	}
	return unknown
}
