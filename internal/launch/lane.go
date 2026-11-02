package launch

import (
	"context"
	"errors"

	"obor/internal/app/port"
	"obor/internal/project"
)

var errNoPane = errors.New("no pane")

// PaneOf is agent's pane (R-28 §2): a pane labelled agent that is this project's (RV-19 #3: labels are machine-wide,
// another project may use the same names). With dir: its cwd in dir; else in the main checkout, a lanes_dir worktree
// or the agent's own configured cwd (an agents.<name> entry may live elsewhere). A pane with a known cwd first; one
// with none is taken, except for a bare <lane lower> label in a lane lookup (dir given), which only a cwd can confirm.
// No config, or (without dir) an agent the config does not know: the first pane labelled agent.
func (l *Launcher) PaneOf(ctx context.Context, agent, dir string) (string, error) {
	panes, err := l.H.Panes(ctx)
	if err != nil {
		return "", err
	}
	return l.paneIn(panes, agent, dir)
}

// paneIn is PaneOf over a pane list already read (one listing, so a caller that holds one asks herdr nothing twice).
func (l *Launcher) paneIn(panes []port.PaneInfo, agent, dir string) (string, error) {
	var labelled []string
	cwds := map[string]string{}
	for _, p := range panes {
		if p.Label == agent {
			labelled = append(labelled, p.ID)
			cwds[p.ID] = p.Cwd
		}
	}
	if len(labelled) == 0 {
		return "", errNoPane
	}
	if l.Cfg == nil {
		return labelled[0], nil
	}
	own := ""
	if dir == "" {
		info, ok := project.AgentLookup(l.Cfg, agent, "")
		if !ok {
			return labelled[0], nil
		}
		own = info.Cwd
	}
	maybe := ""
	for _, id := range labelled {
		cwd := cwds[id]
		if !project.LabelOurs(l.Cfg, agent, cwd, dir) && (own == "" || !project.LabelOurs(l.Cfg, agent, cwd, own)) {
			continue
		}
		if cwd != "" {
			return id, nil
		}
		if maybe == "" {
			maybe = id
		}
	}
	if maybe == "" {
		return "", errNoPane
	}
	return maybe, nil
}
