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
		{"a daemon command", `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "npm run dev", "IsDaemon": true}}}`,
			"", "", 0,
			"pre-tool-use", claude("PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm run dev", "run_in_background": true}}),
			map[string]any{"decision": "ask"}},
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
// opinion, a tool the table passes ask.
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

// Outside an obor project (the event's cwd, else the hook's own dir) obor is not run: a tool call is ask (a workflow and
// a tool the table does not know too), any other event no opinion, an event that does not parse ask.
func TestHookOutsideProject(t *testing.T) {
	var asked []string
	in := func(dir string) bool { asked = append(asked, dir); return false }
	for ev, want := range map[string]map[string]any{
		`{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_command", "args": {"CommandLine": "ls"}}}`: {"decision": "ask"},
		`{"hookEventName": "SessionStart", ` + common + `}`:                                                                   {},
		`{"hookEventName": "Stop", ` + common + `}`:                                                                           {},
		`not json`: {"decision": "ask"},
		`{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "run_workflow", "args": {"Script": "x"}}}`: {"decision": "ask"},
		`{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": "browser_click"}}`:                         {"decision": "ask"},
	} {
		var runs []ran
		var got map[string]any
		_ = json.Unmarshal(kind.Hook([]byte(ev), "/p/.agents", fake(&runs, "", "", 2, nil), in, &bytes.Buffer{}), &got)
		if len(runs) != 0 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ran %d, answered %v; want no run and %v", ev, len(runs), got, want)
		}
	}
	slices.Sort(asked)
	if want := []string{"/p/.agents", "/p/wt/L-01", "/p/wt/L-01", "/p/wt/L-01", "/p/wt/L-01", "/p/wt/L-01"}; !slices.Equal(asked, want) {
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
// running obor kind agy hook with agy's 30 s timeout; PreToolUse one group, its matcher * (every tool, agy-probes.md
// P13).
func TestHooksEntry(t *testing.T) {
	h := map[string]any{"type": "command", "command": "obor kind agy hook", "timeout": 30}
	want := map[string]any{"SessionStart": []any{h}, "PreInvocation": []any{h}, "Stop": []any{h},
		"PreToolUse": []any{map[string]any{"matcher": "*", "hooks": []any{h}}}}
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

// obor-kit's committed .agents/hooks.json holds obor's named hook as HooksEntry writes it: its PreToolUse one
// match-all group, not a list of tools.
func TestCommittedHooksFile(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", kind.HooksFile))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("%s: %v", kind.HooksFile, err)
	}
	if file["obor"] == nil {
		t.Fatalf("precondition: %s has obor's named hook: %s", kind.HooksFile, b)
	}
	if want := obj(mustJSON(kind.HooksEntry())); !reflect.DeepEqual(file["obor"], want) {
		t.Errorf("%s's obor = %v, want HooksEntry %v", kind.HooksFile, file["obor"], want)
	}
	groups, _ := file["obor"].(map[string]any)["PreToolUse"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["matcher"] != "*" {
		t.Errorf("%s's PreToolUse = %v, want one group matching every tool", kind.HooksFile, groups)
	}
}

// Every PreToolUse in a project goes by the transport's table, with obor hook answering deny (exit 2) to whatever it
// is asked: run_command, write_file and edit_file run it (its deny comes back); the tools that only read, talk or
// start a hooked child are no opinion (ask), obor not run; run_workflow and swarm are denied as a workflow script, a
// tool the table does not know (or no name) denied naming it, obor not run either.
func TestHookToolTable(t *testing.T) {
	const workflow = "obor cannot guard a workflow script; run commands with run_command"
	unknown := func(name string) map[string]any {
		return map[string]any{"decision": "deny", "reason": "obor's agy transport does not know " + name +
			"; a new agy tool needs a row in kinds/agy/kind/hook.go"}
	}
	guarded := map[string]any{"decision": "deny", "reason": "obor: denied by the guard"}
	ask := map[string]any{"decision": "ask"}
	for _, c := range []struct {
		tool string
		runs int
		want map[string]any
	}{
		{"run_command", 1, guarded}, {"write_file", 1, guarded}, {"edit_file", 1, guarded},
		{"read_file", 0, ask}, {"search_web", 0, ask}, {"list_resources", 0, ask}, {"read_resource", 0, ask},
		{"ask_question", 0, ask}, {"manage_task", 0, ask}, {"manage_subagents", 0, ask}, {"define_subagent", 0, ask},
		{"invoke_subagent", 0, ask}, {"schedule", 0, ask}, {"send_message", 0, ask}, {"finish", 0, ask},
		{"call_mcp_tool", 0, ask},
		{"run_workflow", 0, map[string]any{"decision": "deny", "reason": workflow}},
		{"swarm", 0, map[string]any{"decision": "deny", "reason": workflow}},
		{"browser_click", 0, unknown("browser_click")},
		{"Run_Command", 0, unknown("Run_Command")},
		{"", 0, unknown("a tool call with no name")},
	} {
		var runs []ran
		in := `{"hookEventName": "PreToolUse", ` + common + `, "toolCall": {"name": ` + mustJSON(c.tool) +
			`, "args": {"CommandLine": "ls", "Path": "/p/wt/L-01/a", "Script": "import os"}}}`
		got, _ := hook(t, in, fake(&runs, "", "obor: denied by the guard\n", 2, nil))
		if len(runs) != c.runs || (c.runs == 1 && runs[0].name != "pre-tool-use") {
			t.Errorf("%q: obor hook ran %+v, want %d run(s) of pre-tool-use", c.tool, runs, c.runs)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: the answer = %v, want %v", c.tool, got, c.want)
		}
	}
	// No tool event at all (agy sends PreToolUse with no toolCall): a deny, never the guard's allow.
	var runs []ran
	if got, _ := hook(t, `{"hookEventName": "PreToolUse", `+common+`}`, fake(&runs, "", "", 0, nil)); len(runs) != 0 ||
		!reflect.DeepEqual(got, unknown("a tool call with no name")) {
		t.Errorf("a PreToolUse with no toolCall: ran %d, answered %v; want a deny", len(runs), got)
	}
}
