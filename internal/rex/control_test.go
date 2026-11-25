package rex

import (
	"encoding/json"
	"slices"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// A prompt's paste is one block.event of type paste carrying the text whole (several lines stand in an agent's input
// as one prompt, R7), Enter is `rex send-key enter`, and obor's other keys map to Rex's binding names (a digit as it
// is).
func TestTypingRequests(t *testing.T) {
	b, err := json.Marshal(pasteParams("session:s", "block:b", "line one\nline two"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"block_id":"block:b","event":{"type":"paste","payload":{"text":"line one\nline two"}},"session_id":"session:s"}`
	if string(b) != want {
		t.Errorf("paste params %s, want %s", b, want)
	}
	got := keyArgs("session:s", "block:b", []string{port.KeyEnter, port.KeyEsc, port.KeyCtrlC, port.KeyCtrlU, "1"})
	if want := []string{"send-key", "-s", "session:s", "-b", "block:b", "enter", "escape", "ctrl+c", "ctrl+u", "1"}; !slices.Equal(got, want) {
		t.Errorf("send-key args %q, want %q", got, want)
	}
}

// A start reads a block as up only by the owner rule: a program in the foreground (not the shell) whose root record
// it owns is up (its report's word); one that has not reported is a program with no report (shellpane: NotReady at
// the bound); a record a dead agent left, its shell back in the foreground, is no start at all (no program), and one
// left behind for a newer program in the foreground is no report of that program. No foreground: the shell is not up.
func TestStartedRule(t *testing.T) {
	login := Process{PID: 100, Name: "login", Path: "/usr/bin/login"}
	zsh := Process{PID: 200, Name: "zsh", Path: "/bin/zsh", Argv0: "-zsh"}
	claude := Process{PID: 300, Name: "2.1.332", Path: "/u/.local/share/claude/versions/2.1.332", Argv0: "claude"}
	newer := Process{PID: 400, Name: "2.1.332", Path: claude.Path, Argv0: "claude"}
	rec := func(state string, owner int) *Record {
		r := &Record{State: state, App: "claude-code"}
		r.Owner = &struct {
			PID int `json:"pid"`
		}{owner}
		return r
	}
	under := func(fg Process) Processes {
		return Processes{Child: &login, Foreground: &fg, Ancestors: []Process{login, zsh}}
	}
	for _, c := range []struct {
		name     string
		proc     Processes
		status   *Record
		up       bool
		program  string
		reported bool
		word     string
	}{
		{"reported: its own record", under(claude), rec("idle", 300), true, "claude", true, "idle"},
		{"not reported yet", under(claude), nil, true, "claude", false, "unknown"},
		{"a dead owner's record, the shell back", Processes{Child: &login, Foreground: &zsh, Ancestors: []Process{login}},
			rec("done", 300), true, "", false, ""},
		{"a dead owner's record, a newer program", under(newer), rec("done", 300), true, "claude", false, "unknown"},
		{"no foreground", Processes{Child: &login}, nil, false, "", false, ""},
	} {
		l := term{Block: Block{ID: "block:b", Label: "pj-w1"}, Proc: c.proc, Status: c.status}.started(testkit.Kinds())
		if l.Up != c.up || l.Program != c.program || l.Reported != c.reported || l.Pane.Status != c.word || l.Pane.Label != "pj-w1" {
			t.Errorf("%s: %+v; want up %v, program %q, reported %v, status %q, label pj-w1", c.name, l, c.up, c.program, c.reported, c.word)
		}
	}
}

// A label Rex made up is no label: the flavor's short name a new block starts with and the default an empty label
// restores ("Block " and the id's last 8 characters, docs: session.set_block_label); obor's own and any other stays.
func TestLabel(t *testing.T) {
	const id = "block:01a21254-3f53-72c6-9d28-bfc521b1db16"
	for l, want := range map[string]string{"shell": "", "Block 21b1db16": "", "Block 3f5372c6": "Block 3f5372c6", "pj-w1": "pj-w1", "": ""} {
		if got := label(Block{ID: id, Label: l}); got != want {
			t.Errorf("label(%q) = %q, want %q", l, got, want)
		}
	}
}

// A block's shell is the first process of its chain that is no login wrapper: the foreground at the shell, the
// ancestor below the agent.
func TestShell(t *testing.T) {
	login := Process{PID: 100, Name: "login", Path: "/usr/bin/login"}
	zsh := Process{PID: 200, Name: "zsh", Path: "/bin/zsh"}
	claude := Process{PID: 300, Name: "2.1.332"}
	for name, c := range map[string]struct {
		proc Processes
		want int
	}{
		"at login's shell":         {Processes{Child: &login, Foreground: &zsh, Ancestors: []Process{login}}, 200},
		"an agent at login's":      {Processes{Child: &login, Foreground: &claude, Ancestors: []Process{login, zsh}}, 200},
		"the block's own shell":    {Processes{Child: &zsh, Foreground: &zsh}, 200},
		"login only (not up)":      {Processes{Child: &login, Foreground: &login}, 0},
		"no foreground (none yet)": {Processes{Child: &login}, 0},
	} {
		got := 0
		if sh := (term{Proc: c.proc}).shell(); sh != nil {
			got = sh.PID
		}
		if got != c.want {
			t.Errorf("%s: shell %d, want %d", name, got, c.want)
		}
	}
}
