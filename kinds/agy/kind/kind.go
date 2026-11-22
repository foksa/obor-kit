// Package kind is the Antigravity CLI (agy) as obor's kind protocol answers it (docs/agents/kind-protocol.md), built as
// the binary obor-kind-agy (kinds/agy). A start is `agy --settings <file> [args] --model … [--effort …] -i <prompt>`
// (a resume: --conversation <id>), the model and effort checked against agy's own `agy models` first, the settings file
// the user's saved settings with the project's override file and obor's entries laid over them (settings.go); obor's
// MCP server through the project's committed .agents/mcp_config.json (install's where: project); /exit with one
// Enter; `agy acp`, -p/--print and --prompt denied; prompts typed; the context read from the conversation's
// transcript.jsonl. Handle is one request; the binary runs it on its stdin, a test binary in process (testkit).
package kind

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
)

// Version is the protocol version this kind speaks.
const Version = 1

// Name is the kind's name (models.<name>.kind, kinds.<name>) and its CLI's executable.
const Name = "agy"

// efforts are agy's --effort values (agy --help: low|medium|high|xhigh|max). Which of them a model has is agy's (its
// `agy models` lists a slug per model and effort); start refuses one the model lacks.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// MCPFile is the project file that registers obor's MCP server for every agy session started in the project (or a
// worktree of it), relative to its root: agy's workspace MCP config, loaded with no prompt in a trusted folder.
const MCPFile = ".agents/mcp_config.json"

// MCPEntry is obor's entry in MCPFile, under mcpServers.obor: the `obor` on the agent's PATH (the server inherits the
// agent's environment, its role, lane and pane with it).
var MCPEntry = map[string]any{"command": "obor", "args": []any{"mcp"}}

// request is one call's request: the fields of every call (docs/agents/kind-protocol.md "The calls").
type request struct {
	Version   int               `json:"version"`
	Env       map[string]string `json:"env"`
	Kit       string            `json:"kit"`
	Args      []string          `json:"args"`
	Resume    string            `json:"resume"`
	Prompt    string            `json:"prompt"`
	Effort    string            `json:"effort"`
	TrimUser  bool              `json:"trim_user"`
	Overrides map[string]string `json:"overrides"` // the project's override files, name → document (OverrideFile)
	WriteDirs []string          `json:"write_dirs"`
	Dir       string            `json:"dir"`
	Root      string            `json:"root"`
	Check     bool              `json:"check"` // the start is the effort check, not a launch: no CLI runs
	What      string            `json:"what"`
	Tail      string            `json:"tail"`
	Error     string            `json:"error"`
	Details   string            `json:"details"`
	At        string            `json:"at"`
}

// Main is the binary: call args[0] with the request on stdin, its reply on stdout; a request it cannot read, or an
// unknown call, exits 2 with the reason on stderr.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: obor-kind-agy <call> < request.json (docs/agents/kind-protocol.md)")
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
	fmt.Fprintf(stderr, "obor-kind-agy: %s: %v\n", args[0], err)
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
		reply = start(in, os.ReadFile, agyModels)
	case "install":
		reply = map[string]any{"mcp": map[string]any{"where": "project", "path": MCPFile,
			"key": []string{"mcpServers", "obor"}, "entry": MCPEntry}}
	case "turn":
		reply = turn(in)
	case "deliver":
		reply = map[string]any{"ok": true, "delivery": map[string]any{"typed": true}}
	default:
		return nil, fmt.Errorf("no call %q", call)
	}
	reply["version"] = Version
	return json.Marshal(reply)
}

// NotReadyPattern matches agy's notice that it runs another model than the start asked for: an unknown model, an
// effort the model lacks, or a slug that names an effort beside --effort (agy 1.5.14 starts anyway, on its default
// model; docs/research/agy-probes.md P5). start refuses those refs against `agy models` before agy runs; the notice is
// what a start shows for a ref the list still let through. Words may wrap across screen lines.
const NotReadyPattern = `Using\s+"[^"]*"\s+instead|is\s+not\s+recognized\s+as\s+a\s+known\s+model|has\s+no\s+"[^"]*"\s+effort|` +
	`conflicts\s+with\s+--effort`

// describe is the manifest. Caps: no hooks yet (auto mode's sandbox stands in for the guard), a resume by the
// conversation id herdr reports (no caller-chosen id: named_session false), no session read from a transcript
// (its first line names none), the model as an id. Status from the terminal's agent detection. Stop: /exit, whose
// popup's Enter runs it; Ctrl+U clears the input. Headless: -p/--print, its alias --prompt, `agy acp` (ValueOpts: agy
// --help). The transcript is the conversation's transcript.jsonl (turn what: context reads it). Prompts typed. MCP: the
// project's committed MCPFile, which obor setup agents writes and obor setup mcp checks.
func describe() map[string]any {
	return map[string]any{
		"name":    Name,
		"status":  "terminal",
		"caps":    map[string]any{"hooks": "none", "resume": true, "model_arg": true},
		"efforts": efforts,
		"env":     []string{"HOME"},
		"stop":    map[string]any{"quit": "/exit", "clear_input": []string{"ctrl+u"}},
		"headless": map[string]any{"subcommands": []string{"acp"}, "print_flag": true, "flags": []string{"--prompt"},
			"value_opts": []string{"--add-dir", "--agent", "--conversation", "--effort", "-i", "--input-format",
				"--json-schema", "--log-file", "--mode", "--model", "--output-format", "--print-timeout", "--project",
				"--prompt-interactive", "--settings"}},
		"transcript": "${HOME}/.gemini/antigravity-cli/brain/{sid}/.system_generated/logs/transcript.jsonl",
		"not_ready": map[string]any{"pattern": NotReadyPattern,
			"why": "runs another model than the start asked for (agy's notice on its start screen: check the model's id and effort against `agy models`)"},
		"typed": true,
		"mcp_lane": map[string]any{"registered": true, "static": true,
			"how": MCPFile + " (the project's committed workspace config: obor mcp on the agent's PATH)"},
		"override_files": []string{OverrideFile},
	}
}

// start: the model and effort checked against `agy models` (checkModel; not for the effort check, in.Check), then
// --settings <the start's settings file>, the type's args but their --model and --effort, the model (with the ref's
// effort: --model <the slug without its effort word> --effort <effort>; without: the slug alone, or the args' own
// --effort), --add-dir per write dir, then --conversation <id> for a resume, else -i <prompt> for a first prompt.
// The settings file is SettingsFile's name in the state dir, its content Settings of the user's saved settings (read
// from the request's HOME), the project's override file (a trimmed start only) and the start. An effort with no model
// is the start's error: agy's --effort selects a variant of a model named by its base.
func start(in request, read func(string) ([]byte, error), models func() ([]byte, error)) map[string]any {
	rest, model, argEffort := splitModel(in.Args)
	effort := in.Effort
	if effort == "" {
		effort = argEffort
	}
	if effort != "" && model == "" {
		return map[string]any{"error": fmt.Sprintf("effort %s needs a model id (models.<name>.id, a slug of agy models)", effort)}
	}
	if model != "" && !in.Check {
		out, err := models()
		if err != nil {
			return map[string]any{"error": "agy models: " + err.Error()}
		}
		if why := checkModel(parseModels(out), model, effort); why != "" {
			return map[string]any{"error": why}
		}
	}
	var over map[string]any
	if doc, ok := in.Overrides[OverrideFile]; ok && in.TrimUser {
		var err error
		if over, err = agents.ParseOverride(OverrideFile, doc); err != nil {
			return map[string]any{"error": OverrideFile + ": does not parse: " + err.Error()}
		}
	}
	name := SettingsFile(!in.TrimUser, in.Dir)
	settings := Settings(UserSettings(in.Env, read), over, !in.TrimUser, in.Dir, in.Root)
	b, _ := json.MarshalIndent(settings, "", "  ")
	args := append([]string{"--settings", "{file:" + name + "}"}, rest...)
	switch {
	case model != "" && effort != "":
		args = append(args, "--model", Base(model), "--effort", effort)
	case model != "":
		args = append(args, "--model", model)
	}
	for _, d := range in.WriteDirs {
		args = append(args, "--add-dir", d)
	}
	switch {
	case in.Resume != "":
		args = append(args, "--conversation", in.Resume)
	case in.Prompt != "":
		args = append(args, "-i", in.Prompt)
	}
	return map[string]any{"args": args, "env": map[string]string{},
		"files": []map[string]string{{"name": name, "bytes": string(b) + "\n"}}}
}

// Base is slug without its effort word ("gemini-3.8-flash-low" → "gemini-3.8-flash"); a slug that ends in none is its
// own base.
func Base(slug string) string {
	if i := strings.LastIndex(slug, "-"); i > 0 && slices.Contains(efforts, slug[i+1:]) {
		return slug[:i]
	}
	return slug
}

// model is one entry of `agy models`: its slug (the id a start's ref names) and the label agy shows for it.
type model struct{ slug, label string }

// modelsLimit bounds `agy models`: about 1.5 s with the network, over 10 s when it cannot reach it, so it is capped
// under the start call's own 5 s (docs/agents/kind-protocol.md) and a slow list is the start's error, not a start with
// no reply.
const modelsLimit = 4 * time.Second

// agyModels is `agy models` on the agent's PATH: its list, or an error naming the failure (a non-zero exit with its
// stderr's last line, its own timeout, no `agy` on PATH).
func agyModels() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, Name, "models")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("no reply within %s", modelsLimit)
	case errors.As(err, &ee):
		why := lastLine(stderr.String())
		if why == "" {
			why = "no stderr"
		}
		return nil, fmt.Errorf("exit %d: %s", ee.ExitCode(), why)
	case err != nil:
		return nil, err
	}
	return stdout.Bytes(), nil
}

// lastLine is text's last non-blank line, trimmed ("" none): agy prints `Fetching available models...` before the line
// that says what failed.
func lastLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// parseModels is `agy models`'s output as the models it lists: one slug, a tab and its label per line (a line without a
// tab is a slug alone).
func parseModels(out []byte) []model {
	var list []model
	for _, line := range strings.Split(string(out), "\n") {
		slug, label, _ := strings.Cut(strings.TrimSpace(line), "\t")
		if slug = strings.TrimSpace(slug); slug != "" {
			list = append(list, model{slug: slug, label: strings.TrimSpace(label)})
		}
	}
	return list
}

// variants are the efforts agy models lists for base: the slugs base-<effort>.
func variants(list []model, base string) []string {
	var out []string
	for _, m := range list {
		if e, ok := strings.CutPrefix(m.slug, base+"-"); ok && slices.Contains(efforts, e) {
			out = append(out, e)
		}
	}
	return out
}

// checkModel is why a start cannot name model (with effort: model's base and that effort): agy runs another model for
// an unknown one or one without the effort, so the start refuses it before agy does. "" : the ref is in list. A ref may
// be a slug, a label (agy resolves labels) or a base with variants (an alias, agy resolves it to one of them).
func checkModel(list []model, ref, effort string) string {
	base := Base(ref)
	has := variants(list, base)
	if effort == "" {
		if len(has) > 0 || slices.ContainsFunc(list, func(m model) bool { return m.slug == ref || m.label == ref }) {
			return ""
		}
		return fmt.Sprintf("model %s is not in agy models (models.<name>.id names a slug the list shows)", ref)
	}
	if slices.Contains(has, effort) {
		return ""
	}
	if len(has) == 0 {
		if slices.ContainsFunc(list, func(m model) bool { return m.slug == base || m.label == base }) {
			return fmt.Sprintf("%s takes no %q effort (agy models names it with no effort)", base, effort)
		}
		return fmt.Sprintf("model %s is not in agy models (models.<name>.id names a slug the list shows)", base)
	}
	return fmt.Sprintf("%s has no %q effort (agy models: %s)", base, effort, strings.Join(has, ", "))
}

// SettingsFile is the name of a start's settings file in the state dir: a lead's (untrimmed) or another agent's, per
// start dir (the file trusts its start dir), so two starts never write one path with different bytes.
func SettingsFile(lead bool, dir string) string {
	sum := sha256.Sum256([]byte(dir))
	kind := "trimmed"
	if lead {
		kind = "lead"
	}
	return "agy-settings-" + kind + "-" + hex.EncodeToString(sum[:4]) + ".json"
}

// splitModel is args without their --model and --effort (with their word, or either spelling with =), and the values
// they named.
func splitModel(args []string) (rest []string, model, effort string) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--model" || a == "--effort") && i+1 < len(args):
			if a == "--model" {
				model = args[i+1]
			} else {
				effort = args[i+1]
			}
			i++
		case strings.HasPrefix(a, "--model="):
			model = strings.TrimPrefix(a, "--model=")
		case strings.HasPrefix(a, "--effort="):
			effort = strings.TrimPrefix(a, "--effort=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, model, effort
}

// limitRE is an error text that names a usage or rate limit (no agy limit has been seen yet: the words any such
// message would carry).
var limitRE = regexp.MustCompile(`(?i)limit|quota|resource.?exhausted`)

// turn: the context of a transcript's tail (Context); no turn and no session are read (status terminal, and the
// transcript's first line names no session). Reset: an error or message naming a limit is limited, its reset the
// time the message names (agents.ResetIn; none: unknown).
func turn(in request) map[string]any {
	switch in.What {
	case "context":
		c, ok := Context([]byte(in.Tail))
		if !ok {
			return map[string]any{"ok": false}
		}
		return map[string]any{"ok": true, "context": map[string]any{"tokens": c.Tokens,
			"at": c.At.UTC().Format(time.RFC3339), "ttl": ""}}
	case "reset":
		if !limitRE.MatchString(in.Error + " " + in.Details) {
			return map[string]any{"limited": false, "reset": ""}
		}
		at, _ := time.Parse(time.RFC3339, in.At)
		reset := ""
		if t := agents.ResetIn(in.Details, at); !t.IsZero() {
			reset = t.Format(time.RFC3339)
		}
		return map[string]any{"limited": true, "reset": reset}
	case "last", "session":
		return map[string]any{"ok": false}
	}
	return map[string]any{"error": fmt.Sprintf("turn: no what %q", in.What)}
}

// Context is the context size of a transcript.jsonl tail: its last PLANNER_RESPONSE line with a token count, whose
// input_tokens + cache_read_tokens is what the model was given, at its created_at. ok false: no such line, or its
// time does not parse.
func Context(tail []byte) (agents.Context, bool) {
	type line struct {
		Type   string `json:"type"`
		At     string `json:"created_at"`
		Input  *int   `json:"input_tokens"`
		Cached int    `json:"cache_read_tokens"`
	}
	var found *line
	sc := bufio.NewScanner(bytes.NewReader(tail))
	sc.Buffer(make([]byte, 0, 64<<10), len(tail)+1)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.Type == "PLANNER_RESPONSE" && l.Input != nil {
			found = &l
		}
	}
	if found == nil {
		return agents.Context{}, false
	}
	at, err := time.Parse(time.RFC3339, found.At)
	if err != nil {
		return agents.Context{}, false
	}
	return agents.Context{Tokens: *found.Input + found.Cached, At: at}, true
}
