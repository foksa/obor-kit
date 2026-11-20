package setup

// obor uninstall: undo what obor init set up in a project, in reverse order, keeping the config and the store.
// Every step is idempotent: a second run reports each one "already". The summary lists each step as done, already,
// skipped (why) or kept: <why>, like obor init's.
//
//	refusals   before anything changes: serve not answering while anything is set up (or with --purge), so the open
//	           tasks are unknown (--force goes on); an open or waiting task (--force: as obor stop); a lane or role
//	           worktree that is dirty; a task/* branch serve's prune does not list as merged (--discard removes
//	           those too). Nothing is changed when one stands, --dry-run included.
//	agents     the project's agents stopped (obor stop's path).
//	serve      serve ended and its launchd agent uninstalled, obor.web with the machine's last one.
//	panes      the terminal's panes (and the tabs they leave empty) of the project's agents closed; never another
//	           project's, and never the pane this command runs in.
//	worktrees  every worktree directly under lanes.dir (lanes, role worktrees, merge, merge-nightly) removed, and the
//	           registration of each whose directory is gone: only those paths.
//	branches   task/* branches deleted (merged ones; with --discard the rest) with their tip sha, and obor's refs
//	           under refs/obor/.
//	githooks   obor setup githooks --remove (a core.hooksPath that is not obor's stays).
//	plugin     the project-scope plugin uninstalled when .claude/settings.json is untracked (a tracked one stays).
//	plugin-links  the links of .claude/settings.json went with the worktrees.
//	projects   the project out of the machine's list.
//	stopped    obor stop's marker removed, so obor init runs again.
//	config     kept: .obor/config.json and every committed file.
//	state      kept: .obor/state; --purge removes it and the other gitignored obor files.
//
// --dry-run prints the same list and changes nothing.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
)

// UninstallUsage is obor uninstall's usage line.
const UninstallUsage = "usage: obor uninstall [--dry-run] [--discard] [--purge] [--force]"

// Teardown is what obor uninstall asks of the running project: its serve, its agents and the main repo's git.
// cmd/obor passes the service, serve and git adapters (setup does not import them). Each func gets the project's config.
type Teardown struct {
	// Open is the open or waiting tasks, one line ("" none); known false: serve does not answer for the project.
	Open func(ctx context.Context, cfg *config.Config) (tasks string, known bool)
	// Merged is the task branches whose task is merged or landed, checked out or not, from serve's structured prune
	// reply; an error: none known.
	Merged func(ctx context.Context, cfg *config.Config) ([]port.PruneBranch, error)
	// Agents stops the project's agents but the caller's own (force: a working one too). With dry it stops none.
	Agents func(ctx context.Context, cfg *config.Config, force, dry bool) (state, line string, err error)
	// Serve ends the project's serve and uninstalls its launchd agent, and obor.web with the machine's last one.
	// With dry it does neither.
	Serve func(ctx context.Context, cfg *config.Config, dry bool) (state, line string, err error)
	// Pruner is the main repo's git: its task branches and worktrees.
	Pruner func(cfg *config.Config) port.Pruner
}

// uninstallOpts are obor uninstall's flags.
type uninstallOpts struct{ dry, discard, purge, force bool }

// ustep is one line of the summary and the detail lines under it.
type ustep struct {
	name, state, what string
	lines             []string
}

func (s *Setup) oborUninstall(ctx context.Context, args []string) error {
	var o uninstallOpts
	for _, a := range args {
		switch a {
		case "--dry-run":
			o.dry = true
		case "--discard":
			o.discard = true
		case "--purge":
			o.purge = true
		case "--force":
			o.force = true
		default:
			return errUsage
		}
	}
	if s.Down == nil || s.Down.Pruner == nil {
		return project.Failf("obor uninstall has no serve or git adapter")
	}
	if err := s.load(); err != nil {
		return err
	}
	repo, _ := s.git().Find(s.Dir)
	if r := project.DetectRole(repo, s.cfg, s.Env); r.Role == "worker" {
		return project.Failf("obor uninstall is the owner's or the lead's, not a worker's: run it in %s", s.root)
	}
	g := s.Down.Pruner(s.cfg)
	plan, err := s.uninstallPlan(ctx, g, o)
	if err != nil {
		return err
	}
	if len(plan.refusals) > 0 {
		return project.Failf("obor uninstall refused, nothing changed:\n  %s", strings.Join(plan.refusals, "\n  "))
	}

	var steps []ustep
	add := func(name, state, format string, a ...any) *ustep {
		steps = append(steps, ustep{name: name, state: state, what: fmt.Sprintf(format, a...)})
		return &steps[len(steps)-1]
	}

	state, line, err := s.Down.Agents(ctx, s.cfg, o.force, o.dry)
	if err != nil {
		return project.Failf("agents: %v", err)
	}
	add("agents", state, "%s", line)
	state, line, err = s.Down.Serve(ctx, s.cfg, o.dry)
	if err != nil {
		return project.Failf("serve: %v", err)
	}
	add("serve", state, "%s", line)

	if err := s.uninstallPanes(ctx, o, add); err != nil {
		return err
	}
	if err := s.uninstallTrees(ctx, g, plan, o, add); err != nil {
		return err
	}
	if err := s.uninstallBranches(ctx, g, plan, o, add); err != nil {
		return err
	}
	if err := s.uninstallHooks(o, add); err != nil {
		return err
	}
	if err := s.uninstallPlugin(ctx, plan, o, add); err != nil {
		return err
	}
	if err := s.uninstallRegistration(o, add); err != nil {
		return err
	}
	s.uninstallFiles(o, add)

	s.say("")
	s.say("obor uninstall %s:", s.cfg.Name())
	for _, st := range steps {
		if st.state == "kept" {
			s.say("  %-13s kept: %s", st.name, st.what)
		} else {
			s.say("  %-13s %-8s %s", st.name, st.state, st.what)
		}
		for _, l := range st.lines {
			s.say("      %s", l)
		}
	}
	if o.dry {
		s.say("OK uninstall %s (dry run: nothing changed)", s.cfg.Name())
	} else {
		s.say("OK uninstall %s; obor init sets it up again from the kept config", s.cfg.Name())
	}
	return nil
}

// uninstallPlan is what the refusals found and the steps then act on.
type uninstallPlan struct {
	refusals []string
	trees    []port.PruneTree // worktrees directly under lanes.dir, with a directory
	gone     []port.PruneTree // registered ones directly under lanes.dir whose directory is gone
	branches []port.PruneBranch
	merged   map[string]bool // task/* branches whose task serve says is merged
	held     map[string]bool // branches checked out outside the worktrees uninstall removes (the main checkout's or a worktree outside lanes.dir)
	unmerged map[string]bool // task/* branches whose task serve does not say is merged
	links    bool            // a worktree holds the plugin's settings link
}

// uninstallPlan reads the project and lists everything that makes obor uninstall refuse. It changes nothing.
func (s *Setup) uninstallPlan(ctx context.Context, g port.Pruner, o uninstallOpts) (*uninstallPlan, error) {
	p := &uninstallPlan{merged: map[string]bool{}, held: map[string]bool{}, unmerged: map[string]bool{}}
	tasks, known := s.Down.Open(ctx, s.cfg)
	if known && tasks != "" && !o.force {
		p.refusals = append(p.refusals, "tasks in progress: "+tasks+": finish or drop them, or obor uninstall --force "+
			"(stops the agents; the tasks stay in the store)")
	}
	trees, err := g.Trees(ctx)
	if err != nil {
		return nil, project.Failf("git worktrees not listed: %v", err)
	}
	lanes := project.Physical(s.cfg.LanesDir())
	for _, t := range trees {
		if project.Physical(filepath.Dir(t.Dir)) != lanes {
			continue
		}
		if t.Gone {
			p.gone = append(p.gone, t)
			continue
		}
		p.trees = append(p.trees, t)
		if _, err := os.Lstat(filepath.Join(t.Dir, project.PluginSettings)); err == nil {
			p.links = true
		}
		if o.discard {
			continue
		}
		dirt, err := g.Dirt(ctx, t.Dir)
		if err != nil {
			return nil, project.Failf("worktree %s not read: %v", t.Dir, err)
		}
		if dirt != "" {
			p.refusals = append(p.refusals, fmt.Sprintf("worktree %s: %s (--discard removes it anyway)", t.Dir, dirt))
		}
	}
	if !known && !o.force && (o.purge || s.setUp(ctx, p)) {
		return nil, project.Failf("serve is not running, so open tasks cannot be checked: obor start, then obor uninstall " +
			"(or --force to go ahead)")
	}
	if p.branches, err = g.Branches(ctx); err != nil {
		return nil, project.Failf("git branches not listed: %v", err)
	}
	mergedErr := errors.New("serve is not running")
	if known { // a serve that does not answer would be retried to its deadline
		var merged []port.PruneBranch
		if merged, mergedErr = s.Down.Merged(ctx, s.cfg); mergedErr == nil {
			for _, b := range merged {
				p.merged[b.Name] = true
			}
		}
	}
	checked, err := g.CheckedOut(ctx)
	if err != nil {
		return nil, project.Failf("git worktrees not listed: %v", err)
	}
	inTrees := map[string]bool{}
	for _, t := range p.trees {
		inTrees[t.Branch] = true
	}
	for _, b := range p.branches {
		if checked[b.Name] && !inTrees[b.Name] {
			p.held[b.Name] = true
		}
		if p.merged[b.Name] || p.held[b.Name] {
			continue
		}
		p.unmerged[b.Name] = true
		if o.discard {
			continue
		}
		why := "serve's prune does not list it as merged"
		if mergedErr != nil {
			why = fmt.Sprintf("serve's prune could not say if it is merged (%v)", mergedErr)
		}
		p.refusals = append(p.refusals, fmt.Sprintf("branch %s (%s): %s (--discard deletes it)", b.Name, short(b.SHA), why))
	}
	slices.Sort(p.refusals)
	return p, nil
}

// setUp: something obor init set up is still there: a worktree under lanes.dir, the githooks, serve's job. A project
// with none of it has nothing to tear down, so serve not answering costs it nothing.
func (s *Setup) setUp(ctx context.Context, p *uninstallPlan) bool {
	if state, _ := s.hooksState(); len(p.trees)+len(p.gone) > 0 || state == "obor" {
		return true
	}
	state, _, err := s.Down.Serve(ctx, s.cfg, true)
	return err != nil || state != "already"
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// uninstallPanes closes the project's panes in the terminal, but the one this command runs in.
func (s *Setup) uninstallPanes(ctx context.Context, o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	own, _ := project.PaneOf(s.cfg, s.Env) // none: the caller runs in no pane
	repo, _ := s.git().Find(s.Dir)
	ownAgent := project.Self(repo, s.cfg, s.Env)
	spared := ""
	ours := func(p port.Pane) bool {
		if p.ID == own || ownAgent != "" && p.Label == ownAgent {
			if p.Label != "" && s.projectPane(p) {
				spared = p.ID
			}
			return false
		}
		return s.projectPane(p)
	}
	lines, err := s.T.CloseProject(ctx, port.CloseProject{Name: s.cfg.Name(), Ours: ours, Dry: o.dry})
	switch {
	case err != nil:
		add("panes", "skipped", "the terminal's panes not closed (%v): close them there", err)
		return nil
	case len(lines) == 0 && spared == "":
		add("panes", "already", "no pane of the project")
	case len(lines) == 0:
		add("panes", "kept", "pane %s runs this command (close it by hand)", spared)
	default:
		note := ""
		if spared != "" {
			note = "; pane " + spared + " runs this command"
		}
		verb := "closed"
		if o.dry {
			verb = "would close"
		}
		add("panes", "done", "%d %s%s", len(lines), verb, note).lines = lines
	}
	return nil
}

// projectPane: a labelled pane of one of the project's agents, by its label and cwd (labels are machine-wide).
func (s *Setup) projectPane(p port.Pane) bool {
	if p.Label == "" {
		return false
	}
	info, ok := s.cfg.AgentLookup(p.Label, "")
	return ok && (project.LabelOurs(s.cfg, p.Label, p.Cwd, "") ||
		info.Cwd != "" && project.LabelOurs(s.cfg, p.Label, p.Cwd, info.Cwd))
}

// uninstallTrees removes the worktrees under lanes.dir and the registrations of the missing ones.
func (s *Setup) uninstallTrees(ctx context.Context, g port.Pruner, p *uninstallPlan, o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	var lines []string
	for _, t := range p.trees {
		if o.dry {
			lines = append(lines, fmt.Sprintf("would remove worktree %s (was %s)", t.Dir, short(t.SHA)))
			continue
		}
		if err := g.RemoveTree(ctx, t.Dir); err != nil {
			return project.Failf("worktree %s not removed: %v", t.Dir, err)
		}
		lines = append(lines, fmt.Sprintf("removed worktree %s (was %s)", t.Dir, short(t.SHA)))
	}
	if len(p.gone) > 0 {
		var dirs []string
		for _, t := range p.gone {
			dirs = append(dirs, t.Dir)
		}
		failed := map[string]error{}
		if !o.dry {
			failed = g.PruneTrees(ctx, dirs)
		}
		for _, t := range p.gone {
			switch err := failed[t.Dir]; {
			case err != nil:
				return project.Failf("registration of %s not removed: %v", t.Dir, err)
			case o.dry:
				lines = append(lines, fmt.Sprintf("would prune registration %s (was %s)", t.Dir, short(t.SHA)))
			default:
				lines = append(lines, fmt.Sprintf("pruned registration %s (was %s)", t.Dir, short(t.SHA)))
			}
		}
	}
	if len(lines) == 0 {
		add("worktrees", "already", "none under %s", s.cfg.LanesDir())
		return nil
	}
	if !o.dry {
		_ = os.Remove(s.cfg.LanesDir()) // when empty
	}
	add("worktrees", "done", "%d under %s", len(lines), s.cfg.LanesDir()).lines = lines
	return nil
}

// uninstallBranches deletes the task/* branches whose task is merged, one checked out in a worktree uninstallTrees
// removed too (and with --discard the others), and obor's refs.
func (s *Setup) uninstallBranches(ctx context.Context, g port.Pruner, p *uninstallPlan, o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	var lines []string
	for _, b := range p.branches {
		switch {
		case p.held[b.Name]:
			lines = append(lines, fmt.Sprintf("kept %s: checked out outside the worktrees uninstall removes", b.Name))
			continue
		case p.unmerged[b.Name] && !o.discard:
			continue // refused before
		case o.dry:
			lines = append(lines, fmt.Sprintf("would delete %s (was %s)", b.Name, short(b.SHA)))
		default:
			if err := g.DeleteBranch(ctx, b); err != nil {
				return project.Failf("branch %s not deleted: %v", b.Name, err)
			}
			lines = append(lines, fmt.Sprintf("deleted %s (was %s)", b.Name, short(b.SHA)))
		}
	}
	refs, err := s.obRefs()
	if err != nil {
		return err
	}
	for _, r := range refs {
		if o.dry {
			lines = append(lines, fmt.Sprintf("would delete %s (was %s)", r[0], short(r[1])))
			continue
		}
		if _, err := s.git().Git(s.root, "update-ref", "-d", r[0], r[1]); err != nil {
			return project.Failf("ref %s not deleted: %v", r[0], err)
		}
		lines = append(lines, fmt.Sprintf("deleted %s (was %s)", r[0], short(r[1])))
	}
	changed := false
	for _, l := range lines {
		changed = changed || !strings.HasPrefix(l, "kept ")
	}
	st := add("branches", doneOr(changed), "%d task branches and obor refs", len(lines))
	if !changed {
		st.what = "no task branch or obor ref"
		if len(lines) > 0 {
			st.what = "none deleted"
		}
	}
	st.lines = lines
	return nil
}

// obRefs is every ref under refs/obor/ with its sha.
func (s *Setup) obRefs() ([][2]string, error) {
	out, err := s.git().Git(s.root, "for-each-ref", "--format=%(refname) %(objectname)", "refs/obor/")
	if err != nil {
		return nil, project.Failf("refs/obor/ not listed: %v", err)
	}
	var refs [][2]string
	for _, l := range strings.Split(out, "\n") {
		if name, sha, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			refs = append(refs, [2]string{name, sha})
		}
	}
	return refs, nil
}

// hooksState is the project's core.hooksPath as uninstall sees it: "already" (not set), "kept" (not obor's, with its
// value) or "obor" (obor's, or a chain obor set).
func (s *Setup) hooksState() (state, cur string) {
	dir := filepath.Join(s.Kit, "githooks")
	cur, _ = s.git().Git(s.root, "config", "--local", "--get", "core.hooksPath")
	cur = strings.TrimSpace(cur)
	chain, _ := s.git().Git(s.root, "config", "--local", "--get", "obor.hooksChain")
	chain = strings.TrimSpace(chain)
	switch {
	case cur == "" && chain == "":
		return "already", ""
	case cur != "" && cur != dir && cur != filepath.Join(dir, "chain"):
		return "kept", cur
	}
	return "obor", cur
}

// uninstallHooks is obor setup githooks --remove for a core.hooksPath that is obor's.
func (s *Setup) uninstallHooks(o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	state, cur := s.hooksState()
	switch {
	case state == "already":
		add("githooks", "already", "core.hooksPath not set")
	case state == "kept":
		add("githooks", "kept", "core.hooksPath is %s, not obor's", cur)
	case o.dry:
		add("githooks", "done", "core.hooksPath would be removed")
	default:
		was := s.Out
		s.Out = io.Discard
		_, err := s.githooksStep([]string{"--remove"})
		s.Out = was
		if err != nil {
			return err
		}
		add("githooks", "done", "core.hooksPath removed")
	}
	return nil
}

// uninstallPlugin undoes the plugin and plugin-links steps.
func (s *Setup) uninstallPlugin(ctx context.Context, p *uninstallPlan, o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	mkt, err := kitMarketplace(s.Kit)
	if err != nil {
		return err
	}
	plugin := "obor@" + mkt
	switch {
	case !s.pluginEnabled(plugin):
		add("plugin", "already", "%s not enabled for the project", plugin)
	case !project.SettingsUntrackedWith(s.root, s.git().Git):
		add("plugin", "kept", "%s is tracked (uninstall dirties nothing tracked): remove %s there by hand",
			project.PluginSettings, plugin)
	case o.dry:
		add("plugin", "done", "%s would be uninstalled (project scope)", plugin)
	default:
		claude, err := project.LookPath(s.Env.Get("PATH"), agents.Default)
		if err != nil {
			add("plugin", "skipped", "%s is not on PATH: run claude plugin uninstall %s --scope project", agents.Default, plugin)
			break
		}
		if err := claudeCall(ctx, claude, s.root, s.Env, "plugin", "uninstall", plugin, "--scope", "project"); err != nil {
			return project.Failf("claude plugin uninstall %s --scope project failed (%s)", plugin, exitText(err))
		}
		add("plugin", "done", "uninstalled %s (project scope)", plugin)
	}
	if p.links {
		add("plugin-links", doneOr(true), "%s links went with the worktrees (lanes.links stays in the kept config)", project.PluginSettings)
	} else {
		add("plugin-links", "already", "no worktree holds a %s link", project.PluginSettings)
	}
	return nil
}

// uninstallRegistration takes the project out of the machine's list of obor projects.
func (s *Setup) uninstallRegistration(o uninstallOpts, add func(name, state, format string, a ...any) *ustep) error {
	file := project.ProjectsFile(s.Env)
	if o.dry {
		listed := slices.ContainsFunc(project.Projects(s.Env), func(p project.Project) bool { return p.Name == s.cfg.Name() })
		if listed {
			add("projects", "done", "%s would leave %s", s.cfg.Name(), file)
		} else {
			add("projects", "already", "%s not in %s", s.cfg.Name(), file)
		}
		return nil
	}
	changed, err := project.Unregister(s.root, s.Env)
	if err != nil {
		return project.Failf("the machine's project list: %v", err)
	}
	if changed {
		add("projects", "done", "%s removed from %s", s.cfg.Name(), file)
	} else {
		add("projects", "already", "%s not in %s", s.cfg.Name(), file)
	}
	return nil
}

// uninstallFiles removes obor stop's marker (obor init refuses a stopped project), keeps the config and, unless
// --purge, the state; --purge removes it and the other gitignored obor files.
func (s *Setup) uninstallFiles(o uninstallOpts, add func(name, state, format string, a ...any) *ustep) {
	marks := []string{project.StoppedFile(s.root), project.StartingFile(s.root)}
	held := false
	for _, m := range marks {
		if _, err := os.Lstat(m); err == nil {
			held = true
			if !o.dry {
				_ = os.Remove(m)
			}
		}
	}
	if held {
		add("stopped", "done", "obor stop's marker removed")
	} else {
		add("stopped", "already", "no obor stop marker")
	}
	add("config", "kept", "%s and every committed file", project.ConfigFile(s.root))
	state := project.StateDir(s.root)
	extra := []string{filepath.Join(s.root, ".obor", "drafts"), filepath.Join(s.root, ".obor", "local.json"),
		filepath.Join(s.root, ".task-run")}
	if !o.purge {
		add("state", "kept", "%s (store, logs, backups): --purge removes it", state)
		return
	}
	var gone []string
	for _, p := range append([]string{state}, extra...) {
		if _, err := os.Lstat(p); err != nil {
			continue
		}
		gone = append(gone, s.rel(p))
		if !o.dry {
			_ = os.RemoveAll(p)
		}
	}
	if len(gone) == 0 {
		add("state", "already", "no gitignored obor file")
		return
	}
	add("state", "done", "%s", strings.Join(gone, ", "))
}
