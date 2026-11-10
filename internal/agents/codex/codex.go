// Package codex is the Codex CLI bridge (agents.Kind): `resume <sid>` first, obor's MCP server as -c options on every
// start (~/.codex/config.toml is never edited), its startup update check off, /quit, `codex exec` denied, rollout
// transcripts, prompts typed.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"obor/internal/agents"
)

func init() { agents.Register(kind{}) }

type kind struct{}

func (kind) Name() string { return "codex" }

// EnvVarsTOML is the TOML array of the variables Codex forwards to the server (it starts MCP servers with a stripped
// environment).
func EnvVarsTOML() string {
	b, _ := json.Marshal(agents.EnvVars) // a JSON string array is a TOML one
	return string(b)
}

// Start: `resume <sid>` before the type's args (with in.Effort: their model_reasoning_effort replaced by
// -c model_reasoning_effort=<effort>), then --add-dir per write dir, then a trimmed start's switches (in.Override != nil:
// trimKeys false: no plugins, no app connectors, no bundled skills; skills.config with every dir of the user's
// skills dir off, skillsOff; the rest of the user's config applies), then obor's server (with a kit), then the
// update check off, then the prompt (a resume carries none). Codex takes no settings.
//
// The project's committed .codex/config.toml in in.Dir outranks obor's policy: a top-level sandbox_mode drops
// the base args' -s workspace-write, a top-level check_for_update_on_startup leaves the update check as the file sets
// it, and a trim key the file sets (in any form: dotted, in a [table], inside a table it assigns whole, or as
// [[skills.config]] tables) is left as the file sets it. A key the type's args set (-c) is left as they set it too.
// A file that does not parse counts as absent (config validate warns).
func (kind) Start(in agents.Start) []string {
	top, _ := projectKeys(in)
	args := in.Args
	if top[sandboxKey] {
		args = withoutSandbox(args)
	}
	var out []string
	if in.Resume != "" {
		out = append(out, "resume", in.Resume)
	}
	out = append(out, withEffort(args, in.Effort)...)
	for _, d := range in.WriteDirs { // workspace-write writes the cwd only
		out = append(out, "--add-dir", d)
	}
	if in.Override != nil {
		for _, k := range trimKeys {
			if !argSets(in.Args, k) && !sets(top, k) {
				out = append(out, "-c", k+"=false")
			}
		}
		if off := skillsOff(in); off != "" && !argSets(in.Args, skillsKey) && !sets(top, skillsKey) {
			out = append(out, "-c", skillsKey+"="+off)
		}
	}
	if in.Kit != "" {
		out = append(out,
			"-c", `mcp_servers.obor.command="`+filepath.Join(in.Kit, "bin", "obor")+`"`,
			"-c", `mcp_servers.obor.args=["mcp"]`,
			"-c", "mcp_servers.obor.env_vars="+EnvVarsTOML(),
			"-c", "mcp_servers.obor.env={"+agents.Pin+`="1"}`,
			// lanes run -a never: without this every obor tool call fails "requires approval"
			"-c", `mcp_servers.obor.default_tools_approval_mode="approve"`)
	}
	// Codex's own "Update available" menu blocks a fresh start until someone picks "Skip"; a type arg that
	// sets the key wins.
	if !argSets(in.Args, updateCheck) && !top[updateCheck] {
		out = append(out, "-c", updateCheck+"=false")
	}
	if in.Prompt != "" && in.Resume == "" {
		out = append(out, in.Prompt)
	}
	return out
}

// ConfigFile is the project's committed config, relative to the start dir.
const ConfigFile = ".codex/config.toml"

// sandboxKey is Codex's sandbox setting; sandbox is the -s value of obor's base args (config.KindBaseArgs).
const (
	sandboxKey = "sandbox_mode"
	sandbox    = "workspace-write"
)

// withoutSandbox is args without their first `-s workspace-write` (the base args' pair: they come first).
func withoutSandbox(args []string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-s" && args[i+1] == sandbox {
			return append(append([]string(nil), args[:i]...), args[i+2:]...)
		}
	}
	return args
}

// trimKeys are the switches of a trimmed start, each set false: the user's plugins (their skills too), the app
// connectors, the bundled system skills. The project's skills and obor's -c MCP server stay.
var trimKeys = []string{"features.plugins", "features.apps", "skills.bundled.enabled"}

// skillsKey is Codex's per-skill switch list: [{name = "<skill>", enabled = false}, …].
const skillsKey = "skills.config"

// skillsOff is the skillsKey value that switches off every skill of the user's skills dir ($CODEX_HOME, default
// ~/.codex: skills/), one entry per dir by its name; "": the dir has none (or the start names no user dir).
func skillsOff(in agents.Start) string {
	dir := agents.UserDir(in, "CODEX_HOME", ".codex")
	if dir == "" {
		return ""
	}
	var entries []string
	for _, n := range agents.UserSkills(in, dir) {
		name, _ := json.Marshal(n) // a JSON string is a TOML basic string
		entries = append(entries, "{name="+string(name)+",enabled=false}")
	}
	if len(entries) == 0 {
		return ""
	}
	return "[" + strings.Join(entries, ",") + "]"
}

// projectKeys are the keys the start dir's ConfigFile sets, as configKeys (nil: no file, or one that does not parse).
func projectKeys(in agents.Start) (map[string]bool, error) {
	b, found, err := agents.ProjectFile(in, ConfigFile)
	if !found || err != nil {
		return nil, err
	}
	return configKeys(string(b))
}

// sets: keys (configKeys) set key, a table above it as one value (`features = {…}` sets features.plugins), or a key
// below it (`[[skills.config]]` then `name = …` sets skills.config).
func sets(keys map[string]bool, key string) bool {
	for k := range keys {
		if strings.HasPrefix(k, key+".") {
			return true
		}
	}
	for p := key; ; {
		if keys[p] {
			return true
		}
		i := strings.LastIndex(p, ".")
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

// ProjectWarnings: a config file whose top-level part does not parse (obor's start flags then apply as without it).
func (kind) ProjectWarnings(dir string, read agents.ReadFile) []string {
	if _, err := projectKeys(agents.Start{Dir: dir, Read: read}); err != nil {
		return []string{ConfigFile + ": does not parse (" + err.Error() + "): obor's start flags apply as if it were absent"}
	}
	return nil
}

// installFiles are the binaries of a Codex package, side by side: Codex spawns the code-mode host for every tool
// call (0.167) and the exec server for commands.
var installFiles = []string{"codex", "codex-code-mode-host", "codex-exec-server"}

// userApproved is quarantine's user-approved bit (docs/research/codex-quarantine.md §3: "0181" → "01c1" once
// Gatekeeper's dialog was answered Open): such a file will not prompt again.
const userApproved = 0x0040

// quarantined: on darwin, the files beside the resolved codex that carry
// com.apple.quarantine without the user-approved bit, and the package's version dir to clear. One read of the
// machine's install for both the setup warning and the start check.
// A quarantined codex-code-mode-host hangs at exec (macOS never approves it for a child process), so every tool call
// of a 0.167 session fails "timed out negotiating with the code-mode host" while the TUI itself starts fine.
func quarantined(h agents.InstallHost) (files []string, version string) {
	if h.GOOS != "darwin" || h.LookPath == nil || h.Quarantine == nil {
		return nil, ""
	}
	bin, err := h.LookPath("codex")
	if err != nil {
		return nil, ""
	}
	if h.Resolve != nil {
		if r, err := h.Resolve(bin); err == nil {
			bin = r
		}
	}
	dir := filepath.Dir(bin)
	for _, f := range installFiles {
		if flags, ok := h.Quarantine(filepath.Join(dir, f)); ok && flags&userApproved == 0 {
			files = append(files, filepath.Join(dir, f))
		}
	}
	if len(files) == 0 {
		return nil, ""
	}
	version = dir // a package's bin/ sits in its version dir (Homebrew's Caskroom/codex/<version>)
	if filepath.Base(dir) == "bin" {
		version = filepath.Dir(dir)
	}
	return files, version
}

// InstallWarnings: the quarantined files, one line naming them and the fix.
func (kind) InstallWarnings(h agents.InstallHost) []string {
	q, version := quarantined(h)
	if len(q) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%s carry com.apple.quarantine: macOS may never start them, and every tool call then "+
		"times out negotiating with the code-mode host (T-1045); fix: xattr -dr com.apple.quarantine %s",
		strings.Join(q, ", "), version)}
}

// StartRefusal: a Codex start whose resolved install carries an unapproved quarantine flag is not made. The
// Gatekeeper dialog for codex-code-mode-host would sit unanswered in an unattended pane, so every tool call of the
// session would time out; the line names the files and the owner's fix (obor applies none).
func (kind) StartRefusal(h agents.InstallHost) string {
	q, version := quarantined(h)
	if len(q) == 0 {
		return ""
	}
	return fmt.Sprintf("codex is quarantined: %s carry com.apple.quarantine and macOS may never start them, so every tool "+
		"call of the session would time out negotiating with the code-mode host; a Codex start is refused until the owner "+
		"clears the flag: xattr -dr com.apple.quarantine %s (obor applies no fix)",
		strings.Join(q, ", "), version)
}

// keyPart is one part of a TOML key, bare or quoted; dottedKey is a key of parts joined by dots.
const (
	keyPart   = `[A-Za-z0-9_-]+|"[^"\\]*"|'[^']*'`
	dottedKey = `(?:` + keyPart + `)(?:\s*\.\s*(?:` + keyPart + `))*`
)

var (
	// tomlKey is a `key = value` line of TOML: the key, the value after the `=`.
	tomlKey = regexp.MustCompile(`^(` + dottedKey + `)\s*=\s*(.*)$`)
	// tomlTable is a `[table]` or `[[array.of.tables]]` header line, a comment allowed after it.
	tomlTable = regexp.MustCompile(`^\[\[?\s*(` + dottedKey + `)\s*\]\]?\s*(?:#.*)?$`)
	keyParts  = regexp.MustCompile(keyPart)
)

// configKeys are the keys a TOML document sets, each as its full dotted path, quotes off: `a = 1` sets "a",
// `a.b = 1` and `[a]` then `b = 1` both set "a.b" (a table's own header sets nothing). A narrow scan, no TOML parser:
// it reads `key = value` lines and table headers, skips blank and comment lines, and follows a value over lines while
// a bracket or a triple-quoted string it opens is open. Any other line before the first table header is an error
// naming its number; under a table it ends the scan with the keys read so far.
func configKeys(doc string) (map[string]bool, error) {
	keys := map[string]bool{}
	var open value
	table := ""
	for i, line := range strings.Split(doc, "\n") {
		if open.pending() {
			open.scan(line)
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if m := tomlTable.FindStringSubmatch(t); m != nil {
			table = keyPath(m[1]) + "."
			continue
		}
		m := tomlKey.FindStringSubmatch(t)
		if m == nil || strings.TrimSpace(m[2]) == "" {
			if table != "" || strings.HasPrefix(t, "[") {
				return keys, nil
			}
			return nil, fmt.Errorf("line %d is no key = value", i+1)
		}
		keys[table+keyPath(m[1])] = true
		open.scan(m[2])
	}
	if open.pending() && table == "" {
		return nil, errors.New("a value is not closed")
	}
	return keys, nil
}

// keyPath is a dotted key's parts, quotes off, joined by dots.
func keyPath(key string) string {
	parts := keyParts.FindAllString(key, -1)
	for i, p := range parts {
		parts[i] = strings.Trim(p, `"'`)
	}
	return strings.Join(parts, ".")
}

// value follows one TOML value over lines: the depth of its open brackets and braces, and the triple quote it is in.
type value struct {
	depth  int
	triple string // `"""` or `'''` while a multi-line string is open
}

func (v *value) pending() bool { return v.depth > 0 || v.triple != "" }

// scan reads one line of the value: its strings, brackets and a trailing comment.
func (v *value) scan(s string) {
	for i := 0; i < len(s); i++ {
		if v.triple != "" {
			if strings.HasPrefix(s[i:], v.triple) {
				i += 2
				v.triple = ""
			}
			continue
		}
		switch c := s[i]; c {
		case '#':
			return
		case '[', '{':
			v.depth++
		case ']', '}':
			v.depth--
		case '"', '\'':
			if q := strings.Repeat(string(c), 3); strings.HasPrefix(s[i:], q) {
				v.triple = q
				i += 2
				continue
			}
			for i++; i < len(s) && s[i] != c; i++ {
				if c == '"' && s[i] == '\\' {
					i++
				}
			}
		}
	}
}

// effortKey is Codex's reasoning effort setting.
const effortKey = "model_reasoning_effort"

// efforts are Codex's model_reasoning_effort values: `codex --help` lists none, so they are the efforts of Codex 0.166's
// built-in models (low, medium, high, xhigh, max, ultra; luna tops out at max, per-model limits are not checked;
// no minimal).
var efforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

func (kind) Efforts() []string { return efforts }

// StartEnv: none; the effort is a -c option.
func (kind) StartEnv(agents.Start) (map[string]string, error) { return nil, nil }

// Vars: none (StartEnv sets none).
func (kind) Vars() []string { return nil }

// WithModel: -m <model> in place of the args' own (-m or --model).
func (kind) WithModel(args []string, model string) []string {
	return agents.SetFlag(args, []string{"-m", "--model"}, model)
}

// withEffort is args without their effortKey option (`-c`/`--config` with its word, or either spelling with =), then
// -c model_reasoning_effort=<effort>; args as they are for effort "".
func withEffort(args []string, effort string) []string {
	if effort == "" {
		return append([]string(nil), args...)
	}
	isEffort := func(word string) bool {
		key, _, ok := strings.Cut(word, "=")
		return ok && strings.TrimSpace(key) == effortKey
	}
	out := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "-c" || a == "--config") && i+1 < len(args) && isEffort(args[i+1]):
			i++
		case strings.HasPrefix(a, "-c=") && isEffort(strings.TrimPrefix(a, "-c=")),
			strings.HasPrefix(a, "--config=") && isEffort(strings.TrimPrefix(a, "--config=")):
		default:
			out = append(out, a)
		}
	}
	return append(out, "-c", effortKey+"="+effort)
}

// updateCheck is Codex's setting for the update check it runs on startup.
const updateCheck = "check_for_update_on_startup"

// argSets: the type's args set key themselves (`-c key=value`, `--config key=value` or their `key=value` word,
// either spelling of the flag with =).
func argSets(args []string, key string) bool {
	for _, a := range args {
		a = strings.TrimPrefix(a, "-c=")
		a = strings.TrimPrefix(a, "--config=")
		if k, _, ok := strings.Cut(a, "="); ok && strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

var updateRE = regexp.MustCompile(`(?i)updat(ed|ing) (codex|to )|restart codex|new version (of codex )?(is )?(available|installed)`)

// NotReady: Codex updated itself and exited instead of starting.
func (kind) NotReady(screen string) string {
	if updateRE.MatchString(screen) {
		return "updated itself and exited"
	}
	return ""
}

func (kind) Stop() agents.Stop { return agents.Stop{Quit: "/quit", ClearInput: []string{"ctrl+u"}} }

// Headless: `codex exec` (alias e, owner 2026-10-27); exec also as a later word, as codex's optional prompt
// precedes its subcommand (`codex "hello" exec hi`); the alias is too short to count there. ValueOpts: codex --help.
func (kind) Headless() agents.Headless {
	return agents.Headless{Subcommands: []string{"exec", "e"}, Anywhere: []string{"exec"},
		ValueOpts: []string{"-c", "--config", "--enable", "--disable", "--remote", "--remote-auth-token-env", "-i",
			"--image", "-m", "--model", "--local-provider", "-p", "--profile", "-s", "--sandbox", "-C", "--cd",
			"--add-dir", "-a", "--ask-for-approval"}}
}

// Install: launch args on every start, nothing to install.
func (kind) Install(kit string, _ func(string) string) agents.Install {
	how := "launch args -c mcp_servers.obor.command=" + filepath.Join(kit, "bin", "obor") + ", env_vars=" + EnvVarsTOML() +
		", env " + agents.Pin + "=1"
	return agents.Install{MCP: agents.MCPRegistration{Where: agents.PerStart,
		Lane: func([]string, agents.ReadFile) (string, bool, bool) { return how, true, false }}}
}

// Transcript: $CODEX_HOME (default ~/.codex)/sessions/YYYY/MM/DD/rollout-<time>-<sid>.jsonl.
func (kind) Transcript(env func(string) string, sid string) string {
	dir := env("CODEX_HOME")
	if dir == "" {
		dir = filepath.Join(env("HOME"), ".codex")
	}
	return filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*-"+sid+".jsonl")
}

// Session: a rollout's first line is its session_meta {payload: {id, cwd, timestamp}}; the line's own
// timestamp when the payload has none.
func (kind) Session(first []byte) (agents.Session, bool) {
	var l struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			ID        string `json:"id"`
			Cwd       string `json:"cwd"`
			Timestamp string `json:"timestamp"`
		} `json:"payload"`
	}
	if json.Unmarshal(bytes.TrimSpace(first), &l) != nil || l.Type != "session_meta" || l.Payload.ID == "" {
		return agents.Session{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, l.Payload.Timestamp)
	if err != nil {
		at, _ = time.Parse(time.RFC3339Nano, l.Timestamp)
	}
	return agents.Session{ID: l.Payload.ID, Cwd: l.Payload.Cwd, Started: at}, true
}

// rolloutLine is the part of a rollout line LastTurn reads: an event_msg's turn events and its token counts' rate
// limits, with the line's time.
type rolloutLine struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type   string `json:"type"`
		TurnID string `json:"turn_id"`
		Error  *struct {
			Message string          `json:"message"`
			Info    json.RawMessage `json:"codex_error_info"`
		} `json:"error"`
		RateLimits *struct {
			Primary, Secondary *rateWindow
		} `json:"rate_limits"`
	} `json:"payload"`
}

// rateWindow is one rate-limit window of a token_count event: how much of it is used, when it resets (Unix seconds).
type rateWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    int64   `json:"resets_at"`
}

// LastTurn: the rollout's last turn event, read back from the end: task_started (running: Ended false), turn_aborted
// (ended, no error) or task_complete (ended), its error {message, codex_error_info} when the turn ended on one (seen
// 2026-10-15: "usage_limit_exceeded", "You've hit your usage limit … try again at 6:30 PM"). Error is
// codex_error_info as given: a string, or the one key of an object (a variant with data); "unknown" when it is
// neither. A line that is not JSON (the partial last one, a cut first one) is skipped.
//
// A turn ended on an error has its Reset from the turn's last token_count whose rate_limits has a window at
// 100% (seen 2026-10-28: {"primary":{"used_percent":100.0,"window_minutes":300,"resets_at":<epoch>}, …}; a later
// one of another limit with no windows is passed over): that window's resets_at, the later of two; else the time the
// message names ("try again at 6:00 PM", the clock of the line's time in the local zone); else zero.
func (kind) LastTurn(tail []byte) (agents.Turn, bool) {
	end := bytes.LastIndexByte(tail, '\n')
	if end < 0 {
		return agents.Turn{}, false
	}
	tail = tail[:end] // the bytes after the last newline are a line still being written
	var (
		t     agents.Turn
		ended bool      // t is a turn ended on an error: read on for its rate limits
		at    time.Time // its line's time
	)
	for len(tail) > 0 {
		line := tail
		if i := bytes.LastIndexByte(tail, '\n'); i >= 0 {
			line, tail = tail[i+1:], tail[:i]
		} else {
			tail = nil
		}
		var l rolloutLine
		if json.Unmarshal(line, &l) != nil || l.Type != "event_msg" {
			continue
		}
		if ended {
			if ty := l.Payload.Type; ty == "task_started" || ty == "turn_aborted" || ty == "task_complete" {
				break // the turn's start, or an earlier turn
			}
			if r := full(l.Payload.RateLimits); r > 0 {
				t.Reset = time.Unix(r, 0)
				return t, true
			}
			continue
		}
		t = agents.Turn{ID: l.Payload.TurnID, Ended: l.Payload.Type != "task_started"}
		switch l.Payload.Type {
		case "task_started", "turn_aborted":
			return t, true
		case "task_complete":
			e := l.Payload.Error
			if e == nil {
				return t, true
			}
			t.Error, t.Details = errorInfo(e.Info), e.Message
			at, _ = time.Parse(time.RFC3339, l.Timestamp)
			ended = true
		}
	}
	if !ended {
		return agents.Turn{}, false
	}
	if !at.IsZero() {
		t.Reset = agents.ResetIn(t.Details, at.In(time.Local))
	}
	return t, true
}

// full is the reset (Unix seconds) of the rate limits' windows at 100%, the later of two; 0: none is full.
func full(r *struct{ Primary, Secondary *rateWindow }) int64 {
	var reset int64
	if r == nil {
		return 0
	}
	for _, w := range []*rateWindow{r.Primary, r.Secondary} {
		if w != nil && w.UsedPercent >= 100 && w.ResetsAt > 0 {
			reset = max(reset, w.ResetsAt)
		}
	}
	return reset
}

// Reset: usage_limit_exceeded is the limit (the others, a failed connection or an overload, are not); its message
// names the reset as a clock time ("try again at 6:30 PM"; agents.ResetIn). LastTurn reads the rollout's own first.
func (kind) Reset(errText, details string, at time.Time) (time.Time, bool) {
	if errText != "usage_limit_exceeded" {
		return time.Time{}, false
	}
	return agents.ResetIn(details, at), true
}

// errorInfo is codex_error_info as given: a string, or the one key of an object; "unknown" otherwise.
func errorInfo(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for k := range m {
			return k
		}
	}
	return "unknown"
}

func (kind) Deliver(string, []byte) (agents.Delivery, bool) {
	return agents.Delivery{Typed: true}, true
}

func (kind) InboxRecord(func(string) string) []byte { return nil }

func (kind) Caps() agents.Caps {
	return agents.Caps{Hooks: agents.HooksNone, Resume: true, Transcript: true, Status: agents.StatusTranscript, Sessions: true}
}
