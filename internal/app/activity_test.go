package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/claude" // the kinds these panes run: their init registers them
	_ "obor/internal/agents/codex"
	"obor/internal/app/port"
	"obor/internal/background"
)

// Turn reads the last turn of a Codex agent's rollout (found from its session id, or the path the terminal reports)
// from its tail, a long rollout's too (its first, cut line skipped); a kind whose turns are not read (Claude), no file,
// no session or no agent: unknown. A turn whose start is more than a tail back (one 300 KiB output after it,
// or thirty 10 KiB ones) is still found, by either path; an oversized task_complete is read whole; a rollout of many
// chunks with no turn event, its last line cut: unknown.
func TestTurn(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	write := func(rel, body string) string {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const (
		started = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}` + "\n"
		limited = `{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"try again at 6:30 PM","codex_error_info":"usage_limit_exceeded"}}}` + "\n"
	)
	write(".claude/projects/-proj-wt-L-01/5e1d0c2b-aaaa.jsonl", started+limited)
	write(".codex/sessions/2026/10/24/rollout-2026-10-24T15-05-17-cdx1.jsonl", started+limited)
	// over 256 KiB: one huge item line, cut at the tail's start, then the turn's end
	long := write("long.jsonl", started+`{"type":"response_item","payload":{"output":"`+strings.Repeat("x", 300<<10)+`"}}`+"\n"+limited)
	output := func(n int) string {
		return `{"type":"response_item","payload":{"output":"` + strings.Repeat("x", n) + `"}}` + "\n"
	}
	const newTurn = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}` + "\n"
	oneBig := started + limited + newTurn + output(300<<10)
	many := started + limited + newTurn + strings.Repeat(output(10<<10), 30)
	if len(oneBig)-len(started+limited+newTurn) <= 256<<10 || len(many)-len(started+limited+newTurn) <= 256<<10 {
		t.Fatal("precondition: the new turn's start is more than 256 KiB from the end")
	}
	bigPath := write("big.jsonl", oneBig)
	manyPath := write("many.jsonl", many)
	write(".codex/sessions/2026/10/30/rollout-2026-10-30T09-00-00-cdxbig.jsonl", oneBig)
	write(".codex/sessions/2026/10/30/rollout-2026-10-30T09-00-00-cdxmany.jsonl", many)
	huge := "try again at 6:30 PM " + strings.Repeat("y", 300<<10)
	hugeEnd := write("huge.jsonl", started+output(10)+`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","error":{"message":"`+huge+`","codex_error_info":"usage_limit_exceeded"}}}`+"\n")
	noTurn := write("noturn.jsonl", strings.Repeat(output(10<<10), 60)+`{"type":"event_msg","payload":{"type":"task_comp`)

	f := &agentFiles{home: home}
	hit := agents.Turn{ID: "t1", Error: "usage_limit_exceeded", Details: "try again at 6:30 PM", Ended: true}
	t2 := agents.Turn{ID: "t2"}
	for _, tc := range []struct {
		name       string
		pane       port.Pane
		transcript string
		want       agents.Turn
		ok         bool
	}{
		{"Codex session id: its rollout's last turn", port.Pane{Agent: "codex", Session: "cdx1"}, "", hit, true},
		{"a long rollout the terminal names", port.Pane{Agent: "codex", Transcript: long}, "", hit, true},
		{"its path: one 300 KiB output after the new start", port.Pane{Agent: "codex", Transcript: bigPath}, "", t2, true},
		{"its path: thirty 10 KiB outputs after the new start", port.Pane{Agent: "codex", Transcript: manyPath}, "", t2, true},
		{"session id: one 300 KiB output after the new start", port.Pane{Agent: "codex", Session: "cdxbig"}, "", t2, true},
		{"session id: thirty 10 KiB outputs after the new start", port.Pane{Agent: "codex", Session: "cdxmany"}, "", t2, true},
		{"an oversized task_complete with an error", port.Pane{Agent: "codex", Transcript: hugeEnd}, "",
			agents.Turn{ID: "t1", Error: "usage_limit_exceeded", Details: huge, Ended: true}, true},
		{"no turn event, the last line cut", port.Pane{Agent: "codex", Transcript: noTurn}, "", agents.Turn{}, false},
		{"Claude: turns not read", port.Pane{Agent: "claude", Session: "5e1d0c2b-aaaa"}, "", agents.Turn{}, false},
		{"no file for the id", port.Pane{Agent: "codex", Session: "no-such-file"}, "", agents.Turn{}, false},
		{"no session", port.Pane{Agent: "claude"}, "", agents.Turn{}, false},
		{"no agent", port.Pane{}, "", agents.Turn{}, false},
		{"the recorded transcript wins", port.Pane{Agent: "codex", Session: "no-such-file"}, bigPath, t2, true},
	} {
		if got, ok := f.Turn(tc.pane, tc.transcript); ok != tc.ok || got != tc.want {
			t.Errorf("%s: Turn = %.200v, %v; want %.200v, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// Activity is when an agent last wrote its transcript: a kind's session id names the file under the home (Claude's
// under .claude/projects, Codex's rollout under .codex/sessions), a terminal-reported path is taken as it is, and
// unknown stays unknown: another kind, no file, no session, no agent.
func TestActivity(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	write := func(rel string, at time.Time) string {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	at := time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC)
	claude := write(".claude/projects/-proj-wt-L-01/5e1d0c2b-aaaa.jsonl", at)
	write(".claude/projects/-proj-wt-L-01/other.jsonl", at.Add(time.Hour))
	codex := write(".codex/sessions/2026/10/24/rollout-2026-10-24T15-05-17-01a1-cdx.jsonl", at.Add(time.Minute))
	f := &agentFiles{home: home}
	for _, tc := range []struct {
		name  string
		pane  port.Pane
		want  time.Time
		known bool
	}{
		{"Claude session id: its transcript", port.Pane{Agent: "claude", Session: "5e1d0c2b-aaaa"}, at, true},
		{"Codex session id: its rollout", port.Pane{Agent: "codex", Session: "01a1-cdx"}, at.Add(time.Minute), true},
		{"the terminal's path", port.Pane{Agent: "codex", Transcript: codex}, at.Add(time.Minute), true},
		{"unknown kind", port.Pane{Agent: "gemini", Session: "5e1d0c2b-aaaa"}, time.Time{}, false},
		{"no file for the id yet", port.Pane{Agent: "claude", Session: "no-such-file"}, time.Time{}, false},
		{"no session", port.Pane{Agent: "claude"}, time.Time{}, false},
		{"no agent", port.Pane{}, time.Time{}, false},
	} {
		if last, known := f.Activity(tc.pane); known != tc.known || !last.Equal(tc.want) {
			t.Errorf("%s: Activity = %v, %v; want %v, %v", tc.name, last, known, tc.want, tc.known)
		}
	}
	// Each session id names its own file: another one in the same kind directory is not a fallback.
	if got := f.Transcript(port.Pane{Agent: "claude", Session: "other"}); got != filepath.Join(home, ".claude/projects/-proj-wt-L-01/other.jsonl") {
		t.Errorf("transcript of another session = %q", got)
	}
	if got := f.Transcript(port.Pane{Agent: "claude", Session: "5e1d0c2b-aaaa"}); got != claude {
		t.Errorf("transcript = %q, want %q", got, claude)
	}
}

// Busy: a process under the pane's agent runs in a process group of its own (a command, a background job), while
// its long-lived helpers (MCP servers, caffeinate) share its group; a pane at its shell is quiet.
func TestBusy(t *testing.T) {
	t.Parallel()
	const shell, agent = 1, 2
	for _, tc := range []struct {
		name  string
		info  port.ProcessInfo
		procs []background.Proc
		want  bool
	}{
		{"nothing under the agent", port.ProcessInfo{ShellPID: shell, FgPGID: agent},
			[]background.Proc{{PID: 1, PPID: 0, PGID: 1}, {PID: 2, PPID: 1, PGID: 2}}, false},
		{"helpers in the agent's group (MCP server, caffeinate)", port.ProcessInfo{ShellPID: shell, FgPGID: agent},
			[]background.Proc{{PID: 2, PPID: 1, PGID: 2}, {PID: 3, PPID: 2, PGID: 2}, {PID: 4, PPID: 3, PGID: 2}},
			false},
		{"a command in a group of its own", port.ProcessInfo{ShellPID: shell, FgPGID: agent},
			[]background.Proc{{PID: 2, PPID: 1, PGID: 2}, {PID: 3, PPID: 2, PGID: 2}, {PID: 50, PPID: 2, PGID: 50}},
			true},
		{"a grandchild in another group", port.ProcessInfo{ShellPID: shell, FgPGID: agent},
			[]background.Proc{{PID: 2, PPID: 1, PGID: 2}, {PID: 3, PPID: 2, PGID: 2}, {PID: 60, PPID: 3, PGID: 60}},
			true},
		{"another group not under the agent", port.ProcessInfo{ShellPID: shell, FgPGID: agent},
			[]background.Proc{{PID: 2, PPID: 1, PGID: 2}, {PID: 70, PPID: 1, PGID: 70}, {PID: 71, PPID: 70, PGID: 71}},
			false},
		{"the shell has the foreground", port.ProcessInfo{ShellPID: shell, FgPGID: shell},
			[]background.Proc{{PID: 1, PPID: 0, PGID: 1}, {PID: 80, PPID: 1, PGID: 80}}, false},
	} {
		f := &agentFiles{
			info:  func(context.Context, string) (port.ProcessInfo, error) { return tc.info, nil },
			procs: func(context.Context) ([]background.Proc, error) { return tc.procs, nil },
		}
		if got, err := f.Busy(context.Background(), port.Pane{ID: "w1:p1"}); err != nil || got != tc.want {
			t.Errorf("%s: Busy = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

// The cases obor cannot tell are errors (unknown), never (false, nil): unknown is never idle.
func TestBusyUnknown(t *testing.T) {
	t.Parallel()
	info := func(pgid int) func(context.Context, string) (port.ProcessInfo, error) {
		return func(context.Context, string) (port.ProcessInfo, error) {
			return port.ProcessInfo{ShellPID: 1, FgPGID: pgid}, nil
		}
	}
	procs := func(ps ...background.Proc) func(context.Context) ([]background.Proc, error) {
		return func(context.Context) ([]background.Proc, error) { return ps, nil }
	}
	for _, tc := range []struct {
		name  string
		files agentFiles
		want  string // what the error says
	}{
		{"the foreground group is 0", agentFiles{info: info(0), procs: procs()}, "foreground process group is 0"},
		{"the agent's pid is not in ps", agentFiles{info: info(2),
			procs: procs(background.Proc{PID: 1, PPID: 0, PGID: 1}, background.Proc{PID: 70, PPID: 1, PGID: 70})},
			"pid 2 is not in ps"},
		{"ps itself fails", agentFiles{info: info(2),
			procs: func(context.Context) ([]background.Proc, error) { return nil, errors.New("ps: permission denied") }},
			"ps: permission denied"},
		{"the terminal gives no process info", agentFiles{}, "the terminal gives no process info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			busy, err := tc.files.Busy(context.Background(), port.Pane{ID: "w1:p1"})
			if busy || !errors.Is(err, ErrUnknown) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Busy = %v, %v; want false, unknown (%q)", busy, err, tc.want)
			}
		})
	}
	// A terminal's own process-info error is passed on as it is: the caller treats it as unknown too.
	some := fmt.Errorf("pane w1:p1: gone")
	f := agentFiles{info: func(context.Context, string) (port.ProcessInfo, error) { return port.ProcessInfo{}, some }}
	if _, err := f.Busy(context.Background(), port.Pane{ID: "w1:p1"}); !errors.Is(err, some) {
		t.Errorf("Busy = %v, want the terminal's error", err)
	}
}
