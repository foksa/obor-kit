package main

// The agy rows' reply check (the live tier's pure half): this file carries no build tag, so its test runs in
// `go test ./...`.

import (
	"encoding/json"
	"strings"
)

// agyReplied is whether agy's transcript (transcript.jsonl, one step per line) has a model step (PLANNER_RESPONSE)
// whose content carries marker. The prompt that asks for marker carries it too, as a USER_INPUT step, so a marker found
// anywhere in the transcript is no reply: a model that asked back (obor's wrong-tab guard) wrote none.
func agyReplied(transcript, marker string) bool {
	_, ok := agyReplyModel(transcript, marker)
	return ok
}

// agySteps are transcript's steps (one JSON object per line; a line that does not parse is none).
func agySteps(transcript string) []agyStep {
	var steps []agyStep
	for _, line := range strings.Split(transcript, "\n") {
		var s agyStep
		if json.Unmarshal([]byte(line), &s) == nil {
			steps = append(steps, s)
		}
	}
	return steps
}

// agyStep is one transcript.jsonl step: its type, content, the model a model step ran on, and its tool calls.
type agyStep struct {
	Type      string `json:"type"`
	Content   string `json:"content"`
	Model     string `json:"model"`
	ToolCalls []struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"tool_calls"`
}

// agyReplyModel is the model of the first model step (PLANNER_RESPONSE) whose content carries marker, and whether
// there is one: the model agy ran the reply on, as its transcript names it (a slug: gemini-3.8-flash-medium).
func agyReplyModel(transcript, marker string) (string, bool) {
	for _, s := range agySteps(transcript) {
		if s.Type == "PLANNER_RESPONSE" && strings.Contains(s.Content, marker) {
			return s.Model, true
		}
	}
	return "", false
}

// agyReadPaths are the paths transcript's read_file calls named, in order: agy writes each argument as a JSON string
// inside the args object (`"Path":"\"/a/b\""`), so a quoted one is unquoted.
func agyReadPaths(transcript string) []string {
	var paths []string
	for _, s := range agySteps(transcript) {
		for _, c := range s.ToolCalls {
			var p string
			if c.Name != "read_file" || json.Unmarshal(c.Args["Path"], &p) != nil {
				continue
			}
			var unq string
			if json.Unmarshal([]byte(p), &unq) == nil {
				p = unq
			}
			paths = append(paths, p)
		}
	}
	return paths
}
