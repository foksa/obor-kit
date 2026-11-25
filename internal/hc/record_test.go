package hc

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"unicode/utf8"

	"obor/internal/app/port"
	"obor/internal/testkit" // every agent kind, as a config's Kinds holds them
)

// shellRecord is terminal.get of a kept shell terminal labelled obor.project=cap obor.agent=w1 after a `cd sub`, as
// hc 0.1.21 answered it on a scratch session (2026-11-20): the record's cwd is where it started, the foreground's
// follows the cd (resolved by the OS).
const shellRecord = `{"address":"t1","argv":["/bin/zsh","-l"],"cols":200,"created_ms":1795171295575,"cwd":"/tmp/obhc-3752525046/work","die_with_parent":false,"exit_code":null,"finished_ms":null,"foreground":{"argv":["/bin/zsh","-l"],"cwd":"/private/tmp/obhc-3752525046/work/sub","name":"zsh","pid":34044},"identity":"ghostty","keep":true,"labels":{"obor.agent":"w1","obor.project":"cap"},"name":null,"notify":false,"parent":null,"pid":34044,"reason":null,"reported":false,"rm":false,"rows":120,"screen":"exact","signal":null,"state":"live","status":null,"status_ms":null,"template":null,"title":"","turn":0}`

// finishedRecord is a plain terminal after its holder was lost, as the same session listed it.
const finishedRecord = `{"address":"t2","argv":["/bin/sleep","600"],"cols":200,"created_ms":1795171305935,"cwd":"/private/tmp/obhc-3752525046/work","die_with_parent":false,"exit_code":null,"finished_ms":1795171306962,"foreground":null,"identity":"ghostty","keep":false,"labels":{"obor.project":"cap"},"name":null,"notify":true,"parent":null,"pid":null,"reason":"holder_lost","reported":false,"rm":false,"rows":120,"screen":"exact","signal":null,"state":"finished","status":null,"status_ms":null,"template":null,"title":"","turn":0}`

// patched is base with the top-level fields of patch replaced (docs/research/hc-probes.md's facts on a captured record).
func patched(t *testing.T, base, patch string) string {
	t.Helper()
	var b, p map[string]any
	if err := json.Unmarshal([]byte(base), &b); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(patch), &p); err != nil {
		t.Fatal(err)
	}
	maps.Copy(b, p)
	out, _ := json.Marshal(b)
	return string(out)
}

// A record maps to the pane its labels, foreground and OSC 7501 status say: group and label from obor.project and
// obor.agent, cwd from the foreground (else the record), the kind from status.app (never foreground.name, Claude's
// version), error as idle, its note what the status says, a program with no status unknown, the shell no agent, and a
// labels change without obor.project the pane out of every group.
func TestRecordPane(t *testing.T) {
	// P1, P9: Claude typed at the shell (pid 71102), its foreground the agent under its version as process title.
	claude := `{"pid":71102,"reported":true,"foreground":{"argv":["claude","--model","haiku"],"cwd":"/private/tmp/obp/1/work/proj","name":"2.1.329","pid":71307}`
	for _, c := range []struct {
		name, json string
		want       port.Pane
	}{
		{"shell after a cd", shellRecord,
			port.Pane{ID: "t1", Terminal: "t1", Run: "34044", Group: "cap", Label: "w1", Cwd: "/private/tmp/obhc-3752525046/work/sub"}},
		{"Claude at its trust dialog", patched(t, shellRecord, claude+`,"status":{"state":"blocked","kind":"permission","msg":"trust this folder","app":"claude-code"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/private/tmp/obp/1/work/proj", Agent: "claude", Status: "blocked", Note: "permission: trust this folder"}},
		{"Claude done", patched(t, shellRecord, claude+`,"status":{"state":"done","msg":"replied OK as requested","app":"claude-code"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/private/tmp/obp/1/work/proj", Agent: "claude", Status: "done", Note: "replied OK as requested"}},
		{"Codex at its trust dialog: no report", patched(t, shellRecord, `{"pid":71102,"foreground":{"argv":["codex","--no-daemon"],"cwd":"/w","name":"codex","pid":72000}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/w", Status: "unknown"}},
		{"Codex ended a turn in an error", patched(t, shellRecord, `{"pid":71102,"reported":true,"foreground":{"argv":["codex"],"cwd":"/w","name":"codex","pid":72000},"status":{"state":"error","msg":"stream error","app":"codex"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/w", Agent: "codex", Status: "idle", Note: "error: stream error"}},
		{"OpenCode idle", patched(t, shellRecord, `{"pid":71102,"reported":true,"foreground":{"argv":["opencode","--standalone"],"cwd":"/w","name":"opencode","pid":73000},"status":{"state":"idle","app":"opencode"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/w", Agent: "opencode", Status: "idle"}},
		{"a program reporting a word the port has not", patched(t, shellRecord, `{"pid":71102,"foreground":{"argv":["x"],"cwd":"/w","name":"x","pid":74000},"status":{"state":"thinking","app":"x"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "71102", Group: "cap", Label: "w1", Cwd: "/w", Status: "unknown"}},
		{"Claude exited back to the shell: reported stays true", patched(t, shellRecord, `{"reported":true}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "34044", Group: "cap", Label: "w1", Cwd: "/private/tmp/obhc-3752525046/work/sub"}},
		{"obor.project removed: out of the group", patched(t, shellRecord, `{"labels":{"obor.agent":"w2"}}`),
			port.Pane{ID: "t1", Terminal: "t1", Run: "34044", Label: "w2", Cwd: "/private/tmp/obhc-3752525046/work/sub"}},
		{"finished: the record's cwd", finishedRecord,
			port.Pane{ID: "t2", Terminal: "t2", Group: "cap", Cwd: "/private/tmp/obhc-3752525046/work"}},
	} {
		var r Record
		if err := json.Unmarshal([]byte(c.json), &r); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := r.pane(testkit.Kinds()); got != c.want {
			t.Errorf("%s: pane = %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// A pane's Note is what its OSC 7501 status says, the word unchanged: blocked its kind and msg (either alone), error
// "error: <msg>" (or "error", the status still idle), another word its msg, no status none.
func TestRecordPaneNote(t *testing.T) {
	for _, c := range []struct {
		name       string
		status     *Status
		word, note string
	}{
		{"blocked with kind and msg", &Status{State: "blocked", Kind: "permission", Msg: "approve Bash: ls"}, "blocked", "permission: approve Bash: ls"},
		{"blocked, kind only", &Status{State: "blocked", Kind: "auth"}, "blocked", "auth"},
		{"blocked, msg only", &Status{State: "blocked", Msg: "Trust this folder?"}, "blocked", "Trust this folder?"},
		{"error with msg", &Status{State: "error", Msg: "stream error"}, "idle", "error: stream error"},
		{"error without msg", &Status{State: "error"}, "idle", "error"},
		{"working with msg", &Status{State: "working", Msg: "running tests"}, "working", "running tests"},
		{"working without msg", &Status{State: "working"}, "working", ""},
		{"blocked, its msg sanitized", &Status{State: "blocked", Kind: "question", Msg: "pick\n\x1b[1mone\x07"}, "blocked", "question: pick [1mone"},
		{"no status", nil, "", ""},
	} {
		r := Record{Address: "t1", State: "live", PID: 10, Status: c.status}
		got := r.pane(testkit.Kinds())
		if got.Status != c.word || got.Note != c.note {
			t.Errorf("%s: status %q note %q, want %q and %q", c.name, got.Status, got.Note, c.word, c.note)
		}
	}
}

// sanitized shows an agent's text on one line: control characters (C0, DEL, C1) dropped, an escape sequence left
// without its ESC, whitespace runs (newlines, tabs) one space, trimmed, a long text cut to noteMax runes ending "…".
func TestSanitized(t *testing.T) {
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
		got := sanitized(c.in)
		if got != c.want {
			t.Errorf("%s: sanitized(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if n := utf8.RuneCountInString(got); n > noteMax {
			t.Errorf("%s: %d runes, want at most %d", c.name, n, noteMax)
		}
	}
}

// kindOf finds a kind by the app name its agents report in hc's status (P1 of docs/research/hc-probes.md: Claude
// Code says claude-code, Codex and OpenCode their kind's name), and no kind by another name, Claude's version (its
// process title) or "".
func TestKindOf(t *testing.T) {
	for app, want := range map[string]string{"claude-code": "claude", "codex": "codex", "opencode": "opencode",
		"claude": "", "2.1.329": "", "vim": "", "": ""} {
		if got := kindOf(testkit.Kinds(), app); got != want {
			t.Errorf("kindOf(%q) = %q, want %q", app, got, want)
		}
	}
}
