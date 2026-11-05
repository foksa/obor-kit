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

// Git runs git -C dir args… and returns its stdout (also on failure, as $(…) would capture it); a failure's error
// carries git's stderr, the reason a read failed (T-871).
func Git(dir string, args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return string(out), fmt.Errorf("%w: %s", err, msg)
		}
	}
	return string(out), err
}

// StateDir is <root>/.obor/state (not created: readers never write).
func StateDir(root string) string { return filepath.Join(root, ".obor", "state") }

// Env looks up an environment variable (os.LookupEnv's shape): the process environment in the binary, a map in
// tests. Commands read the environment only through it, so tests can run in parallel.
type Env func(key string) (string, bool)

// OSEnv is the process environment.
var OSEnv Env = os.LookupEnv

// Get is the value of key, "" when unset.
func (e Env) Get(key string) string {
	v, _ := e(key)
	return v
}

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
// lanes_dir (not the merge scratch) → a worker in that lane. The lane cwd wins over the env, except OBOR_ROLE=driver
// there: the owner's drive agent (T-751).
func DetectRole(r *Repo, cfg *config.Config, env Env) Role {
	envRole, envLane := env.Get("OBOR_ROLE"), env.Get("OBOR_LANE")
	cwdLane := LaneOf(r, cfg)
	switch {
	case envRole == roles.Driver && cwdLane != "": // the owner's drive agent in its lane (T-751)
		return Role{roles.Driver, cwdLane, "env+cwd"}
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
// merge scratch and not a role worktree (T-853: the reviewer's, the deep dive's). "" when none (T-777: a lane is any
// other worktree there).
func LaneOf(r *Repo, cfg *config.Config) string {
	if r == nil || cfg == nil || !r.Linked() {
		return ""
	}
	if filepath.Dir(r.Top) != Physical(cfg.LanesDir()) || r.Top == Physical(cfg.ScratchDir()) {
		return ""
	}
	for _, d := range cfg.RoleWorktrees() {
		if r.Top == Physical(d) {
			return ""
		}
	}
	return filepath.Base(r.Top)
}

// SelfAgent is the herdr agent name of a session in role (DetectRole: OBOR_ROLE, its lane's cwd winning): the name
// config.Self derives from the role's slot and the current config (T-786). "" when none (no role, the owner). An
// OBOR_AGENT in the environment is never read: a stored copy went stale on every rename.
func SelfAgent(cfg *config.Config, role Role) string {
	if cfg == nil {
		return ""
	}
	return cfg.Self(role.Role, role.Lane)
}

// Self is the session's own agent name: SelfAgent of DetectRole of its repo r (nil: no repo) and env. Every "who am
// I" (hooks, the MCP server, CLI calls to serve) is this one call.
func Self(r *Repo, cfg *config.Config, env Env) string {
	return SelfAgent(cfg, DetectRole(r, cfg, env))
}
