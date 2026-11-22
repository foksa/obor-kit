package kind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"obor/internal/agents"
)

// The hook transport (docs/agents/kind-protocol.md "Hook transport"): the project's committed HooksFile runs
// `obor kind agy hook` for each of agy's events, which execs this binary's `hook` call with the event on stdin. It
// rewrites agy's event as Claude Code's, runs `obor hook <name>` and rewrites the answer as agy's
// (docs/research/agent-kinds-advice.md §1.6). No obor rule lives here: the guard, the session record, the context and
// the stop are obor hook's.
//
//	SessionStart                       → session-start; its context lines → injectSteps (a system message)
//	PreToolUse run_command             → pre-tool-use, Bash (args.CommandLine → command; args.Cwd → a cd before it;
//	                                     args.IsDaemon → run_in_background)
//	PreToolUse write_file / edit_file  → pre-tool-use, Write / Edit (args.Path → file_path)
//	                                     a deny → {"decision":"deny","reason":…}; an allow → {"decision":"ask"}
//	PreToolUse call_mcp_tool / list_resources / read_resource
//	                                   → pre-tool-use, mcp (args.ServerName → mcp_server)
//	PreToolUse, any other tool         → tools' row: no opinion ({"decision":"ask"}) or a deny with its reason; a tool
//	                                     with no row is a deny naming it
//	PreInvocation, invocationNum 0     → user-prompt-submit (lastUserInput → prompt); its context → injectSteps
//	Stop                               → stop (executionNum > 0 → stop_hook_active; fullyIdle false → a running
//	                                     background task); a block → {"decision":"continue","reason":…}
//	Stop, terminationReason ERROR      → stop-failure (error → error)
//
// agy denies a tool call whose hook answers nothing, exits non-zero or takes its 30 s (docs/research/agy-probes.md
// P3), so the transport always answers, exit 0: obor hook pre-tool-use failing (obor not found, a non-zero exit but
// its denial, no answer within HookTimeout) is a deny with the reason; any other hook failing is no opinion ({}).
// Outside an obor project every event is no opinion (a tool call: ask) and obor is not run.

// HooksFile is the project file that runs obor's hooks for every agy session started in the project (or a worktree of
// it), relative to its root: agy's workspace hooks, loaded with no prompt in a trusted folder.
const HooksFile = ".agents/hooks.json"

// HookCommand is the command each of HooksEntry's handlers runs: the obor on the agent's PATH, which execs the kit's
// kind binary (obor kind).
const HookCommand = "obor kind agy hook"

// HookTimeout bounds obor hook: agy kills a hook after the entry's timeout (HookTimeoutSeconds), and the transport
// must answer before that.
const HookTimeout = 25 * time.Second

// HookTimeoutSeconds is HooksEntry's timeout per handler, agy's default made explicit.
const HookTimeoutSeconds = 30

// MatchAll is HooksEntry's one PreToolUse matcher: agy's matcher is a regular expression on the tool's name and `*`
// matches every tool (docs/research/agy-probes.md P13), so every tool call reaches the transport and its table.
const MatchAll = "*"

// HooksEntry is obor's named hook in HooksFile (the key obor): every event obor maps, each running HookCommand;
// PreToolUse one group matching every tool.
func HooksEntry() map[string]any {
	handler := func() []any {
		return []any{map[string]any{"type": "command", "command": HookCommand, "timeout": HookTimeoutSeconds}}
	}
	return map[string]any{"SessionStart": handler(), "PreInvocation": handler(), "Stop": handler(),
		"PreToolUse": []any{map[string]any{"matcher": MatchAll, "hooks": handler()}}}
}

// toolRule is what the transport does with a PreToolUse of one of agy's tools in an obor project.
type toolRule int

const (
	unknownTool toolRule = iota // no row: a deny naming the tool
	guardTool                   // obor hook pre-tool-use decides (toClaude maps it)
	passTool                    // no opinion: agy's own check decides
	denyTool                    // a deny: obor cannot guard what it runs
)

// tools is the transport's table of agy's tools (agy 1.5.14 offers the 17 of docs/research/agy-probes.md P13, and a
// workflow agent finish): the ones obor's guard checks (a command, a write, an MCP call) go to the guard; the ones that
// only read, talk or start a child whose own tool calls fire these hooks again (a subagent, a workflow agent(), a
// timer's prompt) are no opinion; a workflow script (run_workflow, swarm), whose Python runs with no hook at all, is
// denied. A tool not here is denied by name, so an agy update that adds one shows as a named deny, never a silent
// bypass.
var tools = map[string]toolRule{
	"run_command": guardTool, "write_file": guardTool, "edit_file": guardTool,
	"call_mcp_tool": guardTool, "list_resources": guardTool, "read_resource": guardTool,
	"read_file": passTool, "search_web": passTool,
	"ask_question": passTool, "manage_task": passTool, "manage_subagents": passTool, "define_subagent": passTool,
	"invoke_subagent": passTool, "schedule": passTool, "send_message": passTool, "finish": passTool,
	"run_workflow": denyTool, "swarm": denyTool,
}

// workflowDenied is the reason a denyTool row gives.
const workflowDenied = "obor cannot guard a workflow script; run commands with run_command"

// tableAnswer is agy's PreToolUse answer to a call of tool by its row in tools; guarded: the tool is the guard's
// (toClaude maps it and obor hook pre-tool-use answers), no answer here.
func tableAnswer(tool string) (answer map[string]any, guarded bool) {
	switch tools[tool] {
	case guardTool:
		return nil, true
	case passTool:
		return ask, false
	case denyTool:
		return deny(workflowDenied), false
	}
	name := tool
	if name == "" {
		name = "a tool call with no name"
	}
	return deny("obor's agy transport does not know " + name + "; a new agy tool needs a row in kinds/agy/kind/hook.go"), false
}

// Runner runs `obor hook <name>` with event on stdin: its stdout, its stderr and its exit code; err when it did not
// start or did not end within the transport's bound.
type Runner func(name string, event []byte) (stdout, stderr []byte, code int, err error)

// agyEvent is the part of agy's hook event the transport reads (agy-probes.md P3; agy's hooks.md).
type agyEvent struct {
	Event          string `json:"hookEventName"`
	ConversationID string `json:"conversationId"`
	TranscriptPath string `json:"transcriptPath"`
	Cwd            string `json:"cwd"`
	LastUserInput  string `json:"lastUserInput"`
	InvocationNum  int    `json:"invocationNum"`
	ToolCall       struct {
		Name string `json:"name"`
		Args struct {
			CommandLine string `json:"CommandLine"`
			Cwd         string `json:"Cwd"`
			IsDaemon    bool   `json:"IsDaemon"`
			Path        string `json:"Path"`
			ServerName  string `json:"ServerName"`
		} `json:"args"`
	} `json:"toolCall"`
	ExecutionNum      int    `json:"executionNum"`
	FullyIdle         *bool  `json:"fullyIdle"`
	TerminationReason string `json:"terminationReason"`
	Error             string `json:"error"`
}

var (
	noOpinion = map[string]any{}
	ask       = map[string]any{"decision": "ask"}
)

// Hook is agy's answer to its hook event in (the table above), as JSON, obor hook run through run when the event is in
// a project (inProject of the event's cwd, else of dir, the hook's own working directory). In a project an event that
// does not parse is a deny: obor cannot guard what it cannot read.
func Hook(in []byte, dir string, run Runner, inProject func(string) bool, stderr io.Writer) []byte {
	var ev agyEvent
	perr := json.Unmarshal(in, &ev)
	where := ev.Cwd
	if where == "" {
		where = dir
	}
	if !inProject(where) {
		if perr != nil || ev.Event == "PreToolUse" {
			return answer(ask)
		}
		return answer(noOpinion)
	}
	if perr != nil {
		return answer(deny("obor: cannot read agy's hook event: " + perr.Error()))
	}
	if ev.Event == "PreToolUse" {
		if a, guarded := tableAnswer(ev.ToolCall.Name); !guarded {
			return answer(a)
		}
	}
	name, claude := toClaude(ev)
	if name == "" {
		return answer(noOpinion)
	}
	event, _ := json.Marshal(claude)
	out, errOut, code, err := run(name, event)
	if len(errOut) > 0 && !(name == "pre-tool-use" && code == 2) {
		stderr.Write(errOut)
	}
	if name == "pre-tool-use" {
		return answer(toolAnswer(errOut, code, err))
	}
	if err != nil || code != 0 {
		fmt.Fprintf(stderr, "obor-kind-agy: obor hook %s: %s\n", name, failure(errOut, code, err))
		return answer(noOpinion)
	}
	return answer(fromClaude(name, out))
}

// toClaude is ev as the Claude Code event obor hook name reads, agent_kind naming this kind (session-start records the
// session as agy's); name "" when obor has no hook for it.
func toClaude(ev agyEvent) (name string, claude map[string]any) {
	claude = map[string]any{"session_id": ev.ConversationID, "transcript_path": ev.TranscriptPath, "cwd": ev.Cwd,
		"agent_kind": Name}
	switch ev.Event {
	case "SessionStart":
		claude["hook_event_name"] = "SessionStart"
		return "session-start", claude
	case "PreToolUse":
		tool, args := ev.ToolCall.Name, ev.ToolCall.Args
		claude["hook_event_name"] = "PreToolUse"
		switch tool {
		case "run_command":
			command := args.CommandLine
			if args.Cwd != "" {
				command = "cd " + shellQuote(args.Cwd) + " && " + command
			}
			input := map[string]any{"command": command}
			if args.IsDaemon { // a long-running process agy starts in the background: obor records it as Claude's
				input["run_in_background"] = true
			}
			claude["tool_name"], claude["tool_input"] = "Bash", input
		case "write_file", "edit_file":
			file := args.Path
			if file != "" && !filepath.IsAbs(file) && ev.Cwd != "" {
				file = filepath.Join(ev.Cwd, file)
			}
			claude["tool_name"] = map[string]string{"write_file": "Write", "edit_file": "Edit"}[tool]
			claude["tool_input"] = map[string]any{"file_path": file}
		case "call_mcp_tool", "list_resources", "read_resource":
			claude["tool_name"] = "mcp"
			claude["tool_input"] = map[string]any{"mcp_server": args.ServerName}
		default:
			return "", nil
		}
		return "pre-tool-use", claude
	case "PreInvocation":
		if ev.InvocationNum != 0 { // a later model call of the same turn
			return "", nil
		}
		claude["hook_event_name"], claude["prompt"] = "UserPromptSubmit", ev.LastUserInput
		return "user-prompt-submit", claude
	case "Stop":
		if ev.TerminationReason == "ERROR" {
			claude["hook_event_name"], claude["error"] = "StopFailure", ev.Error
			return "stop-failure", claude
		}
		claude["hook_event_name"], claude["stop_hook_active"] = "Stop", ev.ExecutionNum > 0
		tasks := []any{}
		if ev.FullyIdle != nil && !*ev.FullyIdle {
			tasks = append(tasks, map[string]any{"status": "running"})
		}
		claude["background_tasks"] = tasks
		return "stop", claude
	}
	return "", nil
}

// toolAnswer is agy's PreToolUse answer to obor hook pre-tool-use's run: exit 0 allows, which is no opinion (ask:
// agy's own permission check decides); exit 2 denies with obor's line; anything else denies with what failed.
func toolAnswer(errOut []byte, code int, err error) map[string]any {
	switch {
	case err == nil && code == 0:
		return ask
	case err == nil && code == 2:
		reason := strings.TrimSpace(string(errOut))
		if reason == "" {
			reason = "obor: denied"
		}
		return deny(reason)
	}
	return deny("obor: the hook transport denies the call: obor hook pre-tool-use " + failure(errOut, code, err))
}

// fromClaude is agy's answer to obor hook name's stdout out: session-start's lines and user-prompt-submit's
// additionalContext as an injected system message (it stays in the conversation), stop's block as a continue with its
// reason; nothing to say, no opinion.
func fromClaude(name string, out []byte) map[string]any {
	var text string
	switch name {
	case "session-start":
		text = strings.TrimSpace(string(out))
	case "user-prompt-submit":
		var r struct {
			Out struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal(out, &r) == nil {
			text = strings.TrimSpace(r.Out.Context)
		}
	case "stop":
		var r struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		if json.Unmarshal(out, &r) == nil && r.Decision == "block" {
			return map[string]any{"decision": "continue", "reason": r.Reason}
		}
	}
	if text == "" {
		return noOpinion
	}
	return map[string]any{"injectSteps": []any{map[string]any{"systemMessage": map[string]any{"systemMessage": text}}}}
}

func deny(reason string) map[string]any { return map[string]any{"decision": "deny", "reason": reason} }

// failure says how a run of obor hook failed: err, else its exit code, with its stderr's first line.
func failure(errOut []byte, code int, err error) string {
	why := fmt.Sprintf("exited %d", code)
	if err != nil {
		why = err.Error()
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(string(errOut)), "\n"); line != "" {
		why += ": " + line
	}
	return why
}

func answer(v map[string]any) []byte {
	b, _ := json.Marshal(v)
	return append(b, '\n')
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// RunHook is the binary's hook call: agy's event on stdin, the answer on stdout, exit 0. obor is the executable
// agents.ExeVar names ("": every run fails, naming the variable: the binary runs as obor kind agy hook).
func RunHook(stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	in, _ := io.ReadAll(stdin)
	dir, _ := os.Getwd()
	stdout.Write(Hook(in, dir, Exec(getenv(agents.ExeVar), HookTimeout), InProject, stderr))
	return 0
}

// Exec is the Runner of the obor at exe: `<exe> hook <name>` in its own process group, with this process's
// environment (the agent's identity with it), killed with its children after timeout.
func Exec(exe string, timeout time.Duration) Runner {
	return func(name string, event []byte) ([]byte, []byte, int, error) {
		if exe == "" {
			return nil, nil, 0, errors.New(agents.ExeVar + " is not set (run this as " + HookCommand + ")")
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, exe, "hook", name)
		cmd.Stdin = bytes.NewReader(event)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case ctx.Err() != nil:
			return out.Bytes(), errOut.Bytes(), -1, fmt.Errorf("no answer within %s", timeout)
		case errors.As(err, &exit):
			return out.Bytes(), errOut.Bytes(), exit.ExitCode(), nil
		case err != nil:
			return out.Bytes(), errOut.Bytes(), -1, err
		}
		return out.Bytes(), errOut.Bytes(), 0, nil
	}
}

// InProject: dir is in an obor project: a directory at or above it holds .obor/config.json, or it is in a linked
// worktree whose main checkout does (its .git file's gitdir, that dir's commondir).
func InProject(dir string) bool {
	if dir == "" {
		return false
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if fileExists(filepath.Join(d, ".obor", "config.json")) {
			return true
		}
		if main := mainCheckout(d); main != "" {
			return fileExists(filepath.Join(main, ".obor", "config.json"))
		}
		if filepath.Dir(d) == d {
			return false
		}
	}
}

// mainCheckout is the main checkout of the linked worktree whose top is d ("": d holds no .git file naming one).
func mainCheckout(d string) string {
	b, err := os.ReadFile(filepath.Join(d, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(d, gitdir)
	}
	common, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return ""
	}
	c := strings.TrimSpace(string(common))
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitdir, c)
	}
	return filepath.Dir(filepath.Clean(c))
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
