// Package codex is the Codex CLI bridge (agents.Kind): `resume <sid>` first, obor's MCP server as -c options on every
// start (~/.codex/config.toml is never edited), its startup update check off, /quit, `codex exec` denied, rollout
// transcripts, prompts typed.
package codex

import (
	"bytes"
	"encoding/json"
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
// -c model_reasoning_effort=<effort>, T-695), then obor's server (with a kit), then the update check off (T-570),
// then the prompt (a resume carries none). Codex takes no settings.
func (kind) Start(in agents.Start) []string {
	var out []string
	if in.Resume != "" {
		out = append(out, "resume", in.Resume)
	}
	out = append(out, withEffort(in.Args, in.Effort)...)
	if in.Kit != "" {
		out = append(out,
			"-c", `mcp_servers.obor.command="`+filepath.Join(in.Kit, "bin", "obor")+`"`,
			"-c", `mcp_servers.obor.args=["mcp"]`,
			"-c", "mcp_servers.obor.env_vars="+EnvVarsTOML(),
			"-c", "mcp_servers.obor.env={"+agents.Pin+`="1"}`,
			// lanes run -a never: without this every obor tool call fails "requires approval" (T-390)
			"-c", `mcp_servers.obor.default_tools_approval_mode="approve"`)
	}
	// Codex's own "Update available" menu blocks a fresh start until someone picks "Skip" (T-570); a type arg that
	// sets the key wins.
	if !updateCheckSet(in.Args) {
		out = append(out, "-c", updateCheck+"=false")
	}
	if in.Prompt != "" && in.Resume == "" {
		out = append(out, in.Prompt)
	}
	return out
}

// effortKey is Codex's reasoning effort setting.
const effortKey = "model_reasoning_effort"

// efforts are Codex's model_reasoning_effort values: `codex --help` lists none, so they are the efforts of Codex 0.166's
// built-in models (low, medium, high, xhigh, max, ultra; luna tops out at max, per-model limits are not checked;
// no minimal, T-799).
var efforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

func (kind) Efforts() []string { return efforts }

// StartEnv: none; the effort is a -c option.
func (kind) StartEnv(agents.Start) (map[string]string, error) { return nil, nil }

// Vars: none (StartEnv sets none).
func (kind) Vars() []string { return nil }

// WithModel: -m <model> in place of the args' own (-m or --model, T-753).
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

// updateCheckSet: the type's args set updateCheck themselves (`-c key=value`, `--config key=value` or their
// `key=value` word, either spelling of the flag with =).
func updateCheckSet(args []string) bool {
	for _, a := range args {
		a = strings.TrimPrefix(a, "-c=")
		a = strings.TrimPrefix(a, "--config=")
		if key, _, ok := strings.Cut(a, "="); ok && strings.TrimSpace(key) == updateCheck {
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

// Headless: `codex exec` (alias e, T-470, owner 2026-10-27); exec also as a later word, as codex's optional prompt
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

// Transcript: $CODEX_HOME (default ~/.codex)/sessions/YYYY/MM/DD/rollout-<time>-<sid>.jsonl; the path records no
// cwd.
func (kind) Transcript(env func(string) string, cwd, sid string) string {
	if cwd != "" {
		return ""
	}
	dir := env("CODEX_HOME")
	if dir == "" {
		dir = filepath.Join(env("HOME"), ".codex")
	}
	return filepath.Join(dir, "sessions", "*", "*", "*", "rollout-*-"+sid+".jsonl")
}

// Session: a rollout's first line is its session_meta {payload: {id, cwd, timestamp}} (T-577); the line's own
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
// A turn ended on an error has its Reset (T-568) from the turn's last token_count whose rate_limits has a window at
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
	return agents.Caps{Hooks: agents.HooksNone, Resume: true, Transcript: true, Turns: true, Sessions: true}
}
