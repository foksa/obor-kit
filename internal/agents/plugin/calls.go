package plugin

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/project"
)

// Timeouts bound each call (start, turn and the per-poll ones short; describe and install longer): a slow plugin is an
// error naming the kind and the call, never a hang.
var Timeouts = map[string]time.Duration{
	"describe": 10 * time.Second, "install": 30 * time.Second,
	"start": 5 * time.Second, "not-ready": 2 * time.Second, "turn": 5 * time.Second, "deliver": 3 * time.Second,
	"project-warnings": 5 * time.Second,
}

// Log receives the failures of the calls whose agents.Kind method returns no error (not-ready aside: its failure is
// the reason a start waits): turn's and deliver's, so a broken plugin is seen, not only read as "no turn".
var Log io.Writer = os.Stderr

// replyMax bounds a reply (a turn's tail is the biggest request; a reply is small).
const replyMax = 4 << 20

// run is one call: req (its fields beside version and call) as JSON on the command's stdin, call as its one argument,
// its stdout one JSON reply decoded into reply. The command runs as its own process group under the call's timeout. An
// error names the kind and the call: a timeout, a non-zero exit (with its stderr's first line), a reply that does not
// parse, names another version, or carries error.
func (k *Kind) run(call string, req map[string]any, reply any) error {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("kind %s: %s: %s", k.name, call, fmt.Sprintf(format, a...))
	}
	body := map[string]any{"version": Version, "call": call}
	for key, v := range req {
		body[key] = v
	}
	in, err := json.Marshal(body)
	if err != nil {
		return fail("request: %v", err)
	}
	if k.local != nil {
		out, err := k.local(call, in)
		if err != nil {
			return fail("%v", err)
		}
		return decode(out, reply, fail)
	}
	limit := Timeouts[call]
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, k.command, call)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &stdout, &stderr
	cleanup := project.Group(cmd)
	err = cmd.Run()
	cleanup()
	var ee *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fail("no reply within %s", limit)
	case errors.As(err, &ee):
		why, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if why == "" {
			why = "no stderr"
		}
		return fail("exit %d: %s", ee.ExitCode(), why)
	case err != nil:
		return fail("%v", err)
	case stdout.Len() > replyMax:
		return fail("a reply over %d bytes", replyMax)
	}
	return decode(stdout.Bytes(), reply, fail)
}

// decode is the reply out into reply: an error (fail's) when it does not parse, names no or another version, or carries
// error.
func decode(out []byte, reply any, fail func(string, ...any) error) error {
	var env struct {
		Version *int   `json:"version"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fail("malformed reply: %v", err)
	}
	switch {
	case env.Version == nil:
		return fail("the reply names no version")
	case *env.Version != Version:
		return fail("reply version %d, obor speaks %d", *env.Version, Version)
	case env.Error != "":
		return fail("%s", env.Error)
	}
	if err := json.Unmarshal(out, reply); err != nil {
		return fail("malformed reply: %v", err)
	}
	return nil
}

// snapshot is env's values of the variables the manifest names (nil env: this process's).
func snapshot(m *manifest, env func(string) string) map[string]string {
	if env == nil {
		env = os.Getenv
	}
	out := map[string]string{}
	for _, v := range m.Env {
		out[v] = env(v)
	}
	return out
}

// startReply is start's reply: the argv after the binary, the start's own env, and the files obor writes into the
// state dir first, whose paths replace each {file:<name>} in args and env values.
type startReply struct {
	Args  []string          `json:"args"`
	Env   map[string]string `json:"env"`
	Files []struct {
		Name  string `json:"name"`
		Bytes string `json:"bytes"`
	} `json:"files"`
}

// start runs start with in as data (closure 1: the command reads a file itself; closure 2: the reply's files).
func (k *Kind) start(in agents.Start) (startReply, error) {
	m, err := k.manifest()
	if err != nil {
		return startReply{}, err
	}
	req := map[string]any{"env": snapshot(m, in.Env), "kit": in.Kit, "args": in.Args, "settings": in.Settings,
		"resume": in.Resume, "prompt": in.Prompt, "effort": in.Effort, "strict_mcp": in.StrictMCP,
		"trim_user": in.TrimUser, "overrides": in.Overrides, "session": in.Session, "write_dirs": in.WriteDirs,
		"dir": in.Dir, "root": in.Root}
	var r startReply
	if err := k.run("start", req, &r); err != nil {
		return startReply{}, err
	}
	for v := range r.Env {
		if !slices.Contains(m.Vars, v) {
			return startReply{}, fmt.Errorf("kind %s: start: env %s is not among the manifest's vars", k.name, v)
		}
	}
	write := in.WriteState
	if write == nil {
		write = k.writeState
	}
	paths := map[string]string{}
	for _, f := range r.Files {
		if !filepath.IsLocal(f.Name) {
			return startReply{}, fmt.Errorf("kind %s: start: file %q is no name in the state dir", k.name, f.Name)
		}
		p, err := write(f.Name, []byte(f.Bytes))
		if err != nil {
			return startReply{}, fmt.Errorf("kind %s: start: file %s: %v", k.name, f.Name, err)
		}
		paths["{file:"+f.Name+"}"] = p
	}
	sub := func(s string) string {
		for ph, p := range paths {
			s = strings.ReplaceAll(s, ph, p)
		}
		return s
	}
	for i, a := range r.Args {
		r.Args[i] = sub(a)
	}
	for v, val := range r.Env {
		r.Env[v] = sub(val)
	}
	if r.Args == nil {
		r.Args = []string{}
	}
	return r, nil
}

// writeState is a start file written as name in k's state dir (project.StateWriter's), when the caller gives no
// WriteState.
func (k *Kind) writeState(name string, data []byte) (string, error) {
	if k.stateDir == "" {
		return "", errors.New("no state dir to write it to")
	}
	path := filepath.Join(k.stateDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, project.WriteAtomic(path, data)
}

// StartWhole is one start (agents.Whole): one start call with the complete start, its argv, env and files
// applied together.
func (k *Kind) StartWhole(in agents.Start) (agents.Launch, error) {
	r, err := k.start(in)
	if err != nil {
		return agents.Launch{}, err
	}
	return agents.Launch{Args: r.Args, Env: r.Env}, nil
}

// CheckEffort (agents.ArgsChecker) is why args cannot carry effort: a start of the args and effort alone, its reply's
// error (an effort that needs an arg the args lack: OpenCode's model id); its files are not written.
func (k *Kind) CheckEffort(args []string, effort string) error {
	if _, err := k.manifest(); err != nil {
		return err
	}
	var r startReply
	return k.run("start", map[string]any{"args": args, "effort": effort}, &r)
}

// ProjectWarnings (agents.ProjectChecker) runs project-warnings {dir} for a manifest with project_warnings (the kind
// reads the files itself: read is the compiled kinds'); a failed call is its one warning.
func (k *Kind) ProjectWarnings(dir string, _ agents.ReadFile) []string {
	if m := k.cached(); m == nil || !m.ProjectWarnings {
		return nil
	}
	if _, err := k.manifest(); err != nil {
		return []string{err.Error()}
	}
	var r struct {
		Warnings []string `json:"warnings"`
	}
	if err := k.run("project-warnings", map[string]any{"dir": dir}, &r); err != nil {
		return []string{err.Error()}
	}
	return r.Warnings
}

// NotReady is the manifest's pattern's reason when it matches the screen, or not-ready's why; a failed call is the
// reason (the start waits, then says it).
func (k *Kind) NotReady(screen string) string {
	m := k.cached()
	switch {
	case m == nil || m.NotReady == nil:
		return ""
	case m.notReady != nil:
		if !m.notReady.MatchString(screen) {
			return ""
		}
		return cmp.Or(m.NotReady.Why, "not ready ("+m.NotReady.Pattern+" on the screen)")
	}
	var r struct {
		Why string `json:"why"`
	}
	if _, err := k.manifest(); err != nil {
		return err.Error()
	}
	if err := k.run("not-ready", map[string]any{"screen": screen}, &r); err != nil {
		return err.Error()
	}
	return r.Why
}

// installReply is install's reply: the MCP registration as data; setup checks it. A reply that still carries the
// removed user-config install path (mcp.where "config", a plugin link under ~/) is refused.
type installReply struct {
	MCP struct {
		Where    string         `json:"where"`
		Path     string         `json:"path"`
		Key      []string       `json:"key"`
		Entry    map[string]any `json:"entry"`
		Unpinned map[string]any `json:"unpinned"`
	} `json:"mcp"`
	Plugin json.RawMessage `json:"plugin"` // removed: any non-null value is refused
}

// InstallErr runs install; the registration's Lane is the manifest's mcp_lane (closure 3 as data).
func (k *Kind) InstallErr(kit string, env func(string) string) (agents.Install, error) {
	m, err := k.manifest()
	if err != nil {
		return failedInstall(err), err
	}
	var r installReply
	if err := k.run("install", map[string]any{"kit": kit, "env": snapshot(m, env)}, &r); err != nil {
		return failedInstall(err), err
	}
	var removed error
	switch {
	case r.MCP.Where == "config":
		removed = fmt.Errorf("kind %s: install: mcp.where %q is no longer supported: the user-config install path was "+
			"removed (T-1119); a kind registers per launch or through the project's files", k.name, r.MCP.Where)
	case len(r.Plugin) > 0 && string(r.Plugin) != "null":
		removed = fmt.Errorf("kind %s: install: the plugin link is no longer supported: the user-config install path was "+
			"removed (T-1119); a kind installs nothing under ~/", k.name)
	}
	if removed != nil {
		return failedInstall(removed), removed
	}
	if !slices.Contains([]string{agents.Manifest, agents.PerStart}, r.MCP.Where) {
		err := fmt.Errorf("kind %s: install: mcp.where %q is none of %s, %s", k.name, r.MCP.Where, agents.Manifest,
			agents.PerStart)
		return failedInstall(err), err
	}
	lane := m.MCPLane
	how := strings.ReplaceAll(lane.How, "{kit}", kit)
	in := agents.Install{MCP: agents.MCPRegistration{Where: r.MCP.Where, Path: r.MCP.Path,
		Key: r.MCP.Key, Entry: r.MCP.Entry, Unpinned: r.MCP.Unpinned,
		Lane: func([]string, agents.ReadFile) (string, bool, bool) { return how, lane.Registered, lane.Static }}}
	return in, nil
}

// failedInstall is an install that failed with err: nothing registered, Lane saying why.
func failedInstall(err error) agents.Install {
	return agents.Install{MCP: agents.MCPRegistration{Where: agents.PerStart,
		Lane: func([]string, agents.ReadFile) (string, bool, bool) { return err.Error(), false, false }}}
}

// Install is InstallErr's registration (failedInstall when it fails; agents.InstallOf gives the error).
func (k *Kind) Install(kit string, env func(string) string) agents.Install {
	in, _ := k.InstallErr(kit, env)
	return in
}

// turn runs turn with what (last, session or reset) and its fields; a failure goes to Log.
func (k *Kind) turn(what string, req map[string]any, reply any) bool {
	if _, err := k.manifest(); err != nil {
		fmt.Fprintf(Log, "obor: %v\n", err)
		return false
	}
	req["what"] = what
	if err := k.run("turn", req, reply); err != nil {
		fmt.Fprintf(Log, "obor: %v\n", err)
		return false
	}
	return true
}

// parseTime is an RFC 3339 time ("" or unparsable: zero).
func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// LastTurn runs turn {what: last, tail}.
func (k *Kind) LastTurn(tail []byte) (agents.Turn, bool) {
	var r struct {
		OK   bool `json:"ok"`
		Turn struct {
			ID      string `json:"id"`
			Error   string `json:"error"`
			Details string `json:"details"`
			Ended   bool   `json:"ended"`
			Reset   string `json:"reset"`
		} `json:"turn"`
	}
	if !k.turn("last", map[string]any{"tail": string(tail)}, &r) || !r.OK {
		return agents.Turn{}, false
	}
	t := r.Turn
	return agents.Turn{ID: t.ID, Error: t.Error, Details: t.Details, Ended: t.Ended, Reset: parseTime(t.Reset)}, true
}

// Session runs turn {what: session, first}.
func (k *Kind) Session(first []byte) (agents.Session, bool) {
	var r struct {
		OK      bool `json:"ok"`
		Session struct {
			ID      string `json:"id"`
			Cwd     string `json:"cwd"`
			Started string `json:"started"`
		} `json:"session"`
	}
	if !k.turn("session", map[string]any{"first": string(first)}, &r) || !r.OK {
		return agents.Session{}, false
	}
	return agents.Session{ID: r.Session.ID, Cwd: r.Session.Cwd, Started: parseTime(r.Session.Started)}, true
}

// Reset runs turn {what: reset, error, details, at}.
func (k *Kind) Reset(errText, details string, at time.Time) (time.Time, bool) {
	var r struct {
		Limited bool   `json:"limited"`
		Reset   string `json:"reset"`
	}
	req := map[string]any{"error": errText, "details": details, "at": at.Format(time.RFC3339)}
	if !k.turn("reset", req, &r) {
		return time.Time{}, false
	}
	return parseTime(r.Reset), r.Limited
}

// Deliver is a typed line for a manifest with typed, else deliver {text, record}; a failure keeps the line queued.
func (k *Kind) Deliver(text string, record []byte) (agents.Delivery, bool) {
	if m := k.cached(); m != nil && m.Typed {
		return agents.Delivery{Typed: true}, true
	}
	if _, err := k.manifest(); err != nil {
		fmt.Fprintf(Log, "obor: %v\n", err)
		return agents.Delivery{}, false
	}
	var r struct {
		OK       bool `json:"ok"`
		Delivery struct {
			Typed  bool     `json:"typed"`
			Socket string   `json:"socket"`
			Lines  []string `json:"lines"`
		} `json:"delivery"`
	}
	if err := k.run("deliver", map[string]any{"text": text, "record": string(record)}, &r); err != nil {
		fmt.Fprintf(Log, "obor: %v\n", err)
		return agents.Delivery{}, false
	}
	d := r.Delivery
	return agents.Delivery{Typed: d.Typed, Socket: d.Socket, Lines: d.Lines}, r.OK
}
