// Package service is the launchd adapter (phase 7 advice §2): one LaunchAgent per project runs its obor serve, and
// one per machine runs obor web, so both outlive the pane (and herdr) they were typed in. Darwin only; elsewhere
// every verb says to run obor serve yourself.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Elsewhere is every verb's line off darwin (exit 0: nothing to do there yet).
const Elsewhere = "run `obor serve` yourself (systemd --user later)"

// EnvVar is one variable of the caller's environment a serve launchd starts must have: the terminal's own, as its
// adapter names them.
type EnvVar struct {
	Name, Value string
}

// Service is one project's LaunchAgent.
type Service struct {
	Name string   // the project's config name: the label is obor.serve.<Name>
	Root string   // the project's main checkout: the agent's WorkingDirectory
	Kit  string   // the obor kit: the agent runs <Kit>/bin/obor serve
	Home string   // the user's home: the plist is in <Home>/Library/LaunchAgents
	UID  int      // the gui/<uid> domain
	GOOS string   // runtime.GOOS: launchd only on darwin
	Path string   // the caller's PATH (launchd's own has no claude, codex, herdr or jq)
	Env  []EnvVar // the terminal's variables as the caller has them (none: not written)
}

// Web is the machine's page agent: one LaunchAgent per machine (obor web's page covers every project), labelled
// obor.web. It runs <Kit>/bin/obor web on its default listen and logs to ~/.obor/web.log, a machine path.
type Web struct {
	Kit  string   // the obor kit: the agent runs <Kit>/bin/obor web
	Home string   // the user's home: the plist is in <Home>/Library/LaunchAgents, the log in <Home>/.obor
	UID  int      // the gui/<uid> domain
	GOOS string   // runtime.GOOS: launchd only on darwin
	Path string   // the caller's PATH
	Env  []EnvVar // the terminal's variables as the caller has them (none: not written)
}

// Supported: launchd is there (darwin).
func (s Service) Supported() bool { return s.GOOS == "darwin" }

// Label is the agent's launchd label.
func (s Service) Label() string { return "obor.serve." + s.Name }

// PlistPath is the agent's plist: ~/Library/LaunchAgents/<label>.plist.
func (s Service) PlistPath() string {
	return filepath.Join(s.Home, "Library", "LaunchAgents", s.Label()+".plist")
}

// ServeLog is the project's serve log (its stdout and stderr): .obor/state/logs/serve.log, by the check logs.
func ServeLog(root string) string {
	return filepath.Join(root, ".obor", "state", "logs", "serve.log")
}

// LogPath is where the agent's serve writes (stdout and stderr): .obor/state/logs/serve.log, by the check logs.
func (s Service) LogPath() string { return ServeLog(s.Root) }

// MaxLog is the size over which a serve.log is moved aside: Install and Restart rename it, serve's own start copies
// it and truncates it in place.
const MaxLog = 5 << 20

// logOver is path's size over MaxLog (a missing log is not).
func logOver(path string) (bool, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.Size() > MaxLog, nil
}

// rotateLog moves serve.log to serve.log.1 (replacing an old one) when it is over MaxLog. Install and Restart run
// outside serve and launchd's next spawn opens the new path, so they rename; serve's own start cannot (RotateServeLog).
// Missing or small: nothing. A crash respawn by launchd is not rotated.
func (s Service) rotateLog() error {
	log := s.LogPath()
	over, err := logOver(log)
	if err != nil || !over {
		return err
	}
	return os.Rename(log, log+".1")
}

// RotateServeLog is the rotation serve itself does at its start, re-execs included: a serve.log over MaxLog
// is copied to serve.log.1 (replacing an old one) and truncated in place. launchd keeps the fd it opened for serve —
// with O_APPEND — across a serve's respawn, and the re-exec keeps the same process's fds, so a rename here would leave
// launchd writing the moved file while a fresh serve.log stayed empty; truncating keeps that fd on the same, now
// empty, file, and its next write lands at offset 0. Missing or small: nothing.
func RotateServeLog(root string) error {
	log := ServeLog(root)
	over, err := logOver(log)
	if err != nil || !over {
		return err
	}
	src, err := os.Open(log)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(log + ".1")
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Truncate(log, 0)
}

func (s Service) target() string { return fmt.Sprintf("gui/%d/%s", s.UID, s.Label()) }

// agent is what a launchd agent's plist carries: its label, the program and its arguments, the working directory,
// the file its stdout and stderr go to, and the caller's PATH and variables.
type agent struct {
	label string
	args  []string
	dir   string
	log   string
	path  string
	env   []EnvVar
}

// plist is the agent's plist: started at load, kept alive, its output in log, run in its working directory, PATH
// then the terminal's own variables (escaped, in the order the adapter names them); an entry with no name is left out.
func (a agent) plist() []byte {
	var b bytes.Buffer
	str := func(indent, v string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, esc(v)) }
	key := func(indent, k string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, k) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("\t", "Label")
	str("\t", a.label)
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range a.args {
		str("\t\t", arg)
	}
	b.WriteString("\t</array>\n")
	key("\t", "WorkingDirectory")
	str("\t", a.dir)
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "KeepAlive")
	b.WriteString("\t<true/>\n")
	key("\t", "StandardOutPath")
	str("\t", a.log)
	key("\t", "StandardErrorPath")
	str("\t", a.log)
	key("\t", "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	key("\t\t", "PATH")
	str("\t\t", a.path)
	for _, v := range a.env {
		if v.Name == "" {
			continue
		}
		key("\t\t", v.Name)
		str("\t\t", v.Value)
	}
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return b.Bytes()
}

// agent is the plist's content: obor serve in the project, its output in LogPath, the caller's PATH and the
// terminal's own variables (Env).
func (s Service) agent() agent {
	return agent{label: s.Label(), args: []string{filepath.Join(s.Kit, "bin", "obor"), "serve"}, dir: s.Root,
		log: s.LogPath(), path: s.Path, env: s.Env}
}

// Plist is the agent's plist: obor serve in the project, started at load, kept alive, its output in LogPath, the
// caller's PATH and the terminal's own variables (Env).
func (s Service) Plist() []byte { return s.agent().plist() }

// agentInstall is serve's install path: its plist, and the log rotation its start owes (Web's has none).
func (s Service) agentInstall() agentInstall {
	return agentInstall{a: s.agent(), path: s.PlistPath(), uid: s.UID, before: s.rotateLog}
}

// agentInstall is one launchd agent's install path: the plist's content, where the plist and its log go, the gui
// domain, and the work before a start that only serve has (a serve.log over MaxLog is moved aside). Serve's and
// Web's Install are both it.
type agentInstall struct {
	a      agent
	path   string       // the plist
	uid    int          // the gui/<uid> domain
	before func() error // serve: rotateLog; web: nil
}

// target is the label's launchd target: gui/<uid>/<label>.
func (i agentInstall) target() string { return fmt.Sprintf("gui/%d/%s", i.uid, i.a.label) }

// install writes the plist and loads it (bootstrap; one loaded before, as loaded says, is booted out first, so a
// reinstall takes the new plist). A bootstrap launchd refuses — the label it still knows, or half knows while the old
// one is torn down ("Bootstrap failed: 5: Input/output error" left the label gone) — is booted out and bootstrapped
// once more; an install that ends with the label not loaded is a FAIL naming the label's state and the next step.
func (i agentInstall) install(ctx context.Context, loaded bool) (string, error) {
	for _, dir := range []string{filepath.Dir(i.path), filepath.Dir(i.a.log)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if i.before != nil {
		if err := i.before(); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(i.path, i.a.plist(), 0o644); err != nil {
		return "", err
	}
	if loaded {
		if err := i.bootout(ctx); err != nil {
			return "", err
		}
	}
	domain := fmt.Sprintf("gui/%d", i.uid)
	_, err := launchctl(ctx, callTimeout, "bootstrap", domain, i.path)
	if err != nil {
		_ = i.bootout(ctx) // not loaded at all: its error is the expected one
		if _, again := launchctl(ctx, callTimeout, "bootstrap", domain, i.path); again != nil {
			err = fmt.Errorf("%v; after a bootout, again: %v", err, again)
		} else {
			err = nil
		}
	}
	pid, loaded := runPID(ctx, true, i.target())
	switch {
	case err != nil && loaded:
		return "", fmt.Errorf("%v: %s is loaded (pid %d), maybe from the plist before this one: obor serve install again", err, i.a.label, pid)
	case err != nil:
		return "", fmt.Errorf("%v: %s is not loaded in launchd: obor serve install again (launchctl print %s shows launchd's view)", err, i.a.label, i.target())
	case !loaded:
		return "", fmt.Errorf("launchctl bootstrap %s %s returned, but %s is not loaded in launchd: obor serve install again (log %s)",
			domain, i.path, i.a.label, i.a.log)
	}
	return fmt.Sprintf("INSTALLED %s (%s; log %s)", i.a.label, i.path, i.a.log), nil
}

// current: the plist on disk is the one install would write and launchd has the label loaded, so a present plist
// launchd does not have loaded is not current (an install left it unloaded).
func (i agentInstall) current(ctx context.Context) bool {
	b, err := os.ReadFile(i.path)
	if err != nil || !bytes.Equal(b, i.a.plist()) {
		return false
	}
	_, loaded := runPID(ctx, true, i.target())
	return loaded
}

// bootout unloads the label (launchd stops its process first: up to its ExitTimeOut) and waits, up to settle, until
// launchd no longer knows it, so a bootstrap after it does not meet the label half torn down.
func (i agentInstall) bootout(ctx context.Context) error {
	_, err := launchctl(ctx, stopTimeout, "bootout", i.target())
	poll(ctx, func() (int, bool) { return runPID(ctx, true, i.target()) }, func(_ int, loaded bool) bool { return !loaded })
	return err
}

// Label is the web agent's launchd label: one agent per machine.
func (w Web) Label() string { return "obor.web" }

// Supported: launchd is there (darwin).
func (w Web) Supported() bool { return w.GOOS == "darwin" }

// PlistPath is the agent's plist: ~/Library/LaunchAgents/obor.web.plist.
func (w Web) PlistPath() string {
	return filepath.Join(w.Home, "Library", "LaunchAgents", w.Label()+".plist")
}

// LogPath is where the agent's web writes (stdout and stderr): ~/.obor/web.log, a machine path, not a project's.
func (w Web) LogPath() string { return filepath.Join(w.Home, ".obor", "web.log") }

// agent is the plist's content: obor web from the kit in the user's home, its output in LogPath, the caller's PATH
// and the terminal's own variables (Env).
func (w Web) agent() agent {
	return agent{label: w.Label(), args: []string{filepath.Join(w.Kit, "bin", "obor"), "web"}, dir: w.Home,
		log: w.LogPath(), path: w.Path, env: w.Env}
}

// Plist is the agent's plist: obor web from the kit in the user's home, started at load, kept alive, its output in
// LogPath, the caller's PATH and the terminal's own variables (Env).
func (w Web) Plist() []byte { return w.agent().plist() }

// agentInstall is the web agent's install path: its plist, and no log rotation (web.log has none).
func (w Web) agentInstall() agentInstall {
	return agentInstall{a: w.agent(), path: w.PlistPath(), uid: w.UID}
}

func (w Web) target() string { return fmt.Sprintf("gui/%d/%s", w.UID, w.Label()) }

func esc(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

var workDir = regexp.MustCompile(`<key>WorkingDirectory</key>\s*<string>([^<]*)</string>`)

// installedRoot is the WorkingDirectory of the plist at path ("" with no plist; an error when it has none).
func installedRoot(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	m := workDir.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("%s names no WorkingDirectory", path)
	}
	var v struct {
		S string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<s>"+string(m[1])+"</s>"), &v); err != nil {
		return "", err
	}
	return v.S, nil
}

// owner is the check all three verbs run before they touch launchd: the label's plist, when there is one,
// must name this root — another project's plist of the same label is not this one's to boot out, kickstart or
// overwrite — and a label launchd has loaded with no plist is one whose owner cannot be told. So nothing is done to
// another project's agent, nor to one nobody can claim. pid and loaded are launchd's state of the label.
func (s Service) owner(ctx context.Context) (pid int, loaded bool, err error) {
	plist := s.PlistPath()
	root, err := installedRoot(plist)
	if err != nil {
		return 0, false, err
	}
	if root != "" && filepath.Clean(root) != filepath.Clean(s.Root) {
		return 0, false, fmt.Errorf("%s runs obor serve for %s, not %s: two projects named %q (rename one: config name)",
			plist, root, s.Root, s.Name)
	}
	pid, loaded = s.PID(ctx)
	if root == "" && loaded {
		return 0, false, fmt.Errorf("%s is loaded in launchd with no plist: cannot tell whose it is (launchctl bootout gui/%d/%s by hand)",
			s.Label(), s.UID, s.Label())
	}
	return pid, loaded, nil
}

// Current: the plist on disk is the one Install would write and launchd has the label loaded (obor init's rerun
// leaves such an agent alone instead of restarting its serve).
func (s Service) Current(ctx context.Context) bool {
	if !s.Supported() {
		return false
	}
	return s.agentInstall().current(ctx)
}

// Install writes the plist and loads it, as agentInstall's install does, refusing another project's label first
// (owner).
func (s Service) Install(ctx context.Context) (string, error) {
	if !s.Supported() {
		return Elsewhere, nil
	}
	if s.Name == "" {
		return "", errors.New("the project has no name")
	}
	_, loaded, err := s.owner(ctx)
	if err != nil {
		return "", err
	}
	return s.agentInstall().install(ctx, loaded)
}

// Uninstall unloads the agent (bootout, which stops its serve) and removes its plist. Another project's label
// (owner): FAIL, nothing done.
func (s Service) Uninstall(ctx context.Context) (string, error) {
	if !s.Supported() {
		return Elsewhere, nil
	}
	plist := s.PlistPath()
	_, loaded, err := s.owner(ctx)
	if err != nil {
		return "", err
	}
	_, statErr := os.Stat(plist)
	if !loaded && errors.Is(statErr, os.ErrNotExist) {
		return fmt.Sprintf("%s is not installed", s.Label()), nil
	}
	if loaded {
		if err := s.agentInstall().bootout(ctx); err != nil {
			return "", err
		}
	}
	if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "UNINSTALLED " + s.Label(), nil
}

// Restart kills the agent's serve and starts it again (kickstart -k): a new binary takes over. kickstart -k waits
// for the old serve to exit (launchd's ExitTimeOut), so it runs under stopTimeout. A kickstart error is not the last
// word: launchd may have restarted serve all the same, so the agent's pid is read (up to settle) — a new one
// is RESTARTED with a warning line, anything else a FAIL naming the state it left. Another project's label (owner):
// FAIL, nothing done.
func (s Service) Restart(ctx context.Context) (string, error) {
	if !s.Supported() {
		return Elsewhere, nil
	}
	old, loaded, err := s.owner(ctx)
	if err != nil {
		return "", err
	}
	if !loaded {
		return "", fmt.Errorf("%s is not installed (obor serve install)", s.Label())
	}
	if err := s.rotateLog(); err != nil {
		return "", err
	}
	_, err = launchctl(ctx, stopTimeout, "kickstart", "-k", s.target())
	if err == nil {
		return "RESTARTED " + s.Label(), nil
	}
	// The caller's deadline may be what ended launchctl: the read gets its own.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settle+callTimeout)
	defer cancel()
	restarted := func(pid int, loaded bool) bool { return loaded && pid != 0 && pid != old }
	pid, loaded := s.poll(rctx, restarted)
	switch {
	case restarted(pid, loaded):
		return fmt.Sprintf("RESTARTED %s (pid %d)\nwarning: %v: serve restarted all the same (pid %d before)",
			s.Label(), pid, err, old), nil
	case !loaded:
		return "", fmt.Errorf("%v: %s is no longer loaded in launchd: obor serve install", err, s.Label())
	case pid == 0:
		return "", fmt.Errorf("%v: %s is loaded but no serve runs (log %s): obor serve restart again", err, s.Label(), s.LogPath())
	}
	return "", fmt.Errorf("%v: %s still runs the old serve (pid %d), not restarted: obor serve restart again", err, s.Label(), pid)
}

// poll reads the agent's pid until done says so or settle has passed, every pollEvery: the last read.
func (s Service) poll(ctx context.Context, done func(pid int, loaded bool) bool) (int, bool) {
	return poll(ctx, func() (int, bool) { return s.PID(ctx) }, done)
}

// poll reads a label's pid (pidFn) until done says so or settle has passed, every pollEvery: the last read.
func poll(ctx context.Context, pidFn func() (int, bool), done func(pid int, loaded bool) bool) (int, bool) {
	until := time.Now().Add(settle)
	for {
		pid, loaded := pidFn()
		if done(pid, loaded) || !time.Now().Before(until) {
			return pid, loaded
		}
		select {
		case <-ctx.Done():
			return pid, loaded
		case <-time.After(pollEvery):
		}
	}
}

var pidLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// PID is the agent's process (launchctl print): loaded says launchd knows the label (print exits 113 for one it
// does not), pid its running process (0: none now). Off darwin: not loaded.
func (s Service) PID(ctx context.Context) (pid int, loaded bool) {
	return runPID(ctx, s.Supported(), s.target())
}

// PID is the agent's process: as Service.PID.
func (w Web) PID(ctx context.Context) (pid int, loaded bool) {
	return runPID(ctx, w.Supported(), w.target())
}

// runPID is a label's PID: launchctl print's pid while loaded (0: loaded, no process now); not loaded when launchd
// does not know the label (or the host has no launchd).
func runPID(ctx context.Context, supported bool, target string) (pid int, loaded bool) {
	if !supported {
		return 0, false
	}
	out, err := launchctl(ctx, callTimeout, "print", target)
	if err != nil {
		return 0, false
	}
	if m := pidLine.FindStringSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	return pid, true
}

// Current: the plist on disk is the one Install would write and launchd has the label loaded (obor serve install
// leaves such a web agent alone instead of restarting its page).
func (w Web) Current(ctx context.Context) bool {
	if !w.Supported() {
		return false
	}
	return w.agentInstall().current(ctx)
}

// Install writes the plist and loads it, as agentInstall's install does.
func (w Web) Install(ctx context.Context) (string, error) {
	if !w.Supported() {
		return Elsewhere, nil
	}
	_, loaded := w.PID(ctx)
	return w.agentInstall().install(ctx, loaded)
}

// Uninstall unloads the web agent (bootout, which stops its web) and removes its plist.
func (w Web) Uninstall(ctx context.Context) (string, error) {
	if !w.Supported() {
		return Elsewhere, nil
	}
	plist := w.PlistPath()
	_, loaded := w.PID(ctx)
	_, statErr := os.Stat(plist)
	if !loaded && errors.Is(statErr, os.ErrNotExist) {
		return w.Label() + " is not installed", nil
	}
	if loaded {
		if err := w.agentInstall().bootout(ctx); err != nil {
			return "", err
		}
	}
	if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "UNINSTALLED " + w.Label(), nil
}

// ServePlists is the project launch agents installed for a home: <Home>/Library/LaunchAgents/obor.serve.*.plist.
func ServePlists(home string) ([]string, error) {
	return filepath.Glob(filepath.Join(home, "Library", "LaunchAgents", "obor.serve.*.plist"))
}

// UninstallLast unloads and removes the web agent when no project's launch agent is left: the page goes with the
// machine's last obor serve. "" (no error) when another project's agent is there and the page stays.
func (w Web) UninstallLast(ctx context.Context) (string, error) {
	if !w.Supported() {
		return Elsewhere, nil
	}
	left, err := ServePlists(w.Home)
	if err != nil {
		return "", err
	}
	if len(left) > 0 {
		return "", nil
	}
	return w.Uninstall(ctx)
}

// Deadline is the most a verb (Install, Uninstall, Restart) may take: each bootout and kickstart under stopTimeout,
// Install's retry included. Callers give their context at least this.
const Deadline = 3 * time.Minute

// The launchctl timeouts (a test that kills a kickstart lowers stopTimeout). callTimeout is for the calls launchd
// answers at once (print, bootstrap); stopTimeout for the ones that wait for serve to exit (kickstart -k, bootout:
// launchd's ExitTimeOut, 20s by default, before it SIGKILLs serve — the flat 10s it once had killed launchctl
// mid-kickstart). settle is how long a verb reads launchd's state after a call that changes it, every pollEvery.
var (
	callTimeout = 10 * time.Second
	stopTimeout = 45 * time.Second
	settle      = 5 * time.Second
	pollEvery   = 250 * time.Millisecond
)

// ExecLaunchctl runs launchctl (from PATH) under ctx: its combined output. The verbs' every launchctl call goes through
// it, so a test answers for launchd in process and records the calls. A test replaces it (and puts it back).
var ExecLaunchctl = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
}

// launchctl runs launchctl (from PATH) under timeout; its error carries its output.
func launchctl(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := ExecLaunchctl(ctx, args...)
	if err != nil {
		msg := fmt.Sprintf("launchctl %s: %v", strings.Join(args, " "), err)
		if said := strings.TrimSpace(string(out)); said != "" {
			msg += ": " + said
		}
		return string(out), errors.New(msg)
	}
	return string(out), nil
}
