// Package setup is `obor setup`: make a project obor-ready. Every subcommand is idempotent, prints what it did and
// ends with OK … / FAIL … (exit 1).
//
//	lane <L-NN>  git worktree add --detach <lanes.dir>/<L-NN> <current branch>; symlink lanes.links; raise lanes.count
//	        to NN when it is the next lane (lanes are L-01 … L-NN); run lanes.setup in the lane (under its timeout), leaving
//	        <state>/lanes/<LANE>.setup when it finished (obor init's rerun skips a lane only then, and
//	        only while that marker names the lane_setup configured now). No pane: serve places the lane's worker pane at
//	        its first dispatch (PlaceLane; obor setup pane <LANE> places one before).
//	pane <LANE> [--tab <tab-id> | --new-tab <label> | --pane <pane-id>] [--start]  a pane for the lane's worker, labelled and
//	        with its env typed but no agent in it unless --start (dispatch starts a fresh worker anyway) (agent
//	        <prefix>-<lane lower>; where it goes is app/layout's rule, asked by the terminal adapter's Place, with
//	        --tab, --new-tab or --pane as the person's say; its refusals print after FAIL); cwd the lane, env
//	        OBOR_ROLE / OBOR_LANE (and with --start the kind's vars), every other var of the registry empty
//	        (agents.Kinds.StartVars; no OBOR_AGENT: a name is derived); then the pane label (= the agent). --start is the
//	        launcher's one start (port.Launcher.Launch, shared with obor restart and serve; without --start the same
//	        launch with its NoAgent option: the pane labelled and its env typed): a fresh session, the
//	        model's kind and args (+ Claude --settings), refused rather than stopping what runs (NoStop), the new
//	        pane placed by this command once the start is built (port.Launch.Place); a pane a new tab or a split made
//	        is used once herdr reports it an available shell (within the launcher's shell bound, agent_pane_busy
//	        retried); a failed start is the launcher's FAIL line (a trust dialog, a Codex self-update, herdr's
//	        agent_not_ready: FAIL start failed in <pane> (<hint>)). A tab holds 4 panes (a constant). First of all,
//	        before a pane is placed or labelled, launch's Locate decides: the agent running (serve's identity table,
//	        its label, the terminal's name; a relabelled pane too) → OK, no-op; another agent running in the lane's
//	        worktree → FAIL naming it, nothing placed; its home (the pane labelled with its name at its shell) → the
//	        start goes there; refused (ambiguous, another pane labelled for it than --pane, --pane labelled or run by
//	        another agent, serve down beside an unaccounted agent of the project for --start) → FAIL with Locate's
//	        line, nothing placed.
//	role <role> [same placement] [--start]  the same (no agent unless --start) for a role of the roles table (not worker): agent and type from
//	        roles.<role>, env OBOR_ROLE (the role's env value, triage too), cwd the role's (config.RoleDir):
//	        the reviewer, the deep dive and the integrator their role worktree <lanes.dir>/<role>, added
//	        detached at the main checkout's branch when missing; every other role the root. triage also copies

//	        templates/BUGS.md to docs/BUGS.md if missing; the reviewer and the deep dive also add reviews.dir (default
//	        docs/reviews) to the main checkout's .gitignore, once.
//	githooks [--remove]  core.hooksPath (repo-local config, shared by all worktrees) = <kit>/githooks (pre-commit,
//	        pre-push and githooks/_hook) or, when there is something to chain, <kit>/githooks/chain (every hook git
//	        runs, all through githooks/_hook: a recorded obor.hooksChain or a hook in .git/hooks the small set
//	        lacks). The project's own hooksPath, when already set, is chained (obor.hooksChain, run by every hook)
//	        instead of refused. --remove puts the chained dir back and unsets obor.hooksChain, or unsets
//	        core.hooksPath when there is none (only when it is ours).
//	agents  caches each plugin kind's manifest and runs every kind's install, whose answer `obor setup mcp`
//	        checks; a kind installs nothing user-global.
//
//	rename --name N --prefix P  rename the project and its agents (config, the herdr
//	        workspace); refused while its serve LaunchAgent is installed: see rename.go.
//
//	show  the setup table, read-only: per difficulty level each model ref, in order, with its kind, model id
//	        and effort; the lanes; the roles' models; whether the owner confirmed it.
//
// obor init: see oborinit.go.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
)

// Setup runs `obor setup` subcommands for the project around Dir.
type Setup struct {
	Kit string // the obor kit: templates/, githooks/, skills/
	Dir string // the caller's cwd
	Env project.Env
	// T is the terminal, in neutral terms (port.TerminalSetup): setup says whose pane and near what, the
	// terminal adapter (herdr.Setup) lays it out.
	T   port.TerminalSetup
	Out io.Writer
	// McpCheck is `obor mcp --check` (the doctor's handshake against the server itself; internal/mcp is an adapter,
	// so cmd/obor passes it in).
	McpCheck func(project.Env) (string, error)
	// Launcher builds the agent launcher for the project's config (cmd/obor passes a launch.Launcher: setup is an
	// adapter and does not import launch).
	Launcher func(cfg *config.Config) port.Launcher
	// Serve is `obor serve install` for the project of cfg (cmd/obor passes the service adapter: setup does not import
	// it): obor init's last step. state is done, already or skipped; line says what.
	Serve func(ctx context.Context, cfg *config.Config) (state, line string, err error)
	// Down is what obor uninstall asks of the running project (cmd/obor passes it; nil: obor uninstall fails).
	Down *Teardown
	// Installed is the plist of cfg's serve LaunchAgent when it is installed (cmd/obor passes the service adapter's;
	// nil: none is): obor setup rename refuses while it is.
	Installed func(cfg *config.Config) (plist string, ok bool)
	// Install is the machine for the doctor's install checks (agents.InstallChecker); nil: this one
	// (installHost).
	Install func() agents.InstallHost
	// Check is the terminal's own check (port.Terminal.Check): its line, or the refusal `obor setup check` fails with;
	// nil: the terminal has none.
	Check func(ctx context.Context) (string, error)

	// host is the git seam (nil: realGit): a test of what setup decides gives it a fake.
	host gitHost

	root string // the main checkout, once found
	cfg  *config.Config
	// terminal is the kind obor init writes into a new config and names in its refusals ("": herdr); cmd/obor
	// passes it as --terminal, decided before any write.
	terminal string
	// deferLaneSetup is obor init's no-commit path with the owner's files left uncommitted: the lanes get their
	// worktrees but no lanes.setup, which waits for the files (serve's lane prepare runs it at the first dispatch).
	deferLaneSetup bool
}

// Usage is the one usage line.
func Usage() string {
	var names []string
	for _, r := range append(roles.All(), roles.DefaultRows()...) {
		if r.Name != "worker" && r.Name != roles.Driver {
			names = append(names, r.Name)
		}
	}
	names = append(names, "<custom role>")
	where := "[--tab <id> | --new-tab <label> | --pane <id>]"
	return "usage: obor setup lane <L-NN> | pane <LANE> " + where + " [--start]" +
		" | role <" + strings.Join(names, "|") + "> " + where + " | githooks [--remove] | agents | mcp [--model <name>] | show | check | rename --name N --prefix P"
}

var errUsage = errors.New("usage")

// Run runs one subcommand: 0, or 1 after the FAIL line.
func (s *Setup) Run(ctx context.Context, args []string) int {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	var err error
	switch sub {
	case "lane":
		err = s.lane(ctx, args)
	case "pane":
		err = s.pane(ctx, args)
	case "role":
		err = s.role(ctx, args)
	case "githooks":
		err = s.githooks(args)
	case "agents":
		err = s.agentsCmd(ctx, args)
	case "mcp":
		err = s.mcpCmd(ctx, args)
	case "rename":
		err = s.renameCmd(ctx, args)
	case "show":
		err = s.show(args)
	case "check":
		err = s.check(ctx, args)
	case "obor-init": // `obor init` (oborinit.go); not in Usage either
		err = s.oborInit(ctx, args)
		if errors.Is(err, errUsage) {
			err = project.Fail(InitUsage)
		}
	case "obor-uninstall": // `obor uninstall` (uninstall.go)
		err = s.oborUninstall(ctx, args)
		if errors.Is(err, errUsage) {
			err = project.Fail(UninstallUsage)
		}
	default:
		err = errUsage
	}
	if errors.Is(err, errUsage) {
		err = project.Fail(Usage())
	}
	if err != nil {
		s.say("FAIL %s", err)
		return 1
	}
	return 0
}

func (s *Setup) say(format string, a ...any) { fmt.Fprintf(s.Out, format+"\n", a...) }

// ok says one step's OK line; already (nothing was left to do) adds the suffix.
func (s *Setup) ok(step string, already bool, format string, a ...any) {
	line := fmt.Sprintf(format, a...)
	if already {
		line += " (already)"
	}
	s.say("OK %s: %s", step, line)
}

// rel is p relative to the project root (p unchanged when it is not under it).
func (s *Setup) rel(p string) string {
	if r, err := filepath.Rel(s.root, p); err == nil {
		return r
	}
	return p
}

// inProject: cwd is in the main checkout or under the project's lanes dir; a renamed (or moved) lanes dir is the
// config's.
func (s *Setup) inProject(cwd string) bool {
	if cwd == "" || s.cfg == nil {
		return false
	}
	c := project.Physical(cwd)
	in := func(d string) bool { d = project.Physical(d); return c == d || strings.HasPrefix(c, d+"/") }
	return in(s.root) || in(s.cfg.LanesDir())
}

// findRoot sets the main checkout of the project around Dir.
func (s *Setup) findRoot() error {
	repo, err := s.git().Find(s.Dir)
	if err != nil {
		return project.Failf("not in a git repository")
	}
	s.root = repo.Root
	return nil
}

// load finds the project and loads its effective config. A config.json that exists and fails to load fails with the
// load's own error; the generic "no usable" text is for no file at all.
func (s *Setup) load() error {
	if err := s.findRoot(); err != nil {
		return err
	}
	cfg, err := config.Load(s.root)
	switch {
	case errors.Is(err, config.ErrNoConfig):
		return project.Failf("no usable .obor/config.json (obor init, then obor config validate)")
	case err != nil:
		return project.Failf("%v", err)
	}
	s.cfg = cfg
	return nil
}

// projectConfig is config.Load for the commands that work without a config (setup agents, setup mcp): nil with no error
// when there is no project config.json, or no git repository at all. Any other load error is a Fail naming it.
func (s *Setup) projectConfig() (*config.Config, error) {
	if err := s.findRoot(); err != nil {
		return nil, nil
	}
	cfg, err := config.Load(s.root)
	switch {
	case errors.Is(err, config.ErrNoConfig):
		return nil, nil
	case err != nil:
		return nil, project.Failf("%v", err)
	}
	return cfg, nil
}

// ensureWorktree makes dir a detached worktree at the root's current branch, unless it already is one of this repo.
func (s *Setup) ensureWorktree(dir string) error {
	branch := ""
	if _, err := os.Stat(dir); err != nil {
		out, _ := s.git().Git(s.root, "branch", "--show-current")
		if branch = strings.TrimSpace(out); branch == "" {
			return project.Failf("the main checkout %s is not on a branch", s.root)
		}
	}
	added, err := s.git().AddWorktree(context.Background(), s.root, dir, branch)
	switch {
	case err != nil:
		return project.Failf("%v", err)
	case !added:
		s.say("skip worktree %s (exists)", dir)
	default:
		s.say("worktree %s (detached at %s)", dir, branch)
	}
	return nil
}
