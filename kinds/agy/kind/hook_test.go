package kind_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/kinds/agy/kind"
)

// ran is one run of obor hook a fake Runner saw: the hook's name and the Claude Code event it got.
type ran struct {
	name  string
	event map[string]any
}

// fake is a Runner answering every run with out, errOut, code and err, recording each run.
func fake(runs *[]ran, out, errOut string, code int, err error) kind.Runner {
	return func(name string, event []byte) ([]byte, []byte, int, error) {
		var ev map[string]any
		if json.Unmarshal(event, &ev) != nil {
			panic("the transport sent obor hook an event that does not parse: " + string(event))
		}
		*runs = append(*runs, ran{name, ev})
		return []byte(out), []byte(errOut), code, err
	}
}

func inProject(string) bool { return true }

// hook is the transport's answer to agy's event in (in a project), as JSON, and the stderr it wrote.
func hook(t *testing.T, in string, run kind.Runner) (map[string]any, string) {
	t.Helper()
	var stderr bytes.Buffer
	b := kind.Hook([]byte(in), "/p/.agents", run, inProject, &stderr)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("the answer is no JSON object: %q", b)
	}
	return got, stderr.String()
}

func obj(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		panic(err)
	}
	return m
}

// common are the fields every agy event carries (agy-probes.md P3), and their Claude Code twins.
const common = `"conversationId": "c1", "transcriptPath": "/b/c1/transcript_full.jsonl", "cwd": "/p/wt/L-01"`

func claude(event string, extra map[string]any) map[string]any {
	m := map[string]any{"hook_event_name": event, "session_id": "c1", "transcript_path": "/b/c1/transcript_full.jsonl",
		"cwd": "/p/wt/L-01", "agent_kind": "agy"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// Each of agy's events in, as the Claude Code event obor hook <name> reads, and obor hook's answer out as agy's:
// session-start's lines and user-prompt-submit's context injected as a system message, a deny with obor's line, an
// allow as ask (never allow: agy's own check still decides), stop's block as a continue, stop-failure's nothing as no
// opinion; the conversation id is the session id, the transcript path and cwd pass through, agent_kind names agy.
func TestHookMapping(t *testing.T) {
	inject := func(text string) map[string]any {
		return map[string]any{"injectSteps": []any{map[string]any{"systemMessage": map[string]any{"systemMessage": text}}}}
	}
	for _, c := range []struct {
		name, in      string
		out, errOut   string
		code          int
		hook          string
		event, answer map[string]any
	}{
		{"session start", `{"hookEventName": "SessionStart", ` + common + `, "lastUserInput": "hi"}`,
			"obor: role worker, lane L-01\nRead the role text.\n", "", 0,
			"session-start", claude("SessionStart", nil), inject("obor: role worker, lane L-01\nRead the role text.")},
		{"session start, no lines", `{"hookEventName": "SessionStart", ` + common + `}`, "", "", 0,
			"session-start", claude("SessionStart", nil), map[string]any{}},
		{"a command, allowed", `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "go test ./...", "WaitMsBeforeAsync": 5000}}}`,
			"", "", 0,
			"pre-tool-use", claude("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}),
			map[string]any{"decision": "ask"}},
		{"a command in another dir, denied", `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "git commit -m x", "Cwd": "/p/it's"}}}`,
			"", "obor: git writes are denied in a lane\n", 2,
			"pre-tool-use", claude("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": `cd '/p/it'\''s' && git commit -m x`}}),
			map[string]any{"decision": "deny", "reason": "obor: git writes are denied in a lane"}},
		{"a write", `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "write_file", "args": {"Path": "/p/wt/L-01/a.go", "Content": "x"}}}`,
			"", "", 0,
			"pre-tool-use", claude("PreToolUse", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "/p/wt/L-01/a.go"}}),
			map[string]any{"decision": "ask"}},
		{"an edit, relative path", `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "edit_file", "args": {"Path": "docs/design.md", "Target": "a", "Replacement": "b"}}}`,
			"", "obor: docs/design.md is protected\n", 2,
			"pre-tool-use", claude("PreToolUse", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/p/wt/L-01/docs/design.md"}}),
			map[string]any{"decision": "deny", "reason": "obor: docs/design.md is protected"}},
		{"a turn's first model call", `{"hookEventName": "PreInvocation", ` + common + `, "invocationNum": 0, "lastUserInput": "[obor job T-1/1/work] go"}`,
			`{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"lead lines: L-01 is done"}}`, "", 0,
			"user-prompt-submit", claude("UserPromptSubmit", map[string]any{"prompt": "[obor job T-1/1/work] go"}), inject("lead lines: L-01 is done")},
		{"a turn's first model call, no context", `{"hookEventName": "PreInvocation", ` + common + `, "invocationNum": 0, "lastUserInput": "x"}`,
			"", "", 0,
			"user-prompt-submit", claude("UserPromptSubmit", map[string]any{"prompt": "x"}), map[string]any{}},
		{"a stop, blocked", `{"hookEventName": "Stop", ` + common + `, "terminationReason": "NO_TOOL_CALL", "executionNum": 0, "fullyIdle": true}`,
			`{"decision":"block","reason":"call task_done first"}`, "", 0,
			"stop", claude("Stop", map[string]any{"stop_hook_active": false, "background_tasks": []any{}}),
			map[string]any{"decision": "continue", "reason": "call task_done first"}},
		{"a stop in a stop loop, a background run", `{"hookEventName": "Stop", ` + common + `, "terminationReason": "NO_TOOL_CALL", "executionNum": 1, "fullyIdle": false}`,
			"", "", 0,
			"stop", claude("Stop", map[string]any{"stop_hook_active": true, "background_tasks": []any{map[string]any{"status": "running"}}}),
			map[string]any{}},
		{"a stop on an error", `{"hookEventName": "Stop", ` + common + `, "terminationReason": "ERROR", "error": "RESOURCE_EXHAUSTED: quota"}`,
			"", "", 0,
			"stop-failure", claude("StopFailure", map[string]any{"error": "RESOURCE_EXHAUSTED: quota"}), map[string]any{}},
	} {
		var runs []ran
		got, _ := hook(t, c.in, fake(&runs, c.out, c.errOut, c.code, nil))
		if len(runs) != 1 || runs[0].name != c.hook || !reflect.DeepEqual(runs[0].event, c.event) {
			t.Errorf("%s: obor hook ran %+v, want one run of %s with %v", c.name, runs, c.hook, c.event)
		}
		if !reflect.DeepEqual(got, c.answer) {
			t.Errorf("%s: the answer = %v, want %v", c.name, got, c.answer)
		}
	}
}

// Events obor has no hook for answer at once, obor not run: a later model call of a turn and agy's other events no
// opinion, a tool with no Claude twin ask.
func TestHookNoRun(t *testing.T) {
	for in, want := range map[string]map[string]any{
		`{"hookEventName": "PreInvocation", ` + common + `, "invocationNum": 2}`:                                       {},
		`{"hookEventName": "PostToolUse", ` + common + `, "toolCall": {"name": "run_command"}}`:                        {},
		`{"hookEventName": "PostInvocation", ` + common + `}`:                                                          {},
		`{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "read_file", "args": {"Path": "/p/a"}}}`: {"decision": "ask"},
	} {
		var runs []ran
		got, _ := hook(t, in, fake(&runs, "", "", 0, nil))
		if len(runs) != 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ran %+v, answered %v; want no run and %v", in, runs, got, want)
		}
	}
}

// The transport always answers: obor hook pre-tool-use not found, exiting non-zero but its denial, or slow is a deny
// naming what failed (its stderr's first line with it); any other hook failing is no opinion, said on stderr. A
// PreToolUse event that does not parse is a deny.
func TestHookFailures(t *testing.T) {
	tool := `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "ls"}}}`
	stop := `{"hookEventName": "Stop", ` + common + `, "terminationReason": "NO_TOOL_CALL"}`
	for name, c := range map[string]struct {
		errOut string
		code   int
		err    error
		reason string
	}{
		"obor missing": {"", 0, errors.New(`exec: "/kit/dist/obor": no such file or directory`), `exec: "/kit/dist/obor": no such file or directory`},
		"non-zero":     {"panic: boom\ngoroutine 1\n", 1, nil, "exited 1: panic: boom"},
		"slow":         {"", -1, errors.New("no answer within 25s"), "no answer within 25s"},
	} {
		var runs []ran
		got, _ := hook(t, tool, fake(&runs, "", c.errOut, c.code, c.err))
		want := map[string]any{"decision": "deny",
			"reason": "obor: the hook transport denies the call: obor hook pre-tool-use " + c.reason}
		if len(runs) != 1 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: pre-tool-use ran %d times, answered %v; want %v", name, len(runs), got, want)
		}
		runs = nil
		got, stderr := hook(t, stop, fake(&runs, `{"decision":"block","reason":"x"}`, c.errOut, c.code, c.err))
		if len(runs) != 1 || !reflect.DeepEqual(got, map[string]any{}) ||
			!strings.Contains(stderr, "obor-kind-agy: obor hook stop: "+c.reason) {
			t.Errorf("%s: stop ran %d times, answered %v, stderr %q; want no opinion and the failure on stderr", name,
				len(runs), got, stderr)
		}
	}
	var runs []ran
	got, _ := hook(t, `{"hookEventName": "PreToolUse", "toolCall": `, fake(&runs, "", "", 0, nil))
	if len(runs) != 0 || got["decision"] != "deny" || !strings.HasPrefix(got["reason"].(string), "obor: cannot read agy's hook event: ") {
		t.Errorf("an event that does not parse: ran %d, answered %v; want a deny", len(runs), got)
	}
}

// Outside an obor project (the event's cwd, else the hook's own dir) obor is not run: a tool call is ask, any other
// event no opinion, an event that does not parse ask.
func TestHookOutsideProject(t *testing.T) {
	var asked []string
	in := func(dir string) bool { asked = append(asked, dir); return false }
	for ev, want := range map[string]map[string]any{
		`{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "ls"}}}`: {"decision": "ask"},
		`{"hookEventName": "SessionStart", ` + common + `}`:                                                                   {},
		`{"hookEventName": "Stop", ` + common + `}`:                                                                           {},
		`not json`: {"decision": "ask"},
	} {
		var runs []ran
		var got map[string]any
		_ = json.Unmarshal(kind.Hook([]byte(ev), "/p/.agents", fake(&runs, "", "", 2, nil), in, &bytes.Buffer{}), &got)
		if len(runs) != 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ran %d, answered %v; want no run and %v", ev, len(runs), got, want)
		}
	}
	slices.Sort(asked)
	if want := []string{"/p/.agents", "/p/wt/L-01", "/p/wt/L-01", "/p/wt/L-01"}; !slices.Equal(asked, want) {
		t.Errorf("InProject asked of %q, want %q: the event's cwd, the hook's dir for the event with none", asked, want)
	}
}

// InProject: a dir under a checkout holding .obor/config.json; a linked worktree whose main checkout holds it (the
// worktree's .git file, its gitdir's commondir), though the worktree has no config of its own; not a dir of no project.
func TestInProject(t *testing.T) {
	base := t.TempDir()
	main, lane, other := filepath.Join(base, "main"), filepath.Join(base, "wt", "L-01"), filepath.Join(base, "other")
	for path, text := range map[string]string{
		filepath.Join(main, ".obor", "config.json"):                   "{}",
		filepath.Join(main, ".git", "worktrees", "L-01", "commondir"): "../..\n",
		filepath.Join(lane, ".git"):                                   "gitdir: " + filepath.Join(main, ".git", "worktrees", "L-01") + "\n",
		filepath.Join(lane, "pkg", "a.go"):                            "package a",
		filepath.Join(other, ".git", "HEAD"):                          "ref: refs/heads/main",
		filepath.Join(other, "sub", "b.go"):                           "package b",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(lane, ".obor")); err == nil {
		t.Fatal("precondition: the lane has no config of its own")
	}
	for dir, want := range map[string]bool{main: true, filepath.Join(main, ".agents"): true, lane: true,
		filepath.Join(lane, "pkg"): true, filepath.Join(other, "sub"): false, "": false} {
		if got := kind.InProject(dir); got != want {
			t.Errorf("InProject(%q) = %v, want %v", dir, got, want)
		}
	}
}

// HooksEntry is obor's named hook as the probes' fixture spells one: SessionStart, PreInvocation and Stop flat, each
// running obor kind agy hook with agy's 30 s timeout; PreToolUse one group per guarded tool, its matcher the tool's
// name.
func TestHooksEntry(t *testing.T) {
	h := map[string]any{"type": "command", "command": "obor kind agy hook", "timeout": 30}
	group := func(tool string) any { return map[string]any{"matcher": tool, "hooks": []any{h}} }
	want := map[string]any{"SessionStart": []any{h}, "PreInvocation": []any{h}, "Stop": []any{h},
		"PreToolUse": []any{group("run_command"), group("write_file"), group("edit_file")}}
	b, _ := json.Marshal(kind.HooksEntry())
	if got := obj(string(b)); !reflect.DeepEqual(got, obj(mustJSON(want))) {
		t.Errorf("HooksEntry = %v, want %v", got, want)
	}
	if kind.HookTimeout.Seconds() >= kind.HookTimeoutSeconds {
		t.Errorf("HookTimeout %s is not under agy's %ds: agy would kill the transport before it answers", kind.HookTimeout, kind.HookTimeoutSeconds)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
