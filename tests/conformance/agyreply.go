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
	for _, line := range strings.Split(transcript, "\n") {
		var step struct {
			Type    string `json:"type"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(line), &step) != nil {
			continue
		}
		if step.Type == "PLANNER_RESPONSE" && strings.Contains(step.Content, marker) {
			return true
		}
	}
	return false
}
