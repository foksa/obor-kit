// Package project finds the project around a directory (git toplevel, main checkout = root), the session's role and
// agent, the lane agents (from the store's lanes table) and the operations registry.
package project

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/roles"
)

// StopFailureLog is the project's StopFailure hook log under StateDir.
const StopFailureLog = "stop-failure.log"

// ErrNotGit: dir is not inside a git repository.
var ErrNotGit = errors.New("not a git repository")

// Fail is a refusal or failure: a command's last line is "FAIL <msg>".
type Fail string

// Error is the FAIL text.
func (f Fail) Error() string { return string(f) }

// Failf is a Fail of a formatted message.
func Failf(format string, a ...any) error { return Fail(fmt.Sprintf(format, a...)) }

// Repo is what one `git rev-parse` says about a directory; paths are absolute and physical.
type Repo struct {
	Top    string // root of the worktree holding the directory
	GitDir string
	Common string
	Root   string // main checkout: dirname of the common dir
}

// Linked reports whether the directory is in a linked worktree (not the main checkout).
func (r *Repo) Linked() bool { return r.GitDir != r.Common }

// Find reads .git and commondir files where possible, then falls back to one git rev-parse.
func Find(dir string) (*Repo, error) {
	if r, err := findFromGitFiles(dir); err == nil {
		return r, nil
	}
	out, err := Git(dir, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	if err != nil {
		return nil, ErrNotGit
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 3 {
		return nil, ErrNotGit
	}
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return Physical(p)
	}
	r := &Repo{Top: Physical(lines[0]), GitDir: abs(lines[1]), Common: abs(lines[2])}
	r.Root = filepath.Dir(r.Common)
	return r, nil
}

func findFromGitFiles(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrNotGit
	}
	cur := Physical(abs)
	for {
		marker := filepath.Join(cur, ".git")
		fi, statErr := os.Stat(marker)
		if statErr == nil {
			gitDir := marker
			if !fi.IsDir() {
				b, readErr := os.ReadFile(marker)
				line := strings.TrimSpace(string(b))
				if readErr != nil || !strings.HasPrefix(line, "gitdir:") {
					return nil, ErrNotGit
				}
				gitDir = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(cur, gitDir)
				}
			}
			gitDir = Physical(gitDir)
			common := gitDir
			if b, readErr := os.ReadFile(filepath.Join(gitDir, "commondir")); readErr == nil {
				common = strings.TrimSpace(string(b))
				if common == "" {
					return nil, ErrNotGit
				}
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				common = Physical(common)
			}
			return &Repo{Top: cur, GitDir: gitDir, Common: common, Root: filepath.Dir(common)}, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return nil, ErrNotGit
		}
		cur = parent
	}
}

// Physical is `cd p && pwd -P` (p unchanged when it does not resolve).
func Physical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Git runs git -C dir args… and returns its stdout (also on failure, as $(…) would capture it).
func Git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = nil
	out, err := cmd.Output()
	return string(out), err
}

// StateDir is <root>/.obor/state (not created: readers never write).
func StateDir(root string) string { return filepath.Join(root, ".obor", "state") }

// Env looks up an environment variable (os.LookupEnv's shape): the process environment in the binary, a map in
// tests. Commands read the environment only through it, so tests can run in parallel.
type Env func(key string) (string, bool)

// OSEnv is the process environment.
var OSEnv Env = os.LookupEnv

// Get is the value of key, "" when unset. OBOR_ROLE, OBOR_AGENT, OBOR_LANE and OBOR_STATE_HOME fall back to their
// OBOR_ name: a pane split before the rename keeps that environment until it is restarted.
func (e Env) Get(key string) string {
	v, _ := e(key)
	if v == "" && herdEnv[key] { // until v0.17 (P7-0a fallback)
		v, _ = e("HERD_" + strings.TrimPrefix(key, "OBOR_"))
	}
	return v
}

// herdEnv are the variables Get reads under their old name too. // until v0.17 (P7-0a fallback)
var herdEnv = map[string]bool{"OBOR_ROLE": true, "OBOR_AGENT": true, "OBOR_LANE": true, "OBOR_STATE_HOME": true}

// Seconds is a whole number of seconds s (def when s is ""); ok false, with def, when s is not one.
func Seconds(s string, def int) (time.Duration, bool) {
	if s == "" {
		return time.Duration(def) * time.Second, true
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || strings.HasPrefix(s, "+") {
		return time.Duration(def) * time.Second, false
	}
	return time.Duration(n) * time.Second, true
}

// Seconds is Seconds of the variable key.
func (e Env) Seconds(key string, def int) (time.Duration, bool) { return Seconds(e.Get(key), def) }

// StateHome is obor's machine-wide state dir: ${OBOR_STATE_HOME:-$HOME/.obor}.
func StateHome(env Env) string {
	if h := env.Get("OBOR_STATE_HOME"); h != "" {
		return h
	}
	return filepath.Join(env.Get("HOME"), ".obor")
}

// KitRoot is the obor kit this binary belongs to: OBOR_PLUGIN_ROOT (bin/obor, scripts and the hook wrapper export
// it), else the directory above the binary's (<kit>/dist/obor).
func KitRoot(env Env) string {
	if r := env.Get("OBOR_PLUGIN_ROOT"); r != "" {
		return r
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(Physical(exe)))
}

// Role is a session's role and lane, and where they came from (env, cwd, env+cwd, none).
type Role struct {
	Role, Lane, Source string
}

// DetectRole is the session's role: OBOR_ROLE / OBOR_LANE from env; a directory in a linked worktree directly under
// lanes_dir (not the merge scratch, nor a role's worktree) → a worker in that lane. The lane cwd wins over the env.
func DetectRole(r *Repo, cfg *config.Config, env Env) Role {
	envRole, envLane := env.Get("OBOR_ROLE"), env.Get("OBOR_LANE")
	cwdLane := LaneOf(r, cfg)
	switch {
	case envRole != "" && cwdLane != "":
		return Role{"worker", cwdLane, "env+cwd"}
	case envRole != "":
		if role, ok := roles.LookupEnv(envRole); ok {
			return Role{role.Name, envLane, "env"}
		}
		return Role{envRole, envLane, "env"}
	case cwdLane != "":
		return Role{"worker", cwdLane, "cwd"}
	}
	return Role{"none", "", "none"}
}

// LaneOf is the lane whose worktree r is: a linked worktree directly under lanes_dir (compared physically), not the
// merge scratch, and not a role's worktree unless the config names it a lane. "" when none.
func LaneOf(r *Repo, cfg *config.Config) string {
	if r == nil || cfg == nil || !r.Linked() {
		return ""
	}
	scratch := cfg.Data.Merge.ScratchDir
	if filepath.Dir(r.Top) != Physical(cfg.LanesDir()) || r.Top == Physical(config.AbsUnder(cfg.Root, scratch)) {
		return ""
	}
	name := filepath.Base(r.Top)
	if Reserved(cfg, name) {
		return ""
	}
	return name
}

// Reserved reports whether name is a role worktree (a configured roles.<role>.worktree or a configured reviewer /
// deep_dive's default) and not an explicit lane: a linked worktree of that name under lanes_dir is no lane.
func Reserved(cfg *config.Config, name string) bool {
	if cfg.HasLane(name) {
		return false
	}
	for _, wt := range cfg.RoleWorktrees() {
		if wt == name {
			return true
		}
	}
	return false
}

// SelfAgent is the herdr agent name of a session in role (DetectRole of its cwd): OBOR_AGENT; else the lane row's
// agent; else the obor setup name (<prefix>-<lane lower>), or for a role config.AgentName (roles.<role>.agent or
// the setup name). "" when none.
func SelfAgent(cfg *config.Config, role Role, env Env) string {
	if a := env.Get("OBOR_AGENT"); a != "" {
		return a
	}
	switch {
	case role.Lane != "":
		if agent := LaneAgent(cfg.Root, role.Lane); agent != "" {
			return agent
		}
		worker, _ := roles.Lookup("worker")
		return worker.AgentName(cfg.Data.Prefix, role.Lane, "")
	case role.Role == "lead", role.Role == "integrator", role.Role == "reviewer", role.Role == "deep_dive":
		return cfg.AgentName(role.Role, "")
	}
	return ""
}
