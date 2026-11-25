// Package progstatus is a program's own status as a terminal relays it (OSC 7501): the root record's fields and what
// obor makes of them. An adapter decodes its terminal's wire into a Report; nothing here knows a terminal.
package progstatus

import (
	"slices"
	"strings"
	"unicode"
)

// Report is a program's root status record: its own word (state), the blocked kind (permission, question, auth), its
// message and the app that reported it.
type Report struct{ State, Kind, Msg, App string }

// Word is the report as the port's status word: idle, working, blocked and done as they are, error as idle (the turn
// ended, the agent takes a prompt again), any other word unknown.
func (r Report) Word() string {
	switch r.State {
	case "idle", "working", "blocked", "done":
		return r.State
	case "error":
		return "idle"
	}
	return "unknown"
}

// Note is the report as port.Pane.Note: blocked its kind and msg ("permission: approve Bash: ls", either alone when
// the other is empty), error "error: <msg>" (or "error"), any other word its msg ("" when none); each part and the
// whole shown (Shown). The word itself decides (Word); the note is shown only.
func (r Report) Note() string {
	kind, msg := Shown(r.Kind), Shown(r.Msg)
	switch r.State {
	case "blocked":
		return Shown(joined(kind, msg))
	case "error":
		return Shown(joined("error", msg))
	}
	return msg
}

// joined is the non-empty ones of a and b, joined by ": ".
func joined(a, b string) string {
	var out []string
	for _, s := range []string{a, b} {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ": ")
}

// noteMax is the most runes a note shows: a longer one is cut to it, its last rune "…".
const noteMax = 160

// Shown is s, text a program wrote, as obor shows it on one line: control characters (C0, DEL, C1) dropped, whitespace
// runs one space, trimmed, at most noteMax runes ("…" ending a cut one). No markup is read or written: a page escapes
// it as any text.
func Shown(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r): // C0, DEL and C1
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if rs := []rune(s); len(rs) > noteMax {
		s = strings.TrimRight(string(rs[:noteMax-1]), " ") + "…"
	}
	return s
}

// TurnEnded: the status word word meets until. Any of the ready words (idle, done) is met by either: a turn ends when
// the status leaves working, and Claude and Codex then say done (and stay there) while OpenCode says idle (it never
// says done).
func TurnEnded(word string, until []string) bool {
	ready := func(w string) bool { return w == "idle" || w == "done" }
	return slices.ContainsFunc(until, func(u string) bool { return u == word || ready(u) && ready(word) })
}
