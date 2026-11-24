package main

import (
	"slices"
	"testing"
)

// A marker in the prompt alone (the model asked back) is no reply; the model's step carrying it is.
func TestAgyReplied(t *testing.T) {
	prompt := `{"type":"USER_INPUT","content":"<USER_REQUEST>\n[obor conf] Reply with exactly AGY-FIRST-OK"}`
	askedBack := `{"type":"PLANNER_RESPONSE","content":"I am worker cf-l-01 on (no task). Did you mean to type here?"}`
	reply := `{"type":"PLANNER_RESPONSE","content":"AGY-FIRST-OK"}`
	cases := []struct {
		name, transcript string
		want             bool
	}{
		{"the prompt alone", prompt, false},
		{"the model asked back", prompt + "\n" + askedBack, false},
		{"the model replied", prompt + "\n" + askedBack + "\n" + reply, true},
		{"a line that does not parse", prompt + "\nAGY-FIRST-OK", false},
	}
	for _, c := range cases {
		got := agyReplied(c.transcript, "AGY-FIRST-OK")
		if got != c.want {
			t.Errorf("%s: agyReplied = %v, want %v", c.name, got, c.want)
		}
	}
}

// The model a reply ran on is its model step's, the first step carrying the marker; none when no model step carries it.
func TestAgyReplyModel(t *testing.T) {
	transcript := `{"type":"USER_INPUT","content":"Reply with exactly AGY-BARE-OK"}
{"type":"PLANNER_RESPONSE","model":"gemini-3.8-flash-medium","content":"AGY-BARE-OK"}
{"type":"PLANNER_RESPONSE","model":"gemini-3.8-flash-low","content":"AGY-BARE-OK again"}`
	if m, ok := agyReplyModel(transcript, "AGY-BARE-OK"); !ok || m != "gemini-3.8-flash-medium" {
		t.Errorf("agyReplyModel = %q, %v; want the first reply's gemini-3.8-flash-medium", m, ok)
	}
	if m, ok := agyReplyModel(transcript, "NOT-THERE"); ok || m != "" {
		t.Errorf("agyReplyModel of a marker no reply carries = %q, %v; want none", m, ok)
	}
}

// read_file's paths come back unquoted (agy writes an argument as a JSON string), in order, beside an argument that is
// not a string; another tool's Path is none, and so is a line that does not parse.
func TestAgyReadPaths(t *testing.T) {
	transcript := `{"type":"PLANNER_RESPONSE","tool_calls":[{"name":"read_file","args":{"Path":"\"/kit/templates/agents/worker.md\""}},{"name":"write_file","args":{"Path":"\"/lane/x\""}}]}
not json
{"type":"PLANNER_RESPONSE","tool_calls":[{"name":"read_file","args":{"Path":"/lane/README.md","StartLine":1}}]}`
	if got, want := agyReadPaths(transcript), []string{"/kit/templates/agents/worker.md", "/lane/README.md"}; !slices.Equal(got, want) {
		t.Errorf("agyReadPaths = %q, want %q", got, want)
	}
}
