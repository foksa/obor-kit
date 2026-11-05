// Package help is obor's one list of commands (T-854): each subcommand cmd/obor dispatches, its usage line, one line on
// what it does and its group. `obor help` prints it and obor web's Help page shows it (GET /api/help), with README's
// "Install" and "Set up a project" sections read from the kit's README at request time (Sections).
package help

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// Command is one row: a subcommand, its usage line (without "usage: "), what it does and its group (one of Groups).
type Command struct {
	Name  string `json:"name"`
	Usage string `json:"usage"`
	Desc  string `json:"desc"`
	Group string `json:"group"`
}

// Groups is the groups in the order they are shown.
var Groups = []string{"tasks", "reviews", "agents", "project", "setup", "internals"}

// Commands is every subcommand cmd/obor dispatches (cmd/obor's TestHelpTableMatchesDispatch keeps the two alike), by
// group, in the order shown.
var Commands = []Command{
	{"spec", `obor spec set <T-xx> <path> [--note "<what changed>"] | obor spec show <T-xx> | obor spec list`,
		"store a task spec (it needs a Difficulty: line), print its current rev, or list the specs", "tasks"},
	{"dispatch", "obor dispatch <T-xx> [--model <model[:effort]>,…] [--after <T-yy,T-zz>] [--force]",
		"run a task on the first free lane of its tier, now or once the --after tasks merge", "tasks"},
	{"watch", `obor watch [<T-xx>[,<T-yy>…] …] [--note "<text>"] | obor watch --rm <T-xx>[,<T-yy>…]`,
		"one line to the lead when the tasks merge (or one is dropped); no argument lists the watches", "tasks"},
	{"resume", `obor resume <T-xx> ["<text>"] [--regate|--sendback]`,
		"act on a task's needs-you: answer its question, nudge a stalled worker, send it back, decide", "tasks"},
	{"drop", `obor drop <T-xx> "<why>" [--discard] [--force]`,
		"stop a task's worker and free its lane (--discard: its unmerged work too)", "tasks"},
	{"drive", "obor drive <name> [--model <model[:effort]>] | obor drive <T-xx>",
		"work on a branch by hand in an interactive agent: a new drive on a free lane, or take over a task", "tasks"},
	{"land", `obor land <T-xx|name> ["<summary>"]`,
		"submit a drive's branch: the gate, the integrator and the merge as for any task", "tasks"},
	{"idle", `obor idle ["<why nothing is in flight>"]`,
		"show the idle state, or acknowledge it with why nothing is in flight", "tasks"},
	{"review", `obor review [--after <T-a>[,<T-b>…]] [--note "<text>"] | --rm | fix <T-a>[,<T-b>…] | close "<summary>"`,
		"request a code review now or after tasks merge, end the request; fix and close are the review's own steps", "reviews"},
	{"lead", "obor lead [--resume]", "start the project's lead in its own tab (--resume continues its last session)", "agents"},
	{"restart", "obor restart <agent> [--pane <id>] [--fresh] [--force] | obor restart <agent> --stop [--pane <id>] [--force]",
		"restart an agent in its pane (--fresh: a new session), or stop it", "agents"},
	{"brief", `obor brief <role-agent> [--fresh] "<text>"`,
		"give a role agent (integrator, reviewer, …) a brief once it is idle", "agents"},
	{"send", `obor send <project> "<text>"`, "post a line to another project's lead", "agents"},
	{"tell-lead", `obor tell-lead "<text>"`, "post a line to this project's lead (from a drive)", "agents"},
	{"whoami", "obor whoami [--json]", "this session's role, agent, lane, task and the protected files it may edit", "agents"},
	{"status", "obor status [--facts] [--json] [--short]",
		"serve, lanes, agents, what needs you, the last full check", "project"},
	{"web", "obor web [--listen 127.0.0.1:<port>]",
		"a localhost page for every project on this machine: lanes, queue, needs-you, events, reports, setup, help", "project"},
	{"pause", `obor pause [--model <name>] [--for <dur>] "<why>" | obor pause [--model <name>] --clear`,
		"stop dispatching (quota, waiting on you), or stop using one model; --clear ends it", "project"},
	{"stop", `obor stop ["<why>"] [--force]`,
		"put the project to sleep: its agents and serve go, files and config stay; --force keeps open tasks open", "project"},
	{"start", "obor start", "wake a stopped project", "project"},
	{"projects", "obor projects | obor projects --notify <release notes file>",
		"the obor projects this machine knows, with serve up or down", "project"},
	{"config", "obor config [get <path> | validate]", "print the effective config, one value, or check it", "project"},
	{"run", "obor run <command-name> [args…]", "run a configured command (commands.<name>) under its timeout", "project"},
	{"flakes", "obor flakes", "the flaky tests of the last 7 days", "project"},
	{"nightly", "obor nightly [on HH:MM | off]", "run the nightly checks now, or schedule or unschedule them", "project"},
	{"version", "obor version", "this binary's version and where it came from", "project"},
	{"help", "obor help", "this list", "project"},
	{"init", "obor init [--name N] [--prefix P]",
		"set up a project: plugin, git hooks, .obor/config.json, lanes, agent panes, obor serve", "setup"},
	{"setup", "obor setup <lane|pane|role|githooks|herdr-plugin|agents|mcp> …",
		"one setup step on its own (a lane, a pane, the git hooks, the MCP server, …)", "setup"},
	{"serve", "obor serve [install | uninstall | restart | dump]",
		"the project's app: dispatch, gate, merges (install: its launchd agent)", "setup"},
	{"hook", "obor hook noop|pre-tool-use|stop|stop-failure|session-start|user-prompt-submit",
		"the agents' hooks (Claude Code runs them)", "internals"},
	{"mcp", "obor mcp [--check]", "the agents' MCP server of job tools (--check: one line on whether it answers)", "internals"},
	{"plugin", "obor plugin restore [--now]", "the herdr plugin's restore command (herdr runs it)", "internals"},
	{"has", "obor has <subcommand> [<sub-subcommand>]", "exit 0 when this binary implements it (the shim asks)", "internals"},
	{"affected", "obor affected go [--changed <file>]", "the Go packages the changed files can break (the gate's tier)", "internals"},
}

// Write is `obor help`: the usage line, then each group's commands, each its usage line and description.
func Write(w io.Writer) {
	fmt.Fprintln(w, "usage: obor <subcommand> [args…]")
	for _, g := range Groups {
		fmt.Fprintf(w, "\n%s:\n", g)
		for _, c := range Commands {
			if c.Group == g {
				fmt.Fprintf(w, "  %-10s %s\n  %-10s %s\n", c.Name, c.Desc, "", c.Usage)
			}
		}
	}
}

// SectionNames is README's sections the Help page shows, in order.
var SectionNames = []string{"Install", "Set up a project"}

// Section is one README section: its heading and its markdown body (without the heading line).
type Section struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Sections is SectionNames' sections of readme ("## <name>" up to the next "## "); one missing is an error naming it.
func Sections(readme string) ([]Section, error) {
	var out []Section
	var missing []string
	lines := strings.Split(readme, "\n")
	for _, name := range SectionNames {
		start := -1
		for i, l := range lines {
			if strings.TrimRight(l, " \r") == "## "+name {
				start = i + 1
				break
			}
		}
		if start < 0 {
			missing = append(missing, name)
			continue
		}
		end := len(lines)
		inCode := false
		for i := start; i < len(lines); i++ {
			if strings.HasPrefix(lines[i], "```") {
				inCode = !inCode
			}
			if !inCode && strings.HasPrefix(lines[i], "## ") {
				end = i
				break
			}
		}
		out = append(out, Section{name, strings.TrimSpace(strings.Join(lines[start:end], "\n"))})
	}
	if len(missing) > 0 {
		return nil, errors.New("README has no section " + strings.Join(missing, ", "))
	}
	return out, nil
}
