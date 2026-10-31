// Package service is the launchd adapter (T-596, phase 7 advice §2): one LaunchAgent per project runs its obor
// serve, so serve outlives the pane (and herdr) it was typed in. Darwin only; elsewhere every verb says to run
// obor serve yourself.
package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
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

// LogPath is where the agent's serve writes (stdout and stderr): .obor/state/logs/serve.log, by the check logs.
func (s Service) LogPath() string {
	return filepath.Join(s.Root, ".obor", "state", "logs", "serve.log")
}

// MaxLog is the size over which Install and Restart move serve.log aside.
const MaxLog = 5 << 20

// rotateLog moves serve.log to serve.log.1 (replacing an old one) when it is over MaxLog: launchd opens the log path
// at each spawn, so serve cannot rotate the file it was given. Missing or small: nothing. Called before an agent is
// started (Install, Restart); a crash respawn by launchd is not rotated.
func (s Service) rotateLog() error {
	log := s.LogPath()
	fi, err := os.Stat(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Size() <= MaxLog {
		return nil
	}
	return os.Rename(log, log+".1")
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

// owner is the check all three verbs run before they touch launchd (T-607): the label's plist, when there is one,
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
// new plist). FAIL when the label is another project's (owner).
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
		if _, err := launchctl(ctx, "bootout", s.target()); err != nil {
			return "", err
		}
	}
	if _, err := launchctl(ctx, "bootstrap", fmt.Sprintf("gui/%d", s.UID), plist); err != nil {
		return "", err
	}
	return fmt.Sprintf("INSTALLED %s (%s; log %s)", s.Label(), plist, s.LogPath()), nil
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
		if _, err := launchctl(ctx, "bootout", s.target()); err != nil {
			return "", err
		}
	}
	if err := os.Remove(plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "UNINSTALLED " + s.Label(), nil
}

// Restart kills the agent's serve and starts it again (kickstart -k): a new binary takes over. Another project's
// label (owner): FAIL, nothing done.
func (s Service) Restart(ctx context.Context) (string, error) {
	if !s.Supported() {
		return Elsewhere, nil
	}
	_, loaded, err := s.owner(ctx)
	if err != nil {
		return "", err
	}
	if !loaded {
		return "", fmt.Errorf("%s is not installed (obor serve install)", s.Label())
	}
	if err := s.rotateLog(); err != nil {
		return "", err
	}
	if _, err := launchctl(ctx, "kickstart", "-k", s.target()); err != nil {
		return "", err
	}
	return "RESTARTED " + s.Label(), nil
}

var pidLine = regexp.MustCompile(`(?m)^\s*pid = (\d+)\s*$`)

// PID is the agent's process (launchctl print): loaded says launchd knows the label (print exits 113 for one it
// does not), pid its running process (0: none now). Off darwin: not loaded.
func (s Service) PID(ctx context.Context) (pid int, loaded bool) {
	if !s.Supported() {
		return 0, false
	}
	out, err := launchctl(ctx, "print", s.target())
	if err != nil {
		return 0, false
	}
	if m := pidLine.FindStringSubmatch(out); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	return pid, true
}

// launchctl runs launchctl (from PATH) under a timeout; its error carries its output.
func launchctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("launchctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
