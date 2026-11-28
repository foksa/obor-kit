// Package kind is pi as obor's kind protocol answers it (docs/agents/kind-protocol.md), built as the binary
// obor-kind-pi (kinds/pi). A start is `pi --approve -e {kit}/pi/obor.js --model <provider/id> [--thinking <level>]`
// (a resume adds --session <id>), then `--` and the prompt; the extension registers obor's MCP server at each start
// and carries the hooks and the session's inbox (pi/obor.js). /quit with one Enter; -p/--print, --mode and the
// subcommands that write the user's pi config denied; lines posted to the session's inbox; the turn, its context and a
// limit's reset read from the session JSONL (turn.go). Handle is one request; the binary runs it on its stdin, a test
// binary in process (testkit).
package kind

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Version is the protocol version this kind speaks.
const Version = 1

// Name is the kind's name (models.<name>.kind, kinds.<name>) and its CLI's executable.
const Name = "pi"

// ResumeVar is the variable the start puts the resumed session's id in (unset on a fresh start): pi's extension
// (pi/obor.js) reads it to tell a resumed start from a fresh one, since pi's session_start names a startup both ways
// (docs/research/pi-probes.md P9), and the session-start hook reports the id either way.
const ResumeVar = "OBOR_PI_RESUME"

// InboxVar is the variable obor's extension (pi/obor.js) puts in the env of the session-start hook it runs: the
// session's inbox socket, which it binds per session. The manifest's inbox_record names it, so the hook records it as
// the session's inbox, and deliver posts there.
const InboxVar = "OBOR_PI_INBOX"

// Extension is the extension a start loads with -e: {kit}/pi/obor.js, the hooks' and the inbox's transport.
func Extension(kit string) string { return filepath.Join(kit, "pi", "obor.js") }

// efforts are pi's --thinking levels (pi --help: off, minimal, low, medium, high, xhigh, max). Which of them a model
// supports is pi's to clamp.
var efforts = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// fixedArgs are the flags every start carries: --approve trusts the project for this run only (a lane's worktree is a
// new path every start, and herdr shows the trust dialog as idle, not blocked; docs/research/pi-probes.md P1, P8).
var fixedArgs = []string{"--approve"}

// headlessValueOpts are pi's options that take the next word (pi --help), so the guard's headless forms do not read a
// value as a subcommand.
var headlessValueOpts = []string{"--provider", "--model", "--api-key", "--system-prompt", "--append-system-prompt",
	"--mode", "--session", "--session-id", "--fork", "--session-dir", "--agent-dir", "--name", "-n", "--models",
	"--tools", "-t", "--exclude-tools", "-xt", "--thinking", "--extension", "-e", "--skill", "--prompt-template",
	"--theme", "--use-theme", "--export", "--tui-mode"}

// headlessShortOpts are pi's short options of more than one letter (pi --help): whole options, so the guard reads -np
// as --no-prompt-templates, not as the cluster -n -p.
var headlessShortOpts = []string{"-nt", "-nbt", "-xt", "-ne", "-ns", "-np", "-nc", "-na"}

// request is one call's request: the fields of every call (docs/agents/kind-protocol.md "The calls").
type request struct {
	Version int               `json:"version"`
	Env     map[string]string `json:"env"`
	Kit     string            `json:"kit"`
	Args    []string          `json:"args"`
	Resume  string            `json:"resume"`
	Prompt  string            `json:"prompt"`
	Effort  string            `json:"effort"`
	What    string            `json:"what"`
	Tail    string            `json:"tail"`
	First   string            `json:"first"`
	Error   string            `json:"error"`
	Details string            `json:"details"`
	At      string            `json:"at"`
	Text    string            `json:"text"`
	Record  string            `json:"record"`
}

// Main is the binary: call args[0] with the request on stdin, its reply on stdout; a request it cannot read, or an
// unknown call, exits 2 with the reason on stderr.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: obor-kind-pi <call> < request.json (docs/agents/kind-protocol.md)")
		return 2
	}
	req, err := io.ReadAll(stdin)
	if err == nil {
		var out []byte
		if out, err = Handle(args[0], req); err == nil {
			_, err = stdout.Write(append(out, '\n'))
			if err == nil {
				return 0
			}
		}
	}
	fmt.Fprintf(stderr, "obor-kind-pi: %s: %v\n", args[0], err)
	return 2
}

// Handle is call's reply to req, as JSON; an error for a request that does not parse or names another version, or a
// call this kind does not answer. A start that cannot be built is a reply with error, not an error.
func Handle(call string, req []byte) ([]byte, error) {
	var in request
	if err := json.Unmarshal(req, &in); err != nil {
		return nil, fmt.Errorf("malformed request: %v", err)
	}
	if in.Version != Version {
		return nil, fmt.Errorf("request version %d, this kind speaks %d", in.Version, Version)
	}
	var reply map[string]any
	switch call {
	case "describe":
		reply = describe()
	case "start":
		reply = start(in)
	case "install":
		reply = map[string]any{"mcp": map[string]any{"where": "start"}}
	case "turn":
		reply = turn(in)
	case "deliver":
		reply = deliver(in)
	default:
		return nil, fmt.Errorf("no call %q", call)
	}
	reply["version"] = Version
	return json.Marshal(reply)
}

// describe is the manifest. Caps: hooks through pi's extension, which acts on the hooks' answers (hook_answers: the
// prompt's context and a stop's block), a resume by the session id the session-start hook reports, the model as a
// required provider/id, and the session's inbox (the extension's per-session socket, recorded from InboxVar:
// inbox_record). Status from the terminal's agent detection (herdr detects a pi pane with no integration). Stop:
// /quit, typed with Enter (its command popup takes that Enter and runs it; docs/research/pi-probes.md P8), Ctrl+U
// clears the input; never Ctrl+C, whose second press exits. Headless: pi starts print mode whenever stdin or stdout is
// no TTY, so the guard denies every pi from a tool whatever its arguments unless its first argument is one of the forms
// that run no agent (always: --help, -h, --version, -v, --list-models, --export, list); -p/--print, --mode's json and
// rpc, the subcommands that write the user's config, and the multi-letter short options (-np, -ne, …) still deny after
// such an argument as the forms agents type.
// Transcript: the session JSONL under pi's default session dir
// ({HOME}/.pi/agent/sessions, one dir per cwd), named <timestamp>_<session id>.jsonl. MCP: the extension registers
// obor's server at each start (install's lane is per start); no file is written.
func describe() map[string]any {
	return map[string]any{
		"name":    Name,
		"status":  "terminal",
		"caps":    map[string]any{"hooks": "plugin", "inbox": true, "hook_answers": true, "resume": true, "model_arg": true},
		"efforts": efforts,
		"vars":    []string{ResumeVar},
		"env":     []string{"HOME"},
		"stop":    map[string]any{"quit": "/quit", "clear_input": []string{"ctrl+u"}},
		"headless": map[string]any{"print_flag": true, "value_opts": headlessValueOpts, "short_opts": headlessShortOpts,
			"flags":       []string{"--mode"},
			"subcommands": []string{"install", "remove", "uninstall", "update", "config", "auth", "mcp"},
			"always":      true,
			"except":      []string{"--help", "-h", "--version", "-v", "--list-models", "--export", "list"}},
		"transcript":   TranscriptGlob,
		"inbox_record": []string{InboxVar},
		"mcp_lane": map[string]any{"registered": true, "how": "-e " + Extension("{kit}") +
			" (the extension registers " + filepath.Join("{kit}", "bin", "obor") + " mcp)"},
	}
}

// deliver posts text to the session's inbox as the record names it (InboxVar, the socket the extension binds): one
// line, {"type":"user","text":<text>}, which the extension hands to pi as a custom message and wakes the session
// with (at once when idle, at its settle when busy). ok false: the record names no socket.
func deliver(in request) map[string]any {
	var rec map[string]string
	if json.Unmarshal([]byte(in.Record), &rec) != nil || rec[InboxVar] == "" {
		return map[string]any{"ok": false}
	}
	line, _ := json.Marshal(map[string]string{"type": "user", "text": in.Text})
	return map[string]any{"ok": true, "delivery": map[string]any{"socket": rec[InboxVar], "lines": []string{string(line)}}}
}

// TranscriptGlob is the manifest's transcript glob: pi's default session dir, one directory per cwd, and a session's
// file named <timestamp>_<session id>.jsonl.
const TranscriptGlob = "${HOME}/.pi/agent/sessions/*/*_{sid}.jsonl"

// start: --approve, -e {kit}/pi/obor.js, the type's args but their --model/-m and --thinking (or the start's effort
// replaces that one), then --model <provider/id>, --thinking <effort> when the start has one, --session <id> for a
// resume, and `--` <prompt> for a first prompt. The model id is the config's id verbatim and must name its provider
// (pi resolves `provider/id`; a bare pattern needs --provider). A resume's id reaches the extension as ResumeVar; a
// fresh start has that variable empty (unset). No file is written.
func start(in request) map[string]any {
	rest, model, argEffort := splitArgs(in.Args)
	effort := in.Effort
	if effort == "" {
		effort = argEffort
	}
	switch {
	case model == "" && effort != "":
		return map[string]any{"error": fmt.Sprintf("effort %s needs a model id (models.<name>.id, provider/id)", effort)}
	case effort != "" && !slices.Contains(efforts, effort):
		return map[string]any{"error": fmt.Sprintf("effort %q is not one of pi's %s", effort, strings.Join(efforts, ", "))}
	case model != "" && !strings.Contains(model, "/"):
		return map[string]any{"error": fmt.Sprintf("model %s names no provider (models.<name>.id, provider/id)", model)}
	}
	args := slices.Clone(fixedArgs)
	if in.Kit != "" {
		args = append(args, "-e", Extension(in.Kit))
	}
	for _, a := range rest {
		if !slices.Contains(fixedArgs, a) {
			args = append(args, a)
		}
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--thinking", effort)
	}
	if in.Resume != "" {
		args = append(args, "--session", in.Resume)
	}
	if in.Prompt != "" {
		args = append(args, "--", in.Prompt)
	}
	return map[string]any{"args": args, "env": map[string]string{ResumeVar: in.Resume}}
}

// splitArgs is args without their --model/-m and --thinking (with their word, or either spelling with =), and the
// values they named.
func splitArgs(args []string) (rest []string, model, effort string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--model" || a == "-m" || a == "--thinking") && i+1 < len(args):
			if a == "--thinking" {
				effort = args[i+1]
			} else {
				model = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		case strings.HasPrefix(a, "-m="):
			model = strings.TrimPrefix(a, "-m=")
		case strings.HasPrefix(a, "--thinking="):
			effort = strings.TrimPrefix(a, "--thinking=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, model, effort
}

// turn answers what: last, session, context or reset.
func turn(in request) map[string]any {
	switch in.What {
	case "last":
		t, ok := LastTurn([]byte(in.Tail))
		if !ok {
			return map[string]any{"ok": false}
		}
		reset := ""
		if !t.Reset.IsZero() {
			reset = t.Reset.Format(time.RFC3339)
		}
		return map[string]any{"ok": true, "turn": map[string]any{"id": t.ID, "error": t.Error, "details": t.Details,
			"ended": t.Ended, "reset": reset}}
	case "session":
		s, ok := Session([]byte(in.First))
		if !ok {
			return map[string]any{"ok": false}
		}
		started := ""
		if !s.Started.IsZero() {
			started = s.Started.Format(time.RFC3339)
		}
		return map[string]any{"ok": true, "session": map[string]any{"id": s.ID, "cwd": s.Cwd, "started": started}}
	case "context":
		c, ok := Context([]byte(in.Tail))
		if !ok {
			return map[string]any{"ok": false}
		}
		return map[string]any{"ok": true, "context": map[string]any{"tokens": c.Tokens,
			"at": c.At.UTC().Format(time.RFC3339), "ttl": ""}}
	case "reset":
		at, _ := time.Parse(time.RFC3339, in.At)
		reset, limited := Reset(in.Error, in.Details, at)
		out := map[string]any{"limited": limited, "reset": ""}
		if !reset.IsZero() {
			out["reset"] = reset.Format(time.RFC3339)
		}
		return out
	}
	return map[string]any{"error": fmt.Sprintf("turn: no what %q", in.What)}
}
