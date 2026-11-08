// Package setup is `obor setup`: make a project obor-ready. Every subcommand is idempotent, prints what it did and
// ends with OK … / FAIL … (exit 1).
//
//	lane <L-NN>  git worktree add --detach <lanes_dir>/<L-NN> <current branch>; symlink lane_links; raise config.lanes
//	        to NN when it is the next lane (lanes are L-01 … L-NN, T-765); run commands.lane_setup in the lane (under its timeout), leaving
//	        <state>/lanes/<LANE>.setup when it finished (T-613: obor init's rerun skips a lane only then, and T-618:
//	        only while that marker names the lane_setup configured now).
//	pane <LANE> [--tab <tab-id> | --new-tab <label> | --pane <pane-id>] [--start]  a pane for the lane's worker, labelled and
//	        with its env typed but no agent in it unless --start (T-705: dispatch starts a fresh worker anyway) (agent
//	        <prefix>-<lane lower>; the placement is the terminal adapter's, herdr.Setup.Place, T-1044): split the largest pane of --tab (default: the caller's tab
//	        when it is the project's, else the project's busiest tab, T-628; a tab is the project's only when every
//	        pane in it has a known cwd in the project, and with none a new tab, T-632), or a new tab in the project's
//	        workspace (the one with most panes in the project), or an existing shell pane; without a pane of the
//	        project anywhere a new herdr workspace named after the project, cwd the root (never the caller's or
//	        herdr's active one, T-632), and another project's pane is never split; cwd the lane, env
//	        OBOR_ROLE / OBOR_LANE and the kind's vars, every other var of the registry empty (agents.StartVars, T-812; no
//	        OBOR_AGENT: a name is derived, T-786); then the pane label (= the agent, R-28 §2: refused when another pane
//	        holds it or --pane is labelled otherwise) and herdr agent start with the model's kind and args (+ Claude
//	        --settings), both through launch.Start (the one start, shared with obor restart). A pane a new tab or a split
//	        made is used once herdr reports it an available shell (T-628: bounded at 10 s, agent_pane_busy retried).
//	        Refuses a full tab
//	        (limits.max_panes_per_tab, default 4). A failed start with a trust dialog or a Codex self-update in the
//	        pane (or herdr's agent_not_ready) → FAIL agent not ready: <hint>. A running agent → OK, no-op.
//	role <role> [same placement] [--start]  the same (no agent unless --start, T-708) for a role of the roles table (not worker): agent and type from
//	        roles.<role>, env OBOR_ROLE (the role's env value, triage too: T-796), cwd the role's (config.RoleDir,
//	        T-853): the reviewer, the deep dive and the integrator their role worktree <lanes_dir>/<role>, added
//	        detached at the main checkout's branch when missing; every other role the root. triage also copies

//	        templates/BUGS.md to docs/BUGS.md if missing; the reviewer and the deep dive also add paths.reviews (default
//	        docs/reviews) to the main checkout's .gitignore, once (T-731).
//	githooks [--remove]  core.hooksPath (repo-local config, shared by all worktrees) = <kit>/githooks (pre-commit,
//	        pre-push and githooks/_hook) or, when there is something to chain, <kit>/githooks/chain (every hook git
//	        runs, all through githooks/_hook; T-623: a recorded obor.hooksChain or a hook in .git/hooks the small set
//	        lacks). The project's own hooksPath, when already set, is chained (obor.hooksChain, run by every hook)
//	        instead of refused. --remove puts the chained dir back and unsets obor.hooksChain, or unsets
//	        core.hooksPath when there is none (only when it is ours).
//	herdr-plugin  the obor.events herdr plugin (R-28 §3; user-global, once per user): link <kit>/herdr-plugin in
//	        herdr, or re-link it when herdr runs another kit's copy (herdr stores the resolved path, so a kit at a new
//	        path needs this); herdr's plugin list is read once, and an error reading it is a FAIL, not "linked"
//	        (T-613); then one hint line unless herdr's config sets `[session] resume_agents_on_restore =
//	        false` (never edited, T-159).
//	agents [--remove]  what each kind's bridge says to install (user-global, once per user): its plugin link
//	        and the obor entry of its user config (none today; another kit's copy re-linked, anything else there left
//	        alone; --remove deletes those), and the removal of what an older obor installed (T-809: OpenCode's
//	        <XDG_CONFIG_HOME|~/.config>/opencode/plugins/obor.js link and mcp.servers.obor entry, when obor's).
//
//	rename --name N --prefix P  rename the project and its agents (config, lane opencode.json kit paths, the herdr
//	        workspace); refused while its serve LaunchAgent is installed: see rename.go.
//
//	show  the setup table, read-only (T-647): per difficulty level each model ref, in order, with its kind, model id
//	        and effort; the lanes; the roles' models; whether the owner confirmed it.
//
// obor init (P7-2): see oborinit.go.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/roles"
)

// Setup runs `obor setup` subcommands for the project around Dir.
type Setup struct {
	Kit string // the obor kit: templates/, githooks/, herdr-plugin/
	Dir string // the caller's cwd
	Env project.Env
	// T is the terminal, in neutral terms (port.TerminalSetup, T-1044): setup says whose pane and near what, the
	// terminal adapter (herdr.Setup) lays it out.
	T   port.TerminalSetup
	Out io.Writer
	// McpCheck is `obor mcp --check` (the doctor's handshake against the server itself; internal/mcp is an adapter,
	// so cmd/obor passes it in).
	McpCheck func(project.Env) (string, error)
	// Starter builds the agent starter for the project's config (cmd/obor passes a launch.Launcher: setup is an
	// adapter and does not import launch).
	Starter func(cfg *config.Config) port.Starter
	// Serve is `obor serve install` for the project of cfg (cmd/obor passes the service adapter: setup does not import
	// it): obor init's last step. state is done, already or skipped; line says what.
	Serve func(ctx context.Context, cfg *config.Config) (state, line string, err error)
	// Installed is the plist of cfg's serve LaunchAgent when it is installed (cmd/obor passes the service adapter's;
	// nil: none is): obor setup rename refuses while it is (DD-consolidation I7).
	Installed func(cfg *config.Config) (plist string, ok bool)
	// Install is the machine for the doctor's install checks (agents.InstallChecker, T-1045); nil: this one
	// (installHost).
	Install func() agents.InstallHost
	// ShellWait bounds the wait for a pane a new tab or a split made to be an available shell (T-628; 0: 10 s).
	ShellWait time.Duration

	root  string // the main checkout, once found
	cfg   *config.Config
	wrote bool // init: a file was created or appended to
	// start's last placement (obor init): the pane, and whether the agent was running already
	placed  string
	running bool
	started bool      // and whether it started one (a pane without --start does not, T-705)
	idle    bool      // a pane labelled for the agent was there already, no agent in it (a rerun without --start)
	hints   *[]string // obor init: herdr's config hint goes here, to its summary, instead of out
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
		" | role <" + strings.Join(names, "|") + "> " + where + " | githooks [--remove] | herdr-plugin | agents [--remove] | mcp [--model <name>] | show | rename --name N --prefix P"
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
	case "herdr-plugin":
		err = s.herdrPlugin(ctx, args)
	case "agents":
		err = s.agentsCmd(args)
	case "mcp":
		err = s.mcpCmd(ctx, args)
	case "rename":
		err = s.renameCmd(ctx, args)
	case "show":
		err = s.show(args)
	case "obor-init": // `obor init` (oborinit.go); not in Usage either
		err = s.oborInit(ctx, args)
		if errors.Is(err, errUsage) {
			err = project.Fail(InitUsage)
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
	return in(s.root) || in(config.AbsUnder(s.root, s.cfg.Data.LanesDir))
}

// findRoot sets the main checkout of the project around Dir.
func (s *Setup) findRoot() error {
	repo, err := project.Find(s.Dir)
	if err != nil {
		return project.Failf("not in a git repository")
	}
	s.root = repo.Root
	return nil
}

// load finds the project and loads its effective config.
func (s *Setup) load() error {
	if err := s.findRoot(); err != nil {
		return err
	}
	cfg, err := config.Load(s.root)
	if err != nil {
		return project.Failf("no usable .obor/config.json (obor init, then obor config validate)")
	}
	s.cfg = cfg
	return nil
}

// ensureWorktree makes dir a detached worktree at the root's current branch, unless it already is one of this repo.
func (s *Setup) ensureWorktree(dir string) error {
	branch := ""
	if _, err := os.Stat(dir); err != nil {
		out, _ := project.Git(s.root, "branch", "--show-current")
		if branch = strings.TrimSpace(out); branch == "" {
			return project.Failf("the main checkout %s is not on a branch", s.root)
		}
	}
	added, err := project.AddWorktree(context.Background(), s.root, dir, branch)
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
