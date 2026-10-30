package project

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/store"
)

// TranscriptOf is the transcript file of an agent session id: the one the project at root recorded for it (serve's
// session facts, T-577; root "": none read), else as the kinds keep them (agents.Kind.Transcript, in agents.Names
// order: $CLAUDE_CONFIG_DIR (~/.claude)/projects/*/<sid>.jsonl, $CODEX_HOME
// (~/.codex)/sessions/*/*/*/rollout-*-<sid>.jsonl); "" when none, or when sid is no uuid-like word (it is matched
// inside a file name).
func TranscriptOf(root, sid string, env Env) string {
	if len(sid) < 8 || !sidRe.MatchString(sid) {
		return ""
	}
	if root != "" {
		if path := store.TranscriptAt(root, sid); path != "" {
			if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
				return path
			}
		}
	}
	for _, name := range agents.Names() {
		k, _ := agents.Lookup(name)
		if !k.Caps().Transcript {
			continue
		}
		matches, _ := filepath.Glob(k.Transcript(env.Get, "", sid))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
				return m
			}
		}
	}
	return ""
}

var sidRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ContextTokens is a session's context size from the last 400 lines of its transcript: the last main-chain Claude
// assistant line with usage (ctx = input + cache read + cache creation tokens) or Codex token_count event (ctx = last
// input tokens), and that line's time. ttl is the cache TTL of the last line that wrote the cache ("1h", "5m"; a Codex
// event: "30m"), "" when none says. ok false when there is no such line or its time is no ISO 8601 time.
func ContextTokens(file string) (ctx int, at time.Time, ttl string, ok bool) {
	if file == "" {
		return 0, time.Time{}, "", false
	}
	lines, err := TailLines(file, 400)
	if err != nil {
		return 0, time.Time{}, "", false
	}
	ts, found := "", false
	for _, line := range lines {
		var l transcriptLine
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		switch u := l.Message.Usage; {
		case l.Type == "assistant" && u != nil && !l.IsSidechain:
			ctx = u.Input + u.CacheRead + u.CacheCreation
			switch {
			case u.Creation.OneHour > 0:
				ttl = "1h"
			case u.Creation.FiveMin > 0:
				ttl = "5m"
			}
		case l.Type == "event_msg" && l.Payload.Type == "token_count" && l.Payload.Info != nil &&
			l.Payload.Info.LastTokenUsage != nil:
			ctx, ttl = l.Payload.Info.LastTokenUsage.Input, "30m"
		default:
			continue
		}
		ts, found = l.Timestamp, true
	}
	at, err = time.Parse(time.RFC3339, ts)
	if !found || err != nil || ctx < 0 {
		return 0, time.Time{}, "", false
	}
	return ctx, at, ttl, true
}

// transcriptLine is what ContextTokens reads of a transcript line: a Claude assistant message's usage, or a Codex
// token_count event.
type transcriptLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	Message     struct {
		Usage *struct {
			Input         int `json:"input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			Creation      struct {
				OneHour int `json:"ephemeral_1h_input_tokens"`
				FiveMin int `json:"ephemeral_5m_input_tokens"`
			} `json:"cache_creation"`
		} `json:"usage"`
	} `json:"message"`
	Payload struct {
		Type string `json:"type"`
		Info *struct {
			LastTokenUsage *struct {
				Input int `json:"input_tokens"`
			} `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// tailBudget bounds how far back TailLines reads: a hook reading a transcript must stay fast whatever its size.
const tailBudget = 8 << 20

// TailLines is the file's last n lines, read from the end, at most tailBudget bytes back. Stopped by the budget, it
// returns the complete lines found so far: the line the budget cut is dropped, never returned partial.
func TailLines(file string, n int) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	const chunk = 64 << 10
	var chunks [][]byte // the newest first
	pos, newlines, trail := size, 0, 0
	for pos > 0 && newlines-trail < n && size-pos < tailBudget {
		step := min(chunk, pos, tailBudget-(size-pos))
		pos -= step
		b := make([]byte, step)
		if _, err := f.ReadAt(b, pos); err != nil && err != io.EOF {
			return nil, err
		}
		if pos+step == size && b[step-1] == '\n' {
			trail = 1
		}
		newlines += bytes.Count(b, []byte{'\n'})
		chunks = append(chunks, b)
	}
	if size == 0 {
		return nil, nil
	}
	slices.Reverse(chunks)
	lines := strings.Split(strings.TrimSuffix(string(bytes.Join(chunks, nil)), "\n"), "\n")
	if pos > 0 {
		lines = lines[1:] // cut by the budget or past the n lines asked for
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// NewestChange is the later of dir's tip commit time and the newest mtime of its tracked and untracked (not ignored)
// files, in epoch seconds.
func NewestChange(dir string) int64 {
	var newest int64
	if out, err := Git(dir, "log", "-1", "--format=%ct"); err == nil {
		newest, _ = strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	}
	out, _ := Git(dir, "ls-files", "-z", "-co", "--exclude-standard")
	for _, f := range strings.Split(out, "\x00") {
		if fi, err := os.Lstat(filepath.Join(dir, f)); f != "" && err == nil && fi.ModTime().Unix() > newest {
			newest = fi.ModTime().Unix()
		}
	}
	return newest
}

// WriteAtomic writes data to a temp file next to path (mode 0644), then renames it over path: a reader sees the old
// file or the new one, never a part.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obor-tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// IsDir: p resolves to a directory.
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// FirstLine is s up to its first newline.
func FirstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// SafeName is name as a file name part: every rune but A-Za-z0-9._- becomes _; "" → unknown.
func SafeName(name string) string {
	if name == "" {
		return "unknown"
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, name)
}
