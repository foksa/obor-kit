package hc

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/port"
)

// setup.go: the parts of port.TerminalSetup hc answers without a layout: a project rename's share and the restart
// hint.

var _ port.TerminalSetup = (*Client)(nil)

// Agents is every live terminal whose program reports a status naming an agent (Pane.Agent), with its obor.agent
// label and cwd.
func (cl *Client) Agents(ctx context.Context) ([]port.Pane, error) {
	ps, err := cl.Panes(ctx)
	if err != nil {
		return nil, err
	}
	out := []port.Pane{}
	for _, p := range ps {
		if p.Agent != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// ResumeHint is "": hc never runs a terminal's program again (a kept terminal comes back as its shell), so it resumes
// no agent obor would restart itself.
func (cl *Client) ResumeHint() string { return "" }

// RenameProject relabels the project's terminals: each live terminal labelled obor.project=old (or name: a rerun)
// gets obor.project=name and, when relabel maps its obor.agent label to another, that label; then herdr-app's space
// named old is renamed to name (`space.rename`). hc not running: no terminal to relabel. herdr-app not running (or no
// App): the space is left, Warn says to rename it by hand.
func (cl *Client) RenameProject(ctx context.Context, old, name string, relabel func(label string) string) (port.Renamed, error) {
	var did []string
	labels, err := cl.relabel(ctx, old, name, relabel)
	switch {
	case port.NotRunning(err):
		did = append(did, "hc is not running: no terminal to relabel")
	case err != nil:
		return port.Renamed{}, err
	default:
		did = labels
	}
	spaceDone, warn := false, false
	if cl.App != nil && old != name {
		switch ok, err := cl.App.RenameSpace(ctx, old, name); {
		case port.NotRunning(err):
			warn = true
			did = append(did, fmt.Sprintf("herdr-app is not running: rename its space %s to %s by hand", old, name))
		case err != nil:
			return port.Renamed{}, err
		case ok:
			spaceDone = true
			did = append(did, fmt.Sprintf("herdr-app space %s → %s", old, name))
		}
	}
	if len(labels) == 0 && !spaceDone && !warn {
		return port.Renamed{Already: true, Line: fmt.Sprintf("no terminal is labelled %s=%s, no agent label follows the "+
			"old prefix, no herdr-app space is named %s", LabelProject, old, old)}, nil
	}
	line := strings.Join(did, "; ")
	if len(labels) > 0 {
		line = "terminal labels " + line
	}
	return port.Renamed{Line: line, Warn: warn}, nil
}

// relabel is the terminal half of RenameProject: one entry for each terminal it relabelled.
func (cl *Client) relabel(ctx context.Context, old, name string, relabel func(label string) string) ([]string, error) {
	var r struct {
		Terminals []Record `json:"terminals"`
	}
	if err := cl.call(ctx, &r, "terminal.list", nil); err != nil {
		return nil, err
	}
	var did []string
	for _, t := range r.Terminals {
		group := t.Labels[LabelProject]
		if t.State != "live" || group != old && group != name {
			continue
		}
		labels := map[string]string{}
		var what []string
		if group != name {
			labels[LabelProject] = name
			what = append(what, fmt.Sprintf("%s → %s", group, name))
		}
		if agent := t.Labels[LabelAgent]; agent != "" && relabel(agent) != agent {
			labels[LabelAgent] = relabel(agent)
			what = append(what, fmt.Sprintf("%s → %s", agent, labels[LabelAgent]))
		}
		if len(labels) == 0 {
			continue
		}
		if err := cl.label(ctx, t.Address, labels); err != nil {
			return nil, err
		}
		did = append(did, t.Address+" "+strings.Join(what, ", "))
	}
	return did, nil
}
