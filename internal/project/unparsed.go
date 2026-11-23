package project

import "path/filepath"

// UnparsedFile is <root>/.obor/state/unparsed.jsonl: one line per Bash call the guard could not parse whole (a
// syntax error stops the shell walk), written by obor's PreToolUse hook so the lead can count how often agents write a
// command the parser rejects. Best-effort, like the other state-dir records.
func UnparsedFile(root string) string { return filepath.Join(StateDir(root), "unparsed.jsonl") }

// UnparsedCall is one such call: when (epoch seconds), by which agent (its name; "" when the session has none) in
// which role, the parser's error and the command's first characters.
type UnparsedCall struct {
	At      int64  `json:"at"`
	Agent   string `json:"agent,omitempty"`
	Role    string `json:"role"`
	Error   string `json:"error"`
	Command string `json:"command"`
}

// AppendUnparsed appends c to root's record file (a hook: never an error, never a wait); a file grown past its bound
// keeps its newest lines.
func AppendUnparsed(root string, c UnparsedCall) { appendRecord(UnparsedFile(root), c) }
