package kind

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
)

// entry is the part of a session JSONL line turn reads: the session header, a message and its usage, with the line's
// time (docs/research/pi-probes.md P5; pi's session-format.md).
type entry struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role         string `json:"role"`
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
		Usage        *struct {
			Input      int `json:"input"`
			CacheRead  int `json:"cacheRead"`
			CacheWrite int `json:"cacheWrite"`
		} `json:"usage"`
	} `json:"message"`
}

// entryTime is a session line's time (an ISO 8601 string; the nested message timestamp is Unix milliseconds and names
// no zone, so the line's own time is the one read).
func entryTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Session is the session a transcript's first line names: pi's {"type":"session", version, id, timestamp, cwd}. ok
// false: the line is no session header.
func Session(first []byte) (agents.Session, bool) {
	var l entry
	if json.Unmarshal(bytes.TrimSpace(first), &l) != nil || l.Type != "session" || l.ID == "" {
		return agents.Session{}, false
	}
	return agents.Session{ID: l.ID, Cwd: l.Cwd, Started: entryTime(l.Timestamp)}, true
}

// LastTurn is a session tail's last turn: its last assistant message, whose stopReason ended it and, when that is
// "error", whose errorMessage is the failure (Details) and the reset the message names. Bytes after the last newline
// are a line still being written and are not read.
func LastTurn(tail []byte) (agents.Turn, bool) {
	end := bytes.LastIndexByte(tail, '\n')
	if end < 0 {
		return agents.Turn{}, false
	}
	tail = tail[:end]
	for len(tail) > 0 {
		line := tail
		if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
			line, tail = tail[i+1:], tail[:i]
		} else {
			tail = nil
		}
		var l entry
		if json.Unmarshal(line, &l) != nil || l.Type != "message" || l.Message.Role != "assistant" {
			continue
		}
		t := agents.Turn{ID: l.ID, Ended: l.Message.StopReason != "" && l.Message.StopReason != "pending"}
		if l.Message.StopReason == "error" {
			t.Error, t.Details = l.Message.StopReason, l.Message.ErrorMessage
			if limitRE.MatchString(t.Details) {
				t.Reset = resetAt(t.Details, entryTime(l.Timestamp))
			}
		}
		return t, true
	}
	return agents.Turn{}, false
}

// Context is a session tail's context size: the last assistant message's usage, input + cacheRead + cacheWrite (the
// whole prompt, as pi records it), at that line's time. A message a failed (error) or aborted turn wrote, or one whose
// usage is zero, is no context: it is skipped. ok false: no assistant message with usage, its time does not parse, or
// its counts are negative.
func Context(tail []byte) (agents.Context, bool) {
	c, ok := agents.Context{}, false
	for _, line := range strings.Split(string(tail), "\n") {
		var l entry
		if json.Unmarshal([]byte(line), &l) != nil || l.Type != "message" || l.Message.Role != "assistant" ||
			l.Message.Usage == nil || l.Message.StopReason == "error" || l.Message.StopReason == "aborted" {
			continue
		}
		u := l.Message.Usage
		total := u.Input + u.CacheRead + u.CacheWrite
		if total == 0 {
			continue
		}
		c = agents.Context{Tokens: total, At: entryTime(l.Timestamp)}
		ok = true
	}
	if !ok || c.Tokens < 0 || c.At.IsZero() {
		return agents.Context{}, false
	}
	return c, true
}

// limitRE is an error text that names a usage or rate limit: pi's assistant message carries the provider's own text
// (Anthropic's 429 says "rate limit", Codex's "ChatGPT usage limit"; docs/research/pi-probes.md P5), so the text is
// what tells a limit from another error.
var limitRE = regexp.MustCompile(`(?i)rate.?limit|\b429\b|usage.?limit|quota|too many requests`)

// minutesRE is a reset named as a duration: Codex's "Try again in ~120 min.".
var minutesRE = regexp.MustCompile(`(?i)try again in ~?(\d+)\s*min`)

// Reset reads a turn's error: limited when the text names a usage or rate limit, its reset the time the text names
// (a clock time or epoch as agents.ResetIn reads it, else "Try again in ~N min."), zero when it names none.
func Reset(errText, details string, at time.Time) (time.Time, bool) {
	text := errText + " " + details
	if !limitRE.MatchString(text) {
		return time.Time{}, false
	}
	return resetAt(details, at), true
}

// resetAt is the reset text names, from at: a clock time or epoch (agents.ResetIn), else a count of minutes. Zero:
// the text names none, or at is unknown.
func resetAt(text string, at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	if t := agents.ResetIn(text, at); !t.IsZero() {
		return t
	}
	if m := minutesRE.FindStringSubmatch(text); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return at.Add(time.Duration(n) * time.Minute)
		}
	}
	return time.Time{}
}
