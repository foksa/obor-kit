//go:build conformance

// The shared static row for every kind's transport (deep dive §6; the wave's shared row): the calls a CLI's tool set
// can make that must end in the guard's deny — an unknown tool, a renamed argument (the command or file key missing)
// and an MCP call naming no server. Static: the real guard (internal/guard) in a temp project, a worker in a lane, no
// CLI, no session. Each transport is driven as its CLI hands a call over: Claude's as its hooks do, OpenCode's through
// its plugin's mapping, agy's through its hook transport; Codex runs no obor hook. A transport that cannot carry a call
// says which and why in its row; one that carries none SKIPs with the reason.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/guard"
	"obor/internal/project"
	"obor/kinds/agy/kind"
)

// guardCall is one call every kind's transport must hand the guard, and a substring of the guard's deny line.
type guardCall struct {
	key, name, want string
}

var guardCalls = []guardCall{
	{"unknown", "an unknown tool", "no rule for"},
	{"command", "a renamed argument (the command key missing)", "is empty"},
	{"file", "a renamed argument (the file key missing)", "is empty"},
	{"server", "an MCP call naming no server", "naming no server"},
}

// guardTransport is one kind's transport for the shared row: what it carries to the guard, why it carries nothing
// else, a precondition proving that reason, and how to drive one call through it.
type guardTransport struct {
	id, name, where string
	carries         []string // guardCalls keys the transport carries, in order
	gap             string   // why it carries none of the others ("" when it carries every call)
	skip            string   // why it cannot be driven here at all ("" when it can)
	check           func() error
	drive           func(key string) (hookAnswer, error)
}

// row drives the calls the transport carries and prints one line; a transport that carries none is a SKIP naming its
// reason, so the contract it has not got is never read as a pass. A transport whose reason stops holding (a caps
// change, a new event) FAILs: the row must then carry the call it did not.
func (t guardTransport) row() {
	if t.skip == "" && t.check != nil {
		if err := t.check(); err != nil {
			report("FAIL", t.id, t.name+": "+err.Error(), t.where)
			return
		}
	}
	if t.skip != "" {
		report("SKIP", t.id, t.name+": "+t.skip, t.where)
		return
	}
	if len(t.carries) == 0 {
		report("SKIP", t.id, t.name+": "+t.gap, t.where)
		return
	}
	var bad []string
	var done []string
	for _, key := range t.carries {
		c := guardCallOf(key)
		done = append(done, c.name)
		a, err := t.drive(key)
		switch {
		case err != nil:
			bad = append(bad, c.name+": "+err.Error())
		case !a.denied:
			bad = append(bad, c.name+": allowed; want the guard's deny")
		case !strings.Contains(a.reason, c.want):
			bad = append(bad, c.name+": "+described(a)+"; want a deny naming "+strconv.Quote(c.want))
		}
	}
	if len(bad) > 0 {
		report("FAIL", t.id, t.name+": "+strings.Join(bad, " / "), t.where)
		return
	}
	fact := t.name + ": the guard denies " + strings.Join(done, ", ")
	if t.gap != "" {
		fact += "; " + t.gap
	}
	report("PASS", t.id, fact, t.where)
}

// guardCallOf is guardCalls' key, so a row's carries list is checked against one table.
func guardCallOf(key string) guardCall {
	for _, c := range guardCalls {
		if c.key == key {
			return c
		}
	}
	panic("conformance: no guard call " + key)
}

// guardTransports is the shared row, one line per transport.
func guardTransports() {
	dir, err := os.MkdirTemp("", "obor-conf-guard")
	if err != nil {
		report("FAIL", "GT-S1", "a temp dir: "+err.Error(), "tests/conformance/guard_transport.go")
		return
	}
	defer os.RemoveAll(dir)
	root, err := tempProject(dir)
	if err != nil {
		report("FAIL", "GT-S1", "the project: "+err.Error(), "tests/conformance/guard_transport.go")
		return
	}
	// A worker in lane L-01 (the env a lane's start hands its agent): every call below is one a lane must not make.
	env := project.Env(func(k string) (string, bool) {
		switch k {
		case "OBOR_ROLE":
			return "worker", true
		case "OBOR_LANE":
			return "L-01", true
		}
		return "", false
	})
	guardRun := func(event []byte) (code int, reason, stderr string) {
		var errw bytes.Buffer
		code, reason = guard.Run(event, env, &errw)
		return code, reason, errw.String()
	}
	deny := func(event map[string]any) (hookAnswer, error) {
		b, err := json.Marshal(event)
		if err != nil {
			return hookAnswer{}, err
		}
		code, reason, stderr := guardRun(b)
		switch code {
		case 0:
			return hookAnswer{}, nil
		case 2:
			return hookAnswer{denied: true, reason: reason}, nil
		}
		return hookAnswer{}, fmt.Errorf("the guard exits %d: %s", code, stderr)
	}
	for _, t := range []guardTransport{
		claudeTransport(root, deny),
		codexTransport(),
		opencodeTransport(root, deny),
		agyTransport(root, guardRun),
	} {
		t.row()
	}
}

// claudeTransport is Claude Code's hooks: hooks/hooks.json runs `obor hook pre-tool-use` (the guard itself) for the
// tools its PreToolUse matcher names, so the event is the guard's as it is.
func claudeTransport(root string, deny func(map[string]any) (hookAnswer, error)) guardTransport {
	matcher := func() (*regexp.Regexp, error) {
		b, err := os.ReadFile("hooks/hooks.json")
		if err != nil {
			return nil, err
		}
		var doc struct {
			Hooks map[string][]struct {
				Matcher string `json:"matcher"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		groups := doc.Hooks["PreToolUse"]
		if len(groups) == 0 || groups[0].Matcher == "" {
			return nil, fmt.Errorf("hooks/hooks.json has no PreToolUse matcher: nothing names the tools its hook runs for")
		}
		return regexp.Compile(groups[0].Matcher)
	}
	return guardTransport{
		id: "GT-S1", name: "Claude Code's hooks", where: "hooks/hooks.json, " + at("internal/guard/guard.go", "func (g *guard) edit("),
		carries: []string{"command", "file"},
		gap:     "an unknown tool and an MCP call never reach it (its PreToolUse matcher names Bash, Edit, Write and NotebookEdit; Claude's MCP tools are mcp__<server>__<tool>, held by the start's MCP config)",
		check: func() error {
			m, err := matcher()
			if err != nil {
				return err
			}
			for _, tool := range []string{"mystery_tool", "mcp__conf__tool"} {
				if m.MatchString(tool) {
					return fmt.Errorf("its PreToolUse matcher sends %s now: drive that call here", tool)
				}
			}
			return nil
		},
		drive: func(key string) (hookAnswer, error) {
			switch key {
			case "command":
				return deny(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": root,
					"tool_input": map[string]any{}})
			case "file":
				return deny(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "cwd": root,
					"tool_input": map[string]any{}})
			}
			return hookAnswer{}, fmt.Errorf("no %s call in Claude Code's hooks", key)
		},
	}
}

// codexTransport is Codex: it runs no obor hook, so none of the calls reaches the guard; the row SKIPs, and fails if
// the kind ever grows a hook transport this row must drive.
func codexTransport() guardTransport {
	where := at(codexGo, "func (kind) Caps(") + ", " + at("internal/agents/agents.go", "HooksNone")
	return guardTransport{
		id: "GT-S2", name: "Codex's transport", where: where,
		gap: "Codex runs no obor hook (caps.hooks is none): a Codex lane's rules are the start's config, its sandbox and the prompt, so none of these calls reaches the guard",
		check: func() error {
			if c := kindOf("codex").Caps().Hooks; c != agents.HooksNone {
				return fmt.Errorf("the Codex kind now runs obor hooks (caps.hooks %v): drive its transport here", c)
			}
			return nil
		},
	}
}

// opencodeTransport is OpenCode's plugin: toolEvents maps a call to the Claude Code events obor checks, and the guard
// decides them (opencode/obor.js, by node). An MCP tool (a namespaced id) has no event: the plugin excepts it, and
// the start's config holds the servers.
func opencodeTransport(root string, deny func(map[string]any) (hookAnswer, error)) guardTransport {
	node, nodeErr := exec.LookPath("node")
	events := func(tool string, args map[string]any, namespaced bool) ([]map[string]any, error) {
		b, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		o, err := runCmd(time.Minute, "", nil, "", node, "tests/conformance/opencode_events.mjs", tool, string(b),
			strconv.FormatBool(namespaced))
		if err != nil {
			return nil, err
		}
		if o.code != 0 {
			return nil, fmt.Errorf("opencode_events.mjs: exit %d: %s", o.code, firstLine(o.stderr))
		}
		var evs []map[string]any
		if err := json.Unmarshal([]byte(o.stdout), &evs); err != nil {
			return nil, fmt.Errorf("opencode_events.mjs answered %q: %v", firstLine(o.stdout), err)
		}
		return evs, nil
	}
	t := guardTransport{
		id: "GT-S3", name: "OpenCode's plugin", where: at(opencodeJS, "export async function toolEvents") + ", " + at(opencodeJS, "const PASS"),
		carries: []string{"unknown", "command", "file"},
		gap:     "an MCP call (a namespaced tool id) has no event: the plugin excepts it, and the start's config holds the servers",
		check: func() error {
			evs, err := events("conf_mcp_tool", nil, true)
			if err != nil {
				return err
			}
			if len(evs) != 0 {
				return fmt.Errorf("a namespaced MCP tool now reaches the guard (%d events): drive the empty-server call here", len(evs))
			}
			return nil
		},
		drive: func(key string) (hookAnswer, error) {
			var evs []map[string]any
			var err error
			switch key {
			case "unknown":
				evs, err = events("mystery_tool", nil, false)
			case "command":
				// the command key renamed: the plugin reads args.command, and a workdir beside the missing one changes
				// nothing (a cd before no command would hide the guard's empty-command deny).
				plain, perr := events("shell", map[string]any{"cmd": "ls"}, false)
				if perr != nil {
					return hookAnswer{}, perr
				}
				if len(plain) != 1 {
					return hookAnswer{}, fmt.Errorf("the call maps to %d events, want 1", len(plain))
				}
				a, perr := deny(plain[0])
				if perr != nil || !a.denied {
					return a, perr
				}
				evs, err = events("shell", map[string]any{"cmd": "ls", "workdir": root}, false)
			case "file":
				evs, err = events("edit", map[string]any{"file": "x"}, false) // the file key renamed: the plugin reads filePath/path
			}
			if err != nil {
				return hookAnswer{}, err
			}
			if len(evs) != 1 {
				return hookAnswer{}, fmt.Errorf("the call maps to %d events, want 1", len(evs))
			}
			return deny(evs[0])
		},
	}
	if nodeErr != nil {
		t.skip = "node is not installed, so the plugin cannot be driven"
	}
	return t
}

// agyTransport is agy's hook transport (kinds/agy/kind.Hook) with the guard as its `obor hook pre-tool-use` (the
// transport runs the same guard through Exec as `obor kind agy hook`).
func agyTransport(root string, guardRun func([]byte) (int, string, string)) guardTransport {
	run := func(hook string, event []byte) ([]byte, []byte, int, error) {
		if hook != "pre-tool-use" {
			return nil, nil, 0, nil
		}
		code, reason, stderr := guardRun(event)
		switch code {
		case 0:
			return nil, nil, 0, nil
		case 2:
			return nil, []byte("obor: " + reason + "\n"), 2, nil
		}
		return nil, []byte(stderr), 1, nil
	}
	return guardTransport{
		id: "GT-S4", name: "agy's hook transport", where: at(agyHookGo, "var tools") + ", " + at(agyHookGo, "func toClaude"),
		carries: []string{"unknown", "command", "file", "server"},
		drive: func(key string) (hookAnswer, error) {
			tool, args := "", map[string]any{}
			switch key {
			case "unknown":
				tool = "mystery_tool"
			case "command":
				tool = "run_command"
			case "file":
				tool = "write_file"
			case "server":
				tool = "call_mcp_tool"
			}
			event, err := json.Marshal(map[string]any{"hookEventName": "PreToolUse", "conversationId": "conf", "cwd": root,
				"toolCall": map[string]any{"name": tool, "args": args}})
			if err != nil {
				return hookAnswer{}, err
			}
			out := kind.Hook(event, root, run, func(string) bool { return true }, io.Discard)
			var a struct{ Decision, Reason string }
			if err := json.Unmarshal(out, &a); err != nil {
				return hookAnswer{}, fmt.Errorf("answer %q: %v", out, err)
			}
			switch a.Decision {
			case "deny":
				return hookAnswer{denied: true, reason: a.Reason}, nil
			case "ask":
				return hookAnswer{}, nil
			}
			return hookAnswer{}, fmt.Errorf("answer %q: no decision", out)
		},
	}
}
