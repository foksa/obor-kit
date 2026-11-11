// Package service is the launchd adapter (phase 7 advice §2): one LaunchAgent per project runs its obor
// serve, so serve outlives the pane (and herdr) it was typed in. Darwin only; elsewhere every verb says to run
// obor serve yourself.
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

// Service is one project's LaunchAgent.
type Service struct {
	Name  string // the project's config name: the label is obor.serve.<Name>
	Root  string // the project's main checkout: the agent's WorkingDirectory
	Kit   string // the obor kit: the agent runs <Kit>/bin/obor serve
	Home  string // the user's home: the plist is in <Home>/Library/LaunchAgents
	UID   int    // the gui/<uid> domain
	GOOS  string // runtime.GOOS: launchd only on darwin
	Path  string // the caller's PATH (launchd's own has no claude, codex, herdr or jq)
	Herdr string // HERDR_SOCKET_PATH ("": unset, not written)
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

// Plist is the agent's plist: obor serve in the project, started at load, kept alive, its output in LogPath, the
// caller's PATH (and HERDR_SOCKET_PATH when set).
func (s Service) Plist() []byte {
	var b bytes.Buffer
	str := func(indent, v string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, esc(v)) }
	key := func(indent, k string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, k) }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key("\t", "Label")
	str("\t", s.Label())
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	str("\t\t", filepath.Join(s.Kit, "bin", "obor"))
	str("\t\t", "serve")
	b.WriteString("\t</array>\n")
	key("\t", "WorkingDirectory")
	str("\t", s.Root)
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "KeepAlive")
	b.WriteString("\t<true/>\n")
	key("\t", "StandardOutPath")
	str("\t", s.LogPath())
	key("\t", "StandardErrorPath")
	str("\t", s.LogPath())
	key("\t", "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	key("\t\t", "PATH")
	str("\t\t", s.Path)
	if s.Herdr != "" {
		key("\t\t", "HERDR_SOCKET_PATH")
		str("\t\t", s.Herdr)
	}
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return b.Bytes()
}

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
	b, err := os.ReadFile(s.PlistPath())
	if err != nil || !bytes.Equal(b, s.Plist()) {
		return false
	}
	_, loaded := s.PID(ctx)
	return loaded
}

// Install writes the plist and loads it (bootstrap; one loaded before is booted out first, so a reinstall takes the
// new plist). A bootstrap launchd refuses — the label it still knows, or half knows while the old one is torn down
// ("Bootstrap failed: 5: Input/output error" left the label gone) — is booted out and bootstrapped once more;
// an install that ends with the label not loaded is a FAIL naming the label's state and the next step. FAIL when the
// label is another project's (owner).
func (s Service) Install(ctx context.Context) (string, error) {
	if !s.Supported() {
		return Elsewhere, nil
	}
	if s.Name == "" {
		return "", errors.New("the project has no name")
	}
	plist := s.PlistPath()
	_, loaded, err := s.owner(ctx)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{filepath.Dir(plist), filepath.Dir(s.LogPath())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	if err := s.rotateLog(); err != nil {
		return "", err
	}
	if err := os.WriteFile(plist, s.Plist(), 0o644); err != nil {
		return "", err
	}
	if loaded {
		if err := s.bootout(ctx); err != nil {
			return "", err
		}
	}
	domain := fmt.Sprintf("gui/%d", s.UID)
	_, err = launchctl(ctx, callTimeout, "bootstrap", domain, plist)
	if err != nil {
		_ = s.bootout(ctx) // not loaded at all: its error is the expected one
		if _, again := launchctl(ctx, callTimeout, "bootstrap", domain, plist); again != nil {
			err = fmt.Errorf("%v; after a bootout, again: %v", err, again)
		} else {
			err = nil
		}
	}
	pid, loaded := s.PID(ctx)
	switch {
	case err != nil && loaded:
		return "", fmt.Errorf("%v: %s is loaded (pid %d), maybe from the plist before this one: obor serve install again", err, s.Label(), pid)
	case err != nil:
		return "", fmt.Errorf("%v: %s is not loaded in launchd: obor serve install again (launchctl print %s shows launchd's view)", err, s.Label(), s.target())
	case !loaded:
		return "", fmt.Errorf("launchctl bootstrap %s %s returned, but %s is not loaded in launchd: obor serve install again (log %s)",
			domain, plist, s.Label(), s.LogPath())
	}
	return fmt.Sprintf("INSTALLED %s (%s; log %s)", s.Label(), plist, s.LogPath()), nil
}

// bootout unloads the label (launchd stops its serve first: up to its ExitTimeOut) and waits, up to settle, until
// launchd no longer knows it, so a bootstrap after it does not meet the label half torn down.
func (s Service) bootout(ctx context.Context) error {
	_, err := launchctl(ctx, stopTimeout, "bootout", s.target())
	s.poll(ctx, func(_ int, loaded bool) bool { return !loaded })
	return err
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
		if err := s.bootout(ctx); err != nil {
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
	until := time.Now().Add(settle)
	for {
		pid, loaded := s.PID(ctx)
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
	if !s.Supported() {
		return 0, false
	}
	out, err := launchctl(ctx, callTimeout, "print", s.target())
	if err != nil {
		return 0, false
	}
	if m := pidLine.FindStringSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	return pid, true
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

// execLaunchctl runs launchctl (from PATH) under ctx: its combined output. The verbs' every launchctl call goes through
// it, so a test answers for launchd in process and records the calls. A test replaces it.
var execLaunchctl = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
}

// SetLaunchctl replaces the launchctl call with f (a test of another package answers for launchd in process); it
// returns the call it replaced. The default runs launchctl from PATH.
func SetLaunchctl(f func(ctx context.Context, args ...string) ([]byte, error)) (restore func()) {
	was := execLaunchctl
	execLaunchctl = f
	return func() { execLaunchctl = was }
}

// launchctl runs launchctl (from PATH) under timeout; its error carries its output.
func launchctl(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := execLaunchctl(ctx, args...)
	if err != nil {
		msg := fmt.Sprintf("launchctl %s: %v", strings.Join(args, " "), err)
		if said := strings.TrimSpace(string(out)); said != "" {
			msg += ": " + said
		}
		return string(out), errors.New(msg)
	}
	return string(out), nil
}
