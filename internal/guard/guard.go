// Package guard is obor's Claude Code hooks: the Stop, SessionStart and UserPromptSubmit hooks (stop.go, session.go,
// prompt.go) and the PreToolUse hook (`obor hook pre-tool-use`, run through bin/obor): the event JSON in,
// allow (exit 0, no output) or deny (exit 2, one line "obor: …" on stderr). Bash commands are parsed with
// mvdan.cc/sh/v3/syntax (shell.go); a load run of tests (-count > 1, -race, two test commands at once) is denied for
// every role (T-543); writes — the editor tools and the Bash write targets alike (checkWrite, RV-83) — are checked
// against protected_files and the role table's write profile (a reviewer or deep_dive writes only under paths.reviews /
// paths.research, a researcher only under paths.research; a final symlink at its destination too), and an editor
// tool of a worker against its lane's boundary: no other worktree of this repo (the main checkout included), no other
// obor project, no final symlink out of the lane. A GitReadOnly role (reviewer, deep_dive, triage, advisor,
// researcher) runs git reads only (gitRead).
// Which files of its lane a worker edits is not checked (T-496: placement is checked at accept). An agent CLI run
// headless (codex exec, claude -p, opencode run) is denied for every role: agents run in herdr panes.
//
// It is an accident guard: it does not try to stop a crafted bypass (an interpreter, a script file, an alias, a
// variable set by an earlier call). The git pre-push hook is the backstop for pushes. Writes by an interpreter or a
// script (python, node, a script file) are not checked at all.
//
// Fails open: no project, no .obor/config.json, bad input or any internal error → allow. Only a matched rule denies.
// Fails closed on a .obor/config.json that exists but does not load (DD-v0.13 A1; an unknown key is ignored): every
// session but a lead (and a role-less one in the main checkout) is denied writes inside the repo's worktrees and git
// writes; git push, pkill/killall and headless agent runs stay denied for everyone, on the default config.
package guard

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
	"obor/internal/spec"
	"obor/internal/store"
)

// denial is raised (panic) by deny and caught by Run: a matched rule ends the check at once.
type denial struct{ reason string }

func deny(reason string) { panic(denial{reason}) }

// Run checks one hook call (the PreToolUse event JSON) in the environment env: 0 and "" to allow, 2 and the reason
// line (without "obor: ") to deny.
func Run(input []byte, env project.Env) (code int, reason string) {
	defer func() {
		if r := recover(); r != nil {
			if d, ok := r.(denial); ok {
				code, reason = 2, d.reason
				return
			}
			code, reason = 0, ""
		}
	}()
	g := load(input, env)
	if g == nil {
		return 0, ""
	}
	if g.tool == "Bash" {
		g.bash()
		g.recordBackground()
	} else {
		g.edit()
	}
	return 0, ""
}

// event is the part of the PreToolUse event the guard reads.
type event struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"session_id"`
	ToolInput struct {
		Command         string `json:"command"`
		FilePath        string `json:"file_path"`
		NotebookPath    string `json:"notebook_path"`
		RunInBackground bool   `json:"run_in_background"`
		Description     string `json:"description"`
	} `json:"tool_input"`
}

type guard struct {
	env                    project.Env
	tool, cwd, cmd, target string
	background             bool   // a Bash call run in the background (run_in_background)
	description, session   string // the Bash call's description, the hook's session id
	repo                   *project.Repo
	cfg                    *config.Config
	broken                 error // the config does not load (cfg is config.Fallback): fail closed (DD-v0.13 A1)
	root, top              string

	protected []string
	allowPush []string

	detected     project.Role // the role of the cwd (whose agent SelfAgent names)
	role, lane   string
	laneDir      string
	restricted   bool
	protectedWho string

	worktrees       []string
	worktreesLoaded bool

	unlocks     []string // the lane's protected-file unlocks (unlocked), read once
	unlocksRead bool
}

// unlocked is the paths the lane's task spec names under "Protected files this task may edit" (spec.Protected; the
// store's current rev, T-440), read once per hook call from the store's lanes and specs tables (read-only: serve may be
// down), so a change the lead sets applies at the next call. No row, no task, no spec, or a store that cannot be read:
// none.
func (g *guard) unlocked() []string {
	if g.unlocksRead {
		return g.unlocks
	}
	g.unlocksRead = true
	row, ok, err := store.ReadLaneAt(g.root, g.lane)
	if err != nil || !ok || row.Task == "" {
		return nil
	}
	if sp, ok, err := store.ReadSpecAt(g.root, row.Task); err == nil && ok {
		g.unlocks = spec.Protected(sp.Text)
	}
	return g.unlocks
}

// specUnlocks: p is inside this worker's lane and its stored spec names p's lane path under "Protected files this task
// may edit" (T-238: the spec is the lead's authorization for a protected file, and only the lead sets one; a glob
// unlocks nothing). T-241: a symlink (its target may be another lane's file) or a directory (its children are not
// listed) never unlocks.
func (g *guard) specUnlocks(p string) bool {
	if g.role != "worker" || g.laneDir == "" || !strings.HasPrefix(p, g.laneDir+"/") {
		return false
	}
	r := p[len(g.laneDir)+1:]
	for _, u := range g.unlocked() {
		if u == r {
			fi, err := os.Lstat(p)
			if err != nil {
				return os.IsNotExist(err)
			}
			return fi.Mode().IsRegular()
		}
	}
	return false
}

// load reads the event, the project and the role; nil = nothing to guard (allow).
func load(input []byte, env project.Env) *guard {
	var ev event
	if json.Unmarshal(input, &ev) != nil {
		return nil
	}
	switch ev.ToolName {
	case "Bash", "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return nil
	}
	g := &guard{env: env, tool: ev.ToolName, cwd: ev.Cwd, cmd: ev.ToolInput.Command, target: ev.ToolInput.FilePath,
		background: ev.ToolInput.RunInBackground, description: ev.ToolInput.Description, session: ev.SessionID}
	if g.target == "" {
		g.target = ev.ToolInput.NotebookPath
	}
	if g.cwd == "" {
		g.cwd, _ = os.Getwd()
	}
	if fi, err := os.Stat(g.cwd); err != nil || !fi.IsDir() {
		return nil
	}
	g.cwd = project.Physical(g.cwd)
	repo, cfg, _, err := loadAt(g.cwd)
	if repo == nil || errors.Is(err, config.ErrNoConfig) {
		return nil
	}
	g.broken = err
	g.repo, g.top, g.root, g.cfg = repo, repo.Top, repo.Root, cfg
	g.protected = cfg.Data.ProtectedFiles
	if cfg.Data.Guards != nil {
		g.allowPush = cfg.Data.Guards.AllowPush
	}
	g.detectRole()
	return g
}

// norm is a lexical, absolute path.
func norm(p string) string { return filepath.Clean("/" + p) }

// detectRole: the session's role (design "Roles"): project.DetectRole (a lane cwd makes a worker, whatever the env).
// protected_files bind every session but a lead and a role-less one in the main checkout.
func (g *guard) detectRole() {
	g.detected = project.DetectRole(g.repo, g.cfg, g.env)
	g.role, g.lane = g.detected.Role, g.detected.Lane
	if roles.LaneBound(g.role) { // a worker or the owner's drive agent (T-751): edits stay in its lane
		if g.detected.Source != "env" {
			g.laneDir = g.top
		} else if d := g.cfg.LanesDir() + "/" + g.lane; g.lane != "" && project.IsDir(d) {
			g.laneDir = project.Physical(d)
		}
	}

	g.restricted = g.role != "lead" && (g.role != "none" || g.top != g.root)
	g.protectedWho = "lead-only) — don't change it; ask the lead (final message or QUESTIONS.md)"
}

// phys is the physical absolute path of p (relative to base): the deepest existing directory resolved, the rest
// appended. ok false when it cannot be resolved.
func (g *guard) phys(p, base string) (string, bool) {
	switch {
	case strings.HasPrefix(p, "/"):
	case p == "~":
		p = g.env.Get("HOME")
	case strings.HasPrefix(p, "~/"):
		p = g.env.Get("HOME") + "/" + p[2:]
	default:
		p = base + "/" + p
	}
	if !strings.HasPrefix(p, "/") {
		return "", false
	}
	d, rest := norm(p), ""
	for {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			break
		}
		rest = "/" + filepath.Base(d) + rest
		d = filepath.Dir(d)
	}
	out := strings.TrimSuffix(project.Physical(d), "/") + rest
	if out == "" {
		out = "/"
	}
	return out, true
}

// loadWorktrees: the main checkout and every <common>/worktrees/*/gitdir (what `git worktree list` reads).
func (g *guard) loadWorktrees() {
	if g.worktreesLoaded {
		return
	}
	g.worktreesLoaded = true
	g.worktrees = []string{g.root}
	files, _ := filepath.Glob(filepath.Join(g.repo.Common, "worktrees", "*", "gitdir"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		line, _, _ := strings.Cut(string(b), "\n")
		if !strings.HasPrefix(line, "/") {
			line = filepath.Dir(f) + "/" + line // worktree.useRelativePaths
		}
		if d := strings.TrimSuffix(line, "/.git"); project.IsDir(d) {
			g.worktrees = append(g.worktrees, project.Physical(d))
		}
	}
}

// wtOf is the worktree of this repo containing p (longest match), or "".
func (g *guard) wtOf(p string) string {
	g.loadWorktrees()
	wt := ""
	for _, w := range g.worktrees {
		if (p == w || strings.HasPrefix(p, w+"/")) && len(w) > len(wt) {
			wt = w
		}
	}
	return wt
}

// rel is ${p#"$wt"/}.
func rel(p, wt string) string {
	if strings.HasPrefix(p, wt+"/") {
		return p[len(wt)+1:]
	}
	return p
}

// checkProtected denies when the physical path p is a protected file of its worktree. A symlink at p is also checked
// at its target.
func (g *guard) checkProtected(p string) {
	if g.broken != nil && g.restricted && g.wtOf(p) != "" {
		deny(unreadable(g.broken))
	}
	if !g.restricted || len(g.protected) == 0 {
		return
	}
	g.protectedAt(p)
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if t, err := filepath.EvalSymlinks(p); err == nil && t != p {
			g.protectedAt(t)
		}
	}
}

func (g *guard) protectedAt(p string) {
	wt := g.wtOf(p)
	if wt == "" {
		return
	}
	r := rel(p, wt)
	for _, glob := range g.protected {
		if spec.Match(r, glob) {
			if g.specUnlocks(p) {
				return
			}
			who := g.protectedWho
			if g.role == "worker" {
				who += "; a lead can unlock it for one task by naming the exact path under the spec's \"" +
					spec.ProtectedHeading + "\"; don't write it through a script or another tool"
			}
			deny(r + " is a protected file (protected_files in .obor/config.json; " + who)
		}
	}
}

// linkDest is the resolved destination of a final symlink at p: each readlink in the chain, then the parent directories
// resolved physically (a destination that does not exist yet keeps its last component). p itself when p is not a
// symlink; ok false for a loop or an unreadable link.
func (g *guard) linkDest(p string) (string, bool) {
	for i := 0; i < 64; i++ {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			return g.phys(p, g.cwd)
		}
		t, err := os.Readlink(p)
		if err != nil {
			return "", false
		}
		if !strings.HasPrefix(t, "/") {
			t = filepath.Dir(p) + "/" + t
		}
		p = norm(t)
	}
	return "", false
}

// checkLink: a selected path p that is a final symlink must resolve inside the worker's lane (T-372). phys resolves directory symlinks but leaves a final file symlink intact, so without this the worktree
// check above sees only the in-lane path and a link into another worktree (or anywhere else) passes.
func (g *guard) checkLink(p string) {
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	t, ok := g.linkDest(p)
	if !ok {
		deny(p + " is a symlink that cannot be resolved — a " + g.role + " edits only inside its lane " + g.laneDir)
	}
	if t == p || strings.HasPrefix(t, g.laneDir+"/") {
		return
	}
	where := ""
	if wt := g.wtOf(t); wt != "" {
		where = " in another worktree of this repo (" + wt + ")"
	}
	deny(p + " is a symlink to " + t + where + " — a " + g.role + " edits only inside its lane " + g.laneDir)
}

// checkWriteArg: checkWrite for a path argument (a write target) of a Bash command run in dir. Read arguments are not
// passed here.
func (g *guard) checkWriteArg(arg, dir string) {
	if !g.restricted || arg == "" {
		return
	}
	if p, ok := g.phys(arg, dir); ok {
		g.checkWrite(p)
	}
}

// writeProfile is the role table's write profile of this session's role.
func (g *guard) writeProfile() roles.WriteProfile {
	if r, ok := roles.Lookup(g.role); ok {
		return r.Write
	}
	return ""
}

// profiled: the write profile limits where the role writes (reviews / research).
func profiled(wp roles.WriteProfile) bool {
	return wp == roles.WriteReviewsOnly || wp == roles.WriteResearchOnly
}

// checkWrite is the one path policy of a write to the physical path p, used by every write route the guard sees (the
// editor tools and the Bash write targets: redirections, tee, cp, mv, rm, sed -i, git checkout/restore paths; RV-83):
// protected files, then the role's write profile, at p and — when p is a final symlink (phys leaves it intact) — at its
// destination; a link that cannot be resolved is denied for a profiled role (RV-83 #3). Writes by an interpreter
// (python -c, node -e) or a script file are not seen at all: an accident guard, not a sandbox.
func (g *guard) checkWrite(p string) {
	g.checkProtected(p)
	wp := g.writeProfile()
	if !profiled(wp) {
		return
	}
	if why := g.profileDenial(wp, p); why != "" {
		deny(why)
	}
	if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	t, ok := g.linkDest(p)
	if !ok {
		deny(p + " is a symlink that cannot be resolved — a " + g.role + " writes only through paths it can resolve")
	}
	if why := g.profileDenial(wp, t); why != "" {
		deny(p + " is a symlink to " + t + ": " + why)
	}
}

// profileDenial is the deny reason of the write profile wp for the physical path p, "" when allowed. Inside this repo's
// worktrees a reviewer / deep_dive writes only under paths.reviews (default docs/reviews) and paths.research, a
// researcher only under paths.research (default docs/research). Outside the repo (scratch, /tmp) is not limited.
func (g *guard) profileDenial(wp roles.WriteProfile, p string) string {
	wt := g.wtOf(p)
	if wt == "" {
		return ""
	}
	r := rel(p, wt)
	paths := g.cfg.Paths()
	research := strings.Trim(paths.Get("research"), "/")
	var dirs []string
	instead := "report the change as a finding instead"
	switch wp {
	case roles.WriteReviewsOnly:
		reviews := strings.Trim(paths.Reviews, "/")
		if reviews == "" {
			reviews = "docs/reviews"
		}
		dirs = append(dirs, reviews)
		if research != "" {
			dirs = append(dirs, research)
		}
	case roles.WriteResearchOnly:
		if research == "" {
			research = "docs/research"
		}
		dirs = append(dirs, research)
		instead = "report the change in the findings doc instead"
	default:
		return ""
	}
	for _, d := range dirs {
		if strings.HasPrefix(r, d+"/") {
			return ""
		}
	}
	return r + " is outside the " + g.role + " write profile — a " + g.role + " writes only under " +
		strings.Join(dirs, ", ") + "; " + instead
}

// edit guards Edit / Write / MultiEdit / NotebookEdit.
func (g *guard) edit() {
	if !g.restricted && !profiled(g.writeProfile()) {
		return
	}
	if g.target == "" {
		return
	}
	p, ok := g.phys(g.target, g.cwd)
	if !ok {
		return
	}
	g.checkWrite(p)
	if !roles.LaneBound(g.role) || g.laneDir == "" {
		return
	}
	if !strings.HasPrefix(p, g.laneDir+"/") {
		if wt := g.wtOf(p); wt != "" {
			deny(p + " is in another worktree of this repo (" + wt + ") — a " + g.role + " edits only inside its lane " + g.laneDir)
		}
		if root := otherProject(p); root != "" {
			deny(p + " is in another obor project (" + root + ") — a " + g.role + " edits only inside its lane " + g.laneDir)
		}
		return
	}
	g.checkLink(p)
}

// otherProject is the nearest directory above p with a .obor/config.json: another obor project's checkout (the
// caller has ruled out this repo's worktrees); "" when there is none (scratch, /tmp).
func otherProject(p string) string {
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".obor", "config.json")); err == nil {
			return d
		}
		if d == "/" || d == "." {
			return ""
		}
	}
}

// bash guards a Bash command (shell.go).
func (g *guard) bash() { g.script(g.cmd, g.cwd, 0) }

// recordBackground records an allowed Bash call run in the background (T-714): its description and command with the
// agent, pane and session, so obor status, monitor and web show the live process under its description
// (project.AppendBackground: one append, no wait).
func (g *guard) recordBackground() {
	if !g.background || strings.TrimSpace(g.cmd) == "" {
		return
	}
	project.AppendBackground(g.root, project.BackgroundCall{At: time.Now().Unix(), Agent: sessionAgent(g.cfg, g.detected, g.env),
		Pane: g.env.Get("HERDR_PANE_ID"), Session: g.session, Description: g.description, Command: g.cmd})
}
