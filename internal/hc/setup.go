package hc

import (
	"context"
	"fmt"
	"strings"

	"obor/internal/app/port"
)

// setup.go: the parts of port.TerminalSetup hc answers without a layout: a project rename's share and the restart
// hint.

// ResumeHint is "": hc never runs a terminal's program again (a kept terminal comes back as its shell), so it resumes
// no agent obor would restart itself.
func (cl *Client) ResumeHint() string { return "" }

// RenameProject relabels the project's terminals: each live terminal labelled obor.project=old (or name: a rerun)
// gets obor.project=name and, when relabel maps its obor.agent label to another, that label. hc not running: nothing
// to do.
func (cl *Client) RenameProject(ctx context.Context, old, name string, relabel func(label string) string) (port.Renamed, error) {
	var r struct {
		Terminals []Record `json:"terminals"`
	}
	err := cl.call(ctx, &r, "terminal.list", nil)
	if port.NotRunning(err) {
		return port.Renamed{Already: true, Line: "hc is not running: no terminal to relabel"}, nil
	}
	if err != nil {
		return port.Renamed{}, err
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
			return port.Renamed{}, err
		}
		did = append(did, t.Address+" "+strings.Join(what, ", "))
	}
	if len(did) == 0 {
		return port.Renamed{Already: true, Line: fmt.Sprintf("no terminal is labelled %s=%s, no agent label follows the "+
			"old prefix", LabelProject, old)}, nil
	}
	return port.Renamed{Line: "terminal labels " + strings.Join(did, "; ")}, nil
}
