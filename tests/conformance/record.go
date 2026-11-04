package main

// The live tier's stub record and the hook payload's fields the checks read. This file carries no build tag, so the
// checks over them run in `go test ./...` (T-820); the lab that writes records is behind the conformance tag.

import "encoding/json"

// record is one stub record (stubmcp.Record).
type record struct {
	Mode  string            `json:"mode"`
	Name  string            `json:"name"`
	PID   int               `json:"pid"`
	PPID  int               `json:"ppid"`
	Cwd   string            `json:"cwd"`
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env"`
	Stdin string            `json:"stdin"`
	file  string
}

// payload is a hook's stdin, the fields the checks read.
type payload struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Source         string `json:"source"`
	LastMessage    string `json:"last_assistant_message"`
}

func (r record) payload() payload {
	var p payload
	_ = json.Unmarshal([]byte(r.Stdin), &p)
	return p
}
