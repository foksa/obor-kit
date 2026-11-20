package hc

import (
	"context"
	"fmt"

	"obor/internal/app/port"
	"obor/internal/herdrapp"
)

// close.go: port.TerminalSetup's CloseProject over hc and herdr-app: the project's terminals killed (the live ones
// labelled obor.project=<name>), then the tabs and the space of the window they leave empty.

// CloseProject kills every live terminal labelled obor.project=c.Name (`terminal.kill`), then closes in herdr-app's
// space named c.Name the tabs that held one of them and hold no other terminal now (`tab.close`), and the space itself
// when that leaves it no tab but an empty lead, lanes or roles tab (`space.close`). A tab or space with a terminal of
// another project, or a tab a person made, stays. A terminal c.Ours refuses (the caller's own; nil: none refused) is
// not killed and counts as still in its tab and space, so neither closes under it. herdr-app not running (or no App)
// leaves its window: a line says so. With c.Dry nothing is killed or closed.
func (cl *Client) CloseProject(ctx context.Context, c port.CloseProject) ([]string, error) {
	recs, err := cl.terminals(ctx)
	if err != nil {
		return nil, err
	}
	var lines []string
	gone := map[string]bool{}
	ownTabs := map[string]bool{}
	for _, r := range recs {
		if r.Labels[LabelProject] != c.Name {
			continue
		}
		if c.Ours != nil && !c.Ours(r.pane()) {
			continue // spared: not in gone, so its tab and space stay
		}
		if !c.Dry {
			if err := cl.call(ctx, nil, "terminal.kill", map[string]any{"terminal": r.Address}); err != nil {
				return lines, fmt.Errorf("terminal %s: %w", r.Address, err)
			}
		}
		gone[r.Address] = true
		if t := r.Labels[herdrapp.LabelTab]; t != "" {
			ownTabs[t] = true
		}
		lines = append(lines, fmt.Sprintf("terminal %s (%s)", r.Address, r.Labels[LabelAgent]))
	}
	if cl.App == nil {
		return lines, nil
	}
	spaces, err := cl.App.Spaces(ctx)
	if err != nil {
		if port.NotRunning(err) {
			return append(lines, "herdr-app is not running: its tabs and space stay"), nil
		}
		return lines, err
	}
	for _, sp := range spaces {
		if sp.Machine != "" || sp.Name != c.Name {
			continue
		}
		var closable []herdrapp.Tab
		allClosable := true
		for _, t := range sp.Tabs {
			left := 0
			for _, r := range inTab(recs, t.ID) {
				if !gone[r.Address] {
					left++
				}
			}
			switch {
			case left > 0:
				allClosable = false
			case ownTabs[t.ID] || t.Name == "lead" || t.Name == "lanes" || t.Name == "roles":
				closable = append(closable, t)
			default:
				allClosable = false
			}
		}
		if allClosable {
			if !c.Dry {
				if err := cl.App.CloseSpace(ctx, sp.ID); err != nil {
					return lines, err
				}
			}
			lines = append(lines, fmt.Sprintf("herdr-app space %s with its %d tabs", sp.Name, len(sp.Tabs)))
			continue
		}
		for _, t := range closable {
			if !c.Dry {
				if err := cl.App.CloseTab(ctx, t.ID); err != nil {
					return lines, err
				}
			}
			lines = append(lines, fmt.Sprintf("herdr-app tab %s", t.Name))
		}
	}
	return lines, nil
}
