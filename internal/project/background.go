package project

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// BackgroundFile is <root>/.obor/state/background.jsonl: one line per Bash call an agent started in the
// background (Claude's run_in_background), written by obor's PreToolUse hook, so the readers (obor status, web) can
// show a live process under its description. Best-effort, like the other state-dir records.
func BackgroundFile(root string) string { return filepath.Join(StateDir(root), "background.jsonl") }

// BackgroundCall is one background Bash call: when (epoch seconds), by which agent (its name; "" when the session has
// none) in which pane and session, its description and its command.
type BackgroundCall struct {
	At          int64  `json:"at"`
	Agent       string `json:"agent,omitempty"`
	Pane        string `json:"pane,omitempty"`
	Session     string `json:"session,omitempty"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command"`
}

// The record file's bound: past backgroundMaxBytes an append rewrites it with its newest backgroundKeep lines.
const (
	backgroundMaxBytes = 64 << 10
	backgroundKeep     = 100
)

// AppendBackground appends c to root's record file (a hook: never an error, never a wait); a file grown past its bound
// keeps its newest lines.
func AppendBackground(root string, c BackgroundCall) {
	line, err := json.Marshal(c)
	if err != nil {
		return
	}
	path := BackgroundFile(root)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > backgroundMaxBytes {
		if b, err := os.ReadFile(path); err == nil {
			lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
			lines = append(lines[max(len(lines)-backgroundKeep, 0):], line)
			_ = WriteAtomic(path, append(bytes.Join(lines, []byte("\n")), '\n'))
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// BackgroundCalls is root's recorded background calls, oldest first (none: no file; a line that is no call is
// skipped).
func BackgroundCalls(root string) []BackgroundCall {
	f, err := os.Open(BackgroundFile(root))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []BackgroundCall
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var c BackgroundCall
		if json.Unmarshal(sc.Bytes(), &c) == nil && c.Command != "" {
			out = append(out, c)
		}
	}
	return out
}
