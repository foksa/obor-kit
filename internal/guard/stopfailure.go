package guard

// The StopFailure hook (`obor hook stop-failure`): Claude Code fires it instead of Stop when a turn ends on an
// API error (a usage limit, an overload, …). The hook logs the payload's error and error_details and tells obor serve
// (cmd/obor sends the request); it never blocks and has no verdict. A worker's stop ends with its next turn: its
// UserPromptSubmit and Stop hooks tell serve (Turn).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/project"
)

// StopFailureLog is the hook's log under the project's state dir: one JSON line per StopFailure, the raw error and
// error_details with a timestamp and the agent (we do not know yet what every limit reports).
const StopFailureLog = project.StopFailureLog

// Stopped is what the StopFailure hook tells serve: the project, the agent and its role (a worker's lane), the error
// and its details as text (a turn's: none).
type Stopped struct {
	Root, Agent, Role, Lane string
	Error, Details          string
}

// stopFailureEvent is the part of the StopFailure event the hook reads; error and error_details stay raw for the log.
type stopFailureEvent struct {
	Cwd          string          `json:"cwd"`
	Error        json.RawMessage `json:"error"`
	ErrorDetails json.RawMessage `json:"error_details"`
}

// StopFailure reads the StopFailure event in input, appends its log line and returns what serve is told; ok false
// outside an obor project or on an unreadable event (nothing to send). The agent is the session's derived name
// (project.SelfAgent: a lane's from its cwd, so OpenCode's shared hook process without env names it too).
// A log line that cannot be written is err (the hook says so and still sends).
func StopFailure(input []byte, env project.Env, now time.Time) (s Stopped, ok bool, err error) {
	var ev stopFailureEvent
	if json.Unmarshal(input, &ev) != nil {
		return Stopped{}, false, nil
	}
	cwd := ev.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	repo, cfg, _, lerr := loadAt(cwd)
	if repo == nil || lerr != nil { // no obor project (config.ErrNoConfig), or a config that does not load: nothing to tell
		return Stopped{}, false, nil
	}
	role := project.DetectRole(repo, cfg, env)
	s = Stopped{Root: cfg.Root, Agent: project.SelfAgent(cfg, role), Role: role.Role, Lane: role.Lane,
		Error: rawText(ev.Error), Details: rawText(ev.ErrorDetails)}
	line, _ := json.Marshal(struct {
		At           string          `json:"at"`
		Agent        string          `json:"agent,omitempty"`
		Error        json.RawMessage `json:"error,omitempty"`
		ErrorDetails json.RawMessage `json:"error_details,omitempty"`
	}{now.UTC().Format(time.RFC3339), s.Agent, ev.Error, ev.ErrorDetails})
	err = appendLine(filepath.Join(project.StateDir(cfg.Root), StopFailureLog), line)
	return s, true, err
}

// Turn is what a worker's or role agent's UserPromptSubmit or Stop hook tells serve besides its own job:
// the agent's next turn began or ended, which ends a stop in force; ok false outside an obor project, on an
// unreadable event, for the lead or a session of no role, or with no agent name. The agent is the session's derived
// name (project.SelfAgent).
func Turn(input []byte, env project.Env) (s Stopped, ok bool) {
	var ev struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(input, &ev) != nil {
		return Stopped{}, false
	}
	if ev.Cwd == "" {
		ev.Cwd, _ = os.Getwd()
	}
	repo, cfg, _, err := loadAt(ev.Cwd)
	if repo == nil || err != nil {
		return Stopped{}, false
	}
	role := project.DetectRole(repo, cfg, env)
	s = Stopped{Root: cfg.Root, Agent: project.SelfAgent(cfg, role), Role: role.Role, Lane: role.Lane}
	return s, role.Role != "lead" && role.Role != "none" && role.Role != "" && s.Agent != ""
}

// rawText is a raw JSON value as text: a string's value, else the JSON itself ("" for none or null).
func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.Join(strings.Fields(s), " ")
	}
	if t := strings.TrimSpace(string(raw)); t != "null" {
		return t
	}
	return ""
}

// appendLine appends line and a newline to path, its directory made.
func appendLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
