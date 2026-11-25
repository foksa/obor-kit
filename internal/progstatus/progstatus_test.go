package progstatus

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A report's word is the port's status: idle, working, blocked and done as they are, error as idle (the turn ended,
// the agent takes a prompt again), any other word unknown.
func TestWord(t *testing.T) {
	for state, want := range map[string]string{
		"idle": "idle", "working": "working", "blocked": "blocked", "done": "done",
		"error": "idle", "clear": "unknown", "thinking": "unknown", "": "unknown",
	} {
		if got := (Report{State: state}).Word(); got != want {
			t.Errorf("Report{State: %q}.Word() = %q, want %q", state, got, want)
		}
	}
}

// A report's note is what it says, the word unchanged: blocked its kind and msg (either alone), error "error: <msg>"
// (or "error"), another word its msg, an empty report none.
func TestNote(t *testing.T) {
	for _, c := range []struct {
		name   string
		report Report
		want   string
	}{
		{"blocked with kind and msg", Report{State: "blocked", Kind: "permission", Msg: "approve Bash: ls"}, "permission: approve Bash: ls"},
		{"blocked, kind only", Report{State: "blocked", Kind: "auth"}, "auth"},
		{"blocked, msg only", Report{State: "blocked", Msg: "Trust this folder?"}, "Trust this folder?"},
		{"error with msg", Report{State: "error", Msg: "stream error"}, "error: stream error"},
		{"error without msg", Report{State: "error"}, "error"},
		{"working with msg", Report{State: "working", Msg: "running tests"}, "running tests"},
		{"working without msg", Report{State: "working"}, ""},
		{"blocked, its msg shown", Report{State: "blocked", Kind: "question", Msg: "pick\n\x1b[1mone\x07"}, "question: pick [1mone"},
		{"no report", Report{}, ""},
	} {
		if got := c.report.Note(); got != c.want {
			t.Errorf("%s: Note() = %q, want %q", c.name, got, c.want)
		}
	}
}

// Shown shows a program's text on one line: control characters (C0, DEL, C1) dropped, an escape sequence left without
// its ESC, whitespace runs (newlines, tabs) one space, trimmed, a long text cut to noteMax runes ending "…".
func TestShown(t *testing.T) {
	long := strings.Repeat("é", noteMax+20)
	for _, c := range []struct{ name, in, want string }{
		{"plain", "approve Bash: ls", "approve Bash: ls"},
		{"control characters", "a\x00b\x07c\x7fd\u0085e\u009bf", "abcd ef"},
		{"escape sequences", "\x1b[31mred\x1b[0m \x1b]0;title\x07x", "[31mred[0m ]0;titlex"},
		{"newlines and tabs", "  line one\r\n\tline two\n\n", "line one line two"},
		{"markup stays text", "<b>bold</b>", "<b>bold</b>"},
		{"at the cap", strings.Repeat("x", noteMax), strings.Repeat("x", noteMax)},
		{"long", long, strings.Repeat("é", noteMax-1) + "…"},
		{"cut at a space", strings.Repeat("x", noteMax-2) + " yyyy", strings.Repeat("x", noteMax-2) + "…"},
		{"empty", " \n\t", ""},
	} {
		got := Shown(c.in)
		if got != c.want {
			t.Errorf("%s: Shown(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > noteMax {
			t.Errorf("%s: %d runes, want at most %d", c.name, n, noteMax)
		}
	}
}

// TurnEnded: the ready words (idle, done) end a turn for one another (OpenCode says idle, Claude and Codex done), an
// exact other word ends its own wait, and any other word ends none.
func TestTurnEnded(t *testing.T) {
	for _, c := range []struct {
		name  string
		word  string
		until []string
		want  bool
	}{
		{"done meets idle or done", "done", []string{"idle", "done"}, true},
		{"idle meets a wait for done", "idle", []string{"done"}, true},
		{"done meets a wait for idle", "done", []string{"idle"}, true},
		{"working ends nothing", "working", []string{"idle", "done"}, false},
		{"blocked ends nothing", "blocked", []string{"idle"}, false},
		{"an exact other word", "working", []string{"working"}, true},
		{"unknown ends nothing", "unknown", []string{"idle", "done"}, false},
	} {
		if got := TurnEnded(c.word, c.until); got != c.want {
			t.Errorf("%s: TurnEnded(%q, %q) = %v, want %v", c.name, c.word, c.until, got, c.want)
		}
	}
}
