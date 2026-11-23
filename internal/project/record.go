package project

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// The bound on a state dir record file (background.jsonl, unparsed.jsonl): past recordMaxBytes an append rewrites it
// with its newest recordKeep lines.
const (
	recordMaxBytes = 64 << 10
	recordKeep     = 100
)

// appendRecord appends v as one JSON line to path (a hook: never an error, never a wait); a file grown past its bound
// keeps its newest lines.
func appendRecord(path string, v any) {
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > recordMaxBytes {
		if b, err := os.ReadFile(path); err == nil {
			lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
			lines = append(lines[max(len(lines)-recordKeep, 0):], line)
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
