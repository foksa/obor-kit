package main

import (
	"slices"
	"strings"
	"testing"
)

func TestToolNamesIn(t *testing.T) {
	cases := []struct {
		name, answer string
		want         []string
	}{
		{"one per line", "read_file\nrun_command\nschedule\n", []string{"read_file", "run_command", "schedule"}},
		{"a list's markers", "- read_file\n* run_command\n2. schedule\n3) manage_task", []string{"read_file", "run_command", "schedule", "manage_task"}},
		{"a heading and a sentence are no name", "Here are the tools I can call:\nread_file\nIt offers echo, a server's tool.\nrun_command works in the lane\n",
			[]string{"read_file"}},
		{"the end marker is no name", "read_file\nTOOL-LIST-DONE", []string{"read_file"}},
		{"a name's dot and hyphen stay, prose's trailing full stop does not", "read_path.fetch\nrun-workflow\netc.\n",
			[]string{"read_path.fetch", "run-workflow"}},
		{"each name once", "read_file\nrun_command\nread_file\n", []string{"read_file", "run_command"}},
		{"nothing named", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := toolNamesIn(c.answer); !slices.Equal(got, c.want) {
				t.Errorf("toolNamesIn(%q) = %q, want %q", c.answer, got, c.want)
			}
		})
	}
}

// The enumeration's prompt is a step of the record, and its answer is what follows it: an earlier turn's content stays
// out, and a list the answer splits over steps stays whole.
func TestAnswerAfterIsWhatFollowsThePrompt(t *testing.T) {
	const ask = "List every tool (function) you can call in this session"
	record := strings.Join([]string{
		`{"type":"PLANNER_RESPONSE","content":"schedule"}`,
		`{"type":"USER_INPUT","content":"[obor conf] Run exactly this shell command: obor whoami"}`,
		`{"type":"USER_INPUT","content":"[obor conf] Do not call any tool. ` + ask + `, by its exact function name. Then reply TOOL-LIST-DONE."}`,
		`{"type":"PLANNER_RESPONSE","content":"read_file\nrun_command"}`,
		`{"type":"PLANNER_RESPONSE","content":"TOOL-LIST-DONE"}`,
		`not JSON`,
	}, "\n")
	answer, ok := answerAfter(record, ask)
	if !ok {
		t.Fatal("no step carrying the prompt was found")
	}
	if strings.Contains(answer, "[obor conf]") || strings.Contains(answer, "obor whoami") {
		t.Errorf("answerAfter kept a prompt's text: %q", answer)
	}
	if names := toolNamesIn(answer); !slices.Equal(names, []string{"read_file", "run_command"}) {
		t.Errorf("the answer's names = %q, want the two the answer listed, not the earlier turn's schedule", names)
	}
}

func TestAnswerAfterNoPrompt(t *testing.T) {
	answer, ok := answerAfter(`{"content":"read_file"}`, "List every tool (function) you can call in this session")
	if ok || answer != "" {
		t.Errorf("answerAfter = %q, %v; want no answer", answer, ok)
	}
}

func TestNoRow(t *testing.T) {
	table := map[string]bool{"read_file": true, "run_command": true, "run_workflow": true}
	hasRow := func(name string) bool { return table[name] }
	cases := []struct {
		name  string
		names []string
		want  []string
	}{
		{"the table's names keep out", []string{"read_file", "run_command", "run_workflow"}, nil},
		{"a name with no row stays, in order", []string{"schedule", "read_file", "swarm"}, []string{"schedule", "swarm"}},
		{"a name once", []string{"schedule", "schedule"}, []string{"schedule"}},
		{"nothing reported", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := noRow(c.names, hasRow); !slices.Equal(got, c.want) {
				t.Errorf("noRow(%q) = %q, want %q", c.names, got, c.want)
			}
		})
	}
}
