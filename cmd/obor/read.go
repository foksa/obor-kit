package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

// readCmd is `obor read <agent> [--lines N]`: the last N lines (default 60) of the agent's pane as the project's
// terminal shows them. The agent → pane lookup is the one `obor restart` uses (launch.Launcher.PaneOf): the pane
// labelled with the agent's name that is this project's. Read-only: no key is typed and nothing is changed.
func readCmd(args []string) int {
	agent, lines, ok := readArgs(args)
	if !ok {
		fmt.Fprintln(out, "FAIL "+help.Usage("read"))
		return 1
	}
	cwd, _ := os.Getwd()
	repo, err := project.Find(cwd)
	if err != nil {
		errf("obor: not in a git repository (no .obor/config.json)")
		return 3
	}
	cfg, lerr := config.Load(repo.Root)
	switch {
	case errors.Is(lerr, config.ErrNoConfig): // no .obor/config.json: there is no config to look the agent up in
		errf("%s", strings.TrimSuffix(lerr.Error(), ": "+config.ErrNoConfig.Error()))
		return 3
	case lerr != nil:
		fmt.Fprintf(out, "FAIL the config does not load: %v (obor config validate)\n", lerr)
		return 1
	}
	t, terr := terminal(cfg, project.OSEnv)
	if terr != nil {
		fmt.Fprintln(out, "FAIL "+terr.Error())
		return 1
	}
	return readOn(context.Background(), out, cfg, t.Control, agent, lines)
}

// readArgs is readCmd's arguments: the agent and the pane lines (60 unless --lines names another). ok false: the
// usage (no agent, two agents, an unknown flag, --lines without a positive number).
func readArgs(args []string) (agent string, lines int, ok bool) {
	lines = 60
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--lines":
			if i+1 == len(args) {
				return "", 0, false
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				return "", 0, false
			}
			lines, i = n, i+1
		case strings.HasPrefix(a, "-") || agent != "":
			return "", 0, false
		default:
			agent = a
		}
	}
	if agent == "" {
		return "", 0, false
	}
	return agent, lines, true
}

// readOn is readCmd after its arguments: the agent's pane's recent text, printed; a FAIL line and 1 when the agent
// is unknown, no pane is labelled with it, or the terminal is unreachable.
func readOn(ctx context.Context, w io.Writer, cfg *config.Config, h port.PaneControl, agent string, lines int) int {
	text, err := paneText(ctx, cfg, h, agent, lines)
	if err != nil {
		fmt.Fprintln(w, "FAIL "+err.Error())
		return 1
	}
	fmt.Fprint(w, text)
	if text != "" && !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(w)
	}
	return 0
}

// paneText is the agent's pane's last lines (the terminal's Recent: its recent output, long lines unwrapped): the
// pane PaneOf finds for the agent. An agent neither the config nor a recorded session knows is unknown; one with no
// pane labelled with its name has none; a terminal that cannot list its panes is unreachable.
func paneText(ctx context.Context, cfg *config.Config, h port.PaneControl, agent string, lines int) (string, error) {
	l := &launch.Launcher{Cfg: cfg, H: h, Out: io.Discard}
	pane, err := l.PaneOf(ctx, agent, "")
	switch {
	case errors.Is(err, launch.ErrNoPane):
		if !knownAgent(cfg, agent) {
			return "", fmt.Errorf("unknown agent %s (no lane, role or recorded session)", agent)
		}
		return "", fmt.Errorf("no pane labelled %s", agent)
	case err != nil:
		return "", fmt.Errorf("terminal unreachable: %v", err)
	}
	text, err := h.Recent(ctx, pane, lines)
	switch {
	case port.NotFound(err): // the pane went between the lookup and the read
		return "", fmt.Errorf("no pane labelled %s", agent)
	case err != nil:
		return "", fmt.Errorf("terminal unreachable: %v", err)
	}
	return text, nil
}

// knownAgent: the config names agent (a lane's worker or drive, or a role agent), or a session of it is recorded.
func knownAgent(cfg *config.Config, agent string) bool {
	if _, ok := cfg.AgentLookup(agent, ""); ok {
		return true
	}
	_, ok := store.CurrentAt(cfg.Root, agent)
	return ok
}
