// Package kind is the Antigravity CLI (agy) as obor's kind protocol answers it (docs/agents/kind-protocol.md), built as
// the binary obor-kind-agy (kinds/agy). A start is `agy --settings <file> [args] --model … [--effort …] -i <prompt>`
// (a resume: --conversation <id>), the model and effort checked against agy's own `agy models` first (a resume's
// too: its argv carries the model; the `slug<TAB>label` lines kept as the last good list for a check that gives none
// or fails), the settings file the user's saved settings with the project's override file and obor's entries laid
// over them (settings.go); obor's MCP server and hooks through the project's committed .agents/mcp_config.json and
// .agents/hooks.json (install's where: project; the hooks' transport is hook.go); /exit with one Enter; `agy acp`,
// -p/--print and --prompt denied; prompts typed, lines sent with agentapi to a session an obor command recorded
// (deliver.go); the context read from the conversation's transcript.jsonl. Handle is one request; the binary runs it on
// its stdin, a test binary in process (testkit).
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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
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
// worktree of it), relative to its root: agy's workspace MCP config, loaded with no prompt in a trusted folder
// (agents.ProjectMCPFile).
const MCPFile = agents.ProjectMCPFile

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
	Check     bool              `json:"check"` // the start checks its wiring, not a launch: no CLI runs
	What      string            `json:"what"`
	Tail      string            `json:"tail"`
	Error     string            `json:"error"`
	Details   string            `json:"details"`
	At        string            `json:"at"`
	Text      string            `json:"text"`   // deliver: the text to send ("": the record's check)
	Record    string            `json:"record"` // deliver: the session's inbox record (InboxRecord's variables)
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
// call this kind does not answer. A start that cannot be built is a reply with error, not an error. A start's model
// check runs `agy models` live and keeps its answer as the cached list.
func Handle(call string, req []byte) ([]byte, error) { return handle(call, req, agyModels, true) }

// HandleModels is Handle with models as agy's model list in place of `agy models` (the start's check reads it, and
// the cache it falls back to): an in-process kind with a fixed list runs no agy (internal/testkit's). A supplied list
// is no live answer, so it is never stored as the cached one.
func HandleModels(models func() ([]byte, error)) func(call string, req []byte) ([]byte, error) {
	return func(call string, req []byte) ([]byte, error) { return handle(call, req, models, false) }
}

// handle is Handle with models as the start's model list, kept as the cached list when store is set.
func handle(call string, req []byte, models func() ([]byte, error), store bool) ([]byte, error) {
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
		reply = start(in, os.ReadFile, models, store)
	case "install":
		reply = map[string]any{"mcp": map[string]any{"where": "project", "path": MCPFile,
			"key": []string{"mcpServers", "obor"}, "entry": MCPEntry},
			"hooks": map[string]any{"where": "project", "path": HooksFile, "key": []string{"obor"}, "entry": HooksEntry()}}
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

// NotReadyPattern matches agy's notice that it runs another model than the start asked for: an unknown model, an
// effort the model lacks, or a slug that names an effort beside --effort (agy 1.5.14 starts anyway, on its default
// model; docs/research/agy-probes.md P5). start refuses those refs against `agy models` before agy runs; the notice is
// what a start shows for a ref the list still let through. Words may wrap across screen lines.
const NotReadyPattern = `Using\s+"[^"]*"\s+instead|is\s+not\s+recognized\s+as\s+a\s+known\s+model|has\s+no\s+"[^"]*"\s+effort|` +
	`conflicts\s+with\s+--effort`

// describe is the manifest. Caps: obor's hooks through obor's transport (hooks plugin: the project's HooksFile runs
// hook.go, which acts on the hooks' answers: hook_answers), a resume by the conversation id the SessionStart hook
// records (no caller-chosen id: named_session false), no session read from a transcript (its first line names none),
// the model as an id. Status from the terminal's agent detection. Stop: /exit, whose
// popup's Enter runs it; Ctrl+U clears the input. Headless: -p/--print, its alias --prompt, `agy acp` (ValueOpts: agy
// --help). The transcript is the conversation's transcript.jsonl (turn what: context reads it). Inbox: InboxRecord, the
// agentapi variables of the commands the agent runs, which deliver sends with (agy's agentapi send-message); a session
// with no record has its prompts typed. MCP: the project's committed MCPFile, which obor setup agents writes and obor
// setup mcp checks.
func describe() map[string]any {
	return map[string]any{
		"name":    Name,
		"status":  "terminal",
		"caps":    map[string]any{"hooks": "plugin", "inbox": true, "hook_answers": true, "resume": true, "model_arg": true},
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
		"inbox_record": InboxRecord,
		"mcp_lane": map[string]any{"registered": true, "static": true,
			"how": MCPFile + " (the project's committed workspace config: obor mcp on the agent's PATH)"},
		"override_files": []string{OverrideFile},
	}
}

// start: the model and effort checked against agy's list (modelList and checkModel: not in a check, in.Check; a
// resume's too, whose argv carries --model as a first start's does), then --settings <the start's settings file>, the
// type's args but their --model and --effort, the model (with the ref's effort: --model <the slug without its effort
// word> --effort <effort>; without: the slug alone, or the args' own --effort), --add-dir per write dir, then
// --conversation <id> for a resume, else -i <prompt> for a first prompt.
// The settings file is SettingsFile's name in the state dir, its content Settings of the user's saved settings (read
// from the request's HOME), the project's override file and the start. An effort with no model
// is the start's error: agy's --effort selects a variant of a model named by its base. A model check with no list (a
// live check that gave none and no cached list) is a reply's warning: the ref goes to agy unchecked. A fallback to the
// cached list is checked, and warned about unless the live check timed out. In an obor project
// (root) whose start dir's HooksFile, the one agy runs (in.Dir's, root's when in.Dir is empty or the root), does not
// hold this kit's entry (hooksStale; not in a check), an agent's start is refused and a lead's goes on with a reply's
// warning: obor logs it as `obor: kind agy: start: WARN …`.
func start(in request, read func(string) ([]byte, error), models func() ([]byte, error), store bool) map[string]any {
	rest, model, argEffort := splitModel(in.Args)
	effort := in.Effort
	if effort == "" {
		effort = argEffort
	}
	if effort != "" && model == "" {
		return map[string]any{"error": fmt.Sprintf("effort %s needs a model id (models.<name>.id, a slug of agy models)", effort)}
	}
	var warns []string
	if !in.Check {
		if why := hooksStale(in, read); why != "" {
			if !lead(in) {
				return map[string]any{"error": why}
			}
			warns = append(warns, "WARN "+why)
		}
	}
	warn := ""
	if model != "" && !in.Check {
		list, from, w, err := modelList(in.Env["HOME"], models, store)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		warn = w
		if len(list) > 0 {
			if why := checkModel(list, model, effort, from); why != "" {
				return map[string]any{"error": why}
			}
		}
	}
	var over map[string]any
	if doc, ok := in.Overrides[OverrideFile]; ok {
		var err error
		if over, err = agents.ParseOverride(OverrideFile, doc); err != nil {
			return map[string]any{"error": OverrideFile + ": does not parse: " + err.Error()}
		}
	}
	name := SettingsFile(lead(in), in.Dir)
	settings := Settings(UserSettings(in.Env, read), over, lead(in), in.Dir, in.Root)
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
	reply := map[string]any{"args": args, "env": map[string]string{},
		"files": []map[string]string{{"name": name, "bytes": string(b) + "\n"}}}
	if warn != "" {
		warns = append(warns, warn)
	}
	if len(warns) > 0 {
		reply["warnings"] = warns
	}
	return reply
}

// lead: in is a lead's start (trim_user false: docs/agents/kind-protocol.md).
func lead(in request) bool { return !in.TrimUser }

// hooksStale is why the agy an obor project (root) starts in in.Dir runs without this kit's hooks: agy runs the hooks
// file of the workspace it starts in, a linked worktree its own copy (docs/research/agy-probes.md P1), so the file read
// is in.Dir's HooksFile (root's when in.Dir is empty or the root). It has no obor entry, or one that is not HooksEntry
// (another obor's: a PreToolUse matching only some tools lets the others miss obor), or it is missing: whether agy
// falls back to another checkout's file when the workspace has none is not verified (P1 probed a worktree with its own
// copy), so none counts as stale. The fix named is that workspace's: the main
// checkout's obor setup agents and its commit; a lane's (its branch task/<task>) a rebase onto the integration branch
// or the task redispatched; a role worktree's (detached) a move to the integration branch's head, then a start again;
// a worktree's whose root file is stale too, the root's fix first. "": the entry is current, or root holds no obor
// project's config (none named). start refuses an agent's start for it and warns a lead's.
func hooksStale(in request, read func(string) ([]byte, error)) string {
	root := in.Root
	if root == "" || !configured(root) {
		return ""
	}
	setup := "run obor setup agents in the project, then commit " + HooksFile
	ws := in.Dir
	if ws == "" || filepath.Clean(ws) == filepath.Clean(root) {
		if why := hooksWhy(filepath.Join(root, HooksFile), read); why != "" {
			return "agy's hooks: " + why + ": " + setup
		}
		return ""
	}
	path := filepath.Join(ws, HooksFile)
	why := hooksWhy(path, read)
	if why == "" {
		return ""
	}
	note := " (agy runs the hooks file of the worktree it starts in, not the main checkout's"
	if why == "no "+path {
		note += "; whether it falls back to another checkout's when the worktree has none is not verified " +
			"(docs/research/agy-probes.md P1), so none counts as stale"
	}
	fix := worktreeFix(ws, read)
	if hooksWhy(filepath.Join(root, HooksFile), read) != "" {
		fix = setup + ", then " + fix
	}
	return "agy's hooks: " + why + note + "): " + fix
}

// configured is whether root is an obor project's main checkout: it holds .obor/config.json. A start's root names the
// main checkout; this binary's package reads no repository (a plugin kind imports its own tree and the bridge only).
func configured(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".obor", "config.json"))
	return err == nil
}

// hooksWhy is why the hooks file at path does not hold this kit's obor entry ("": it does).
func hooksWhy(path string, read func(string) ([]byte, error)) string {
	b, err := read(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no " + path
	case err != nil:
		return err.Error()
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return path + ": does not parse: " + err.Error()
	}
	raw, ok := doc["obor"]
	if !ok {
		return "no obor entry in " + path
	}
	var got, want any
	w, _ := json.Marshal(HooksEntry())
	json.Unmarshal(w, &want)
	if json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got, want) {
		return path + "'s obor entry is not this kit's (an older obor's hooks: tools they do not match miss obor's guard)"
	}
	return ""
}

// worktreeFix is the fix of the linked worktree ws whose hooks file is stale, by its HEAD (read through its .git
// file): a lane's branch task/<task> is brought onto the integration branch (merge.target_branch) by a rebase, or the
// task redispatched (a dispatch prepares the lane at the integration head); a role worktree (detached: the reviewer's,
// the deep dive's, the integrator's) is moved to its head, which serve does itself only for the deep dive's, at a brief
// while it is idle or stopped (and the reviewer's at a review's start), never at a restart; any other HEAD, either.
func worktreeFix(ws string, read func(string) ([]byte, error)) string {
	const target = "merge.target_branch"
	lane := "bring the lane's branch onto the integration branch's head: in the lane, git rebase --autostash <" + target +
		">, or redispatch its task: obor drop <task> \"<why>\" --discard, then obor dispatch <task>"
	role := "move the role worktree to the integration branch's head: git -C " + ws + " checkout --detach <" + target +
		"> (serve moves only the deep dive's itself, at a brief while it is idle or stopped: obor brief <agent> --fresh " +
		"\"<text>\"; obor restart moves none), then start the agent again: obor restart <agent> --fresh"
	head := worktreeHead(ws, read)
	switch {
	case strings.HasPrefix(head, "ref: refs/heads/task/"):
		task := strings.TrimPrefix(head, "ref: refs/heads/task/")
		return "bring the lane's branch task/" + task + " onto the integration branch's head: in " + ws +
			", git rebase --autostash <" + target + ">, or redispatch the task: obor drop " + task +
			" \"<why>\" --discard, then obor dispatch " + task
	case head != "" && !strings.HasPrefix(head, "ref:"):
		return role
	}
	return "this worktree is behind the integration branch: a lane's, " + lane + "; a role's, " + role
}

// worktreeHead is the HEAD of the linked worktree ws, trimmed ("": ws holds no .git file naming a readable one).
func worktreeHead(ws string, read func(string) ([]byte, error)) string {
	b, err := read(filepath.Join(ws, ".git"))
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(ws, gitdir)
	}
	head, err := read(filepath.Join(gitdir, "HEAD"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(head))
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
// under the start call's own 5 s (docs/agents/kind-protocol.md), and a check that gets no answer takes the last good
// list or, with none, goes on unchecked.
const modelsLimit = 4 * time.Second

// ErrModelsTimeout is `agy models` with no reply within modelsLimit: a start takes its cached list instead or, with
// none cached, goes on unchecked with a warning. Any other failure of the check is a start's error.
var ErrModelsTimeout = errors.New("no reply within " + modelsLimit.String())

// errModelsEmpty is `agy models` answering no model: an empty answer, or none of its lines a `slug<TAB>label` one. It is
// no list: a start takes its cached list instead or, with none cached, goes on unchecked with a warning.
var errModelsEmpty = errors.New("no model listed")

// groupGrace is how long `agy models` and `agy agentapi send-message`, and the children they leave, have to exit after
// SIGTERM before SIGKILL.
const groupGrace = 200 * time.Millisecond

// ModelsFile is a start's cached `agy models` output, relative to HOME: agy's own model list, one file for every
// project and lane (~/.obor/agy/models).
const ModelsFile = ".obor/agy/models"

// modelList is the list a start's check reads: `agy models`'s live answer (kept as the cached list when store is set),
// else the last good list. Only its `slug<TAB>label` lines are models, so a live answer with none is no list and
// replaces no cache (as a timeout does). from names where the list came from (checkModel's refusal says so). warn names
// a live check that gave no list at all (a timeout, an empty or garbled answer), whose model goes unchecked, or a
// fallback to the cache for a cause that is not a timeout, which the start checks; err is the live check's failure with
// no cached list.
func modelList(home string, models func() ([]byte, error), store bool) (list []model, from, warn string, err error) {
	out, liveErr := models()
	if liveErr == nil {
		if list = parseModels(out); len(list) > 0 {
			if store {
				keepModels(home, out)
			}
			return list, "the live list", "", nil
		}
		liveErr = errModelsEmpty
	}
	cached, ok := cachedModels(home)
	if ok {
		from = "the cached list in " + modelsCache(home)
		if errors.Is(liveErr, ErrModelsTimeout) {
			return parseModels(cached), from, "", nil
		}
		return parseModels(cached), from, fmt.Sprintf("WARN agy models: %v: the start checks %s", liveErr, from), nil
	}
	if errors.Is(liveErr, ErrModelsTimeout) || errors.Is(liveErr, errModelsEmpty) {
		where := "no cached list: the start names no HOME"
		if p := modelsCache(home); p != "" {
			where = "no cached list in " + p
		}
		return nil, "", fmt.Sprintf("WARN agy models: %v and %s: the start's model is unchecked", liveErr, where), nil
	}
	return nil, "", "", fmt.Errorf("agy models: %w", liveErr)
}

// modelsCache is the file of home's cached model list ("" : no HOME, so no cache).
func modelsCache(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(home, filepath.FromSlash(ModelsFile))
}

// cachedModels is the cached model list under home and whether there is one (no HOME, no file, or a file that names
// no model: none).
func cachedModels(home string) ([]byte, bool) {
	p := modelsCache(home)
	if p == "" {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil || len(parseModels(b)) == 0 {
		return nil, false
	}
	return b, true
}

// keepModels writes out as home's cached model list: best effort (a cache obor cannot write is no start's failure),
// through a temp file and a rename, so a start reading it at the same time reads a whole list or none.
func keepModels(home string, out []byte) {
	p := modelsCache(home)
	if p == "" {
		return
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(dir, "models-*")
	if err != nil {
		return
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(out); err != nil {
		f.Close()
		return
	}
	if err := f.Close(); err != nil {
		return
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return
	}
	os.Rename(name, p)
}

// agyModels is `agy models` on the agent's PATH, in its own process group: its list, or an error naming the failure (a
// non-zero exit with its stderr's last line, ErrModelsTimeout, no `agy` on PATH). The group is killed with it: the CLI
// leaves a child waiting on the network, which would answer no later start either.
func agyModels() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelsLimit)
	defer cancel()
	cmd := exec.CommandContext(ctx, Name, "models")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return signalGroup(cmd, syscall.SIGTERM, groupGrace) }
	cmd.WaitDelay = groupGrace + 100*time.Millisecond // a group that held the pipes: gone well within the start's 5 s
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	signalGroup(cmd, syscall.SIGKILL, 0) // a timed-out group's children: the group goes whether or not cmd waited
	var ee *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, ErrModelsTimeout
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

// signalGroup signals cmd's process group (nothing: cmd did not start) and, after grace, its SIGKILL.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal, grace time.Duration) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := cmd.Process.Pid
	if err := syscall.Kill(-pgid, sig); err != nil {
		return err
	}
	if grace > 0 {
		time.AfterFunc(grace, func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	}
	return nil
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

// parseModels is `agy models`'s output as the models it lists: one slug, a tab and its label per line. Any other line
// (a notice before the list, a wrapped label, a blank line) is no model: only the shape agy's list has counts.
func parseModels(out []byte) []model {
	var list []model
	for _, line := range strings.Split(string(out), "\n") {
		slug, label, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(slug) == "" {
			continue
		}
		list = append(list, model{slug: strings.TrimSpace(slug), label: strings.TrimSpace(label)})
	}
	return list
}

// slugsMax bounds the slugs a start's refusal names: agy's own list (22 models, about 510 characters) fits, and a list
// that outgrows it is cut rather than making an unbounded error line.
const slugsMax = 600

// slugLine is list's slugs on one line, cut after slugsMax bytes at a slug's end with "…".
func slugLine(list []model) string {
	slugs := make([]string, len(list))
	for i, m := range list {
		slugs[i] = m.slug
	}
	line := strings.Join(slugs, ", ")
	if len(line) <= slugsMax {
		return line
	}
	line = line[:slugsMax]
	if i := strings.LastIndex(line, ", "); i > 0 {
		line = line[:i]
	}
	return line + ", …"
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
// an unknown one or one without the effort, so the start refuses it before agy does. "" : the ref is in list. A ref
// with no effort of its own is a listed slug or label exactly (agy resolves labels), or the bare base of a model the
// list shows with variants (agy's default variant); a ref that names a variant the list has not ("…-xhigh" beside
// low/medium/high) is no model agy runs. from names where list came from: the refusal of a model it does not hold
// names it and the slugs it held (one line), so the failure shows what the check saw.
func checkModel(list []model, ref, effort, from string) string {
	base := Base(ref)
	has := variants(list, base)
	if effort == "" {
		if slices.ContainsFunc(list, func(m model) bool { return m.slug == ref || m.label == ref }) ||
			base == ref && len(has) > 0 {
			return ""
		}
		return notListed(ref, from, list)
	}
	if slices.Contains(has, effort) {
		return ""
	}
	if len(has) == 0 {
		if slices.ContainsFunc(list, func(m model) bool { return m.slug == base || m.label == base }) {
			return fmt.Sprintf("%s takes no %q effort (agy models names it with no effort)", base, effort)
		}
		return notListed(base, from, list)
	}
	return fmt.Sprintf("%s has no %q effort (agy models: %s)", base, effort, strings.Join(has, ", "))
}

// notListed is the refusal of name, which list (from) does not hold: its slugs follow, so the next failure shows what
// the check read.
func notListed(name, from string, list []model) string {
	return fmt.Sprintf("model %s is not in agy models (%s: %s; models.<name>.id names a slug the list shows)",
		name, from, slugLine(list))
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

// limitRE is an error text that names a usage, quota or rate limit: agy's own texts (its binary's strings: "You have
// exhausted your quota on this model.", "individual quota reached", "model … is out of quota until …", "Your AI credits
// balance is too low to continue.", "Usage limit reached", and its swarm's QUOTA_RE: RESOURCE_EXHAUSTED, quota, 429,
// "exhausted your capacity", rate limit). Another limit ("context window limit exceeded", a token or output limit) is
// not one: a bare "limit" does not match.
var limitRE = regexp.MustCompile(`(?i)quota|resource.?exhausted|\b429\b|exhausted your (quota|capacity)|` +
	`credits balance is too low|usage.?limit|rate.?limit`)

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
