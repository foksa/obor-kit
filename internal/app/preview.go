package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"obor/internal/app/port"
	"obor/internal/app/wire"
)

// mergePreview answers merge_preview, the worker's tool: the caller's lane's work (its HEAD and its uncommitted
// changes) merged onto the target's head now (port.Workflow.MergePreview: read-only, nothing recorded). Refused: not a
// worker, no lane named, no served project, the lane holds no task, no workflow adapter.
func (a *App) mergePreview(c wire.MergePreviewReq) (string, error) {
	switch {
	case c.Role != "worker":
		return "", fmt.Errorf("merge_preview: the worker's tool, not %s's (role %s)", Dash(c.Agent), Dash(c.Role))
	case c.Lane == "":
		return "", errors.New("merge_preview: the caller's lane is required")
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	task := ""
	if p != nil {
		task = p.laneTask(c.Lane)
	}
	a.mu.Unlock()
	switch {
	case p == nil:
		return "", fmt.Errorf("merge_preview: no served project at %s", c.Root)
	case task == "":
		return "", fmt.Errorf("merge_preview: lane %s holds no task in obor serve", c.Lane)
	}
	fx, err := a.loopOf(p)
	if err != nil {
		return "", fmt.Errorf("merge_preview: %v", err)
	}
	if fx.Workflow == nil {
		return "", fmt.Errorf("merge_preview: %s has no git workflow", p.name)
	}
	ctx, cancel := context.WithTimeout(a.life, requestTimeout)
	defer cancel()
	pv, err := fx.Workflow.MergePreview(ctx, c.Lane)
	var refused port.Refused
	if errors.As(err, &refused) {
		return "", fmt.Errorf("merge_preview: %s", refused.Line)
	}
	if err != nil {
		return "", fmt.Errorf("merge_preview: %v", err)
	}
	return previewText(pv), nil
}

// The reply's caps: at most previewPaths conflicted paths, each with at most previewMessages of git's lines; the rest
// is a "… N more" tail.
const (
	previewPaths    = 20
	previewMessages = 4
)

// previewText is merge_preview's reply: clean, or the conflicted paths with git's messages for each (capped), then
// that it is a preview.
func previewText(pv port.Preview) string {
	at := pv.Target + " " + sha7(pv.TargetSHA)
	if len(pv.Conflicts) == 0 {
		return "clean: your work merges onto " + at + "\n(a preview: the gate still decides)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "conflicts with %s:\n", at)
	for i, c := range pv.Conflicts {
		if i == previewPaths {
			fmt.Fprintf(&b, "… %d more\n", len(pv.Conflicts)-i)
			break
		}
		fmt.Fprintf(&b, "  %s\n", c.Path)
		for j, m := range c.Messages {
			if j == previewMessages {
				fmt.Fprintf(&b, "    … %d more\n", len(c.Messages)-j)
				break
			}
			fmt.Fprintf(&b, "    %s\n", m)
		}
	}
	b.WriteString("(a preview: the gate still decides, and the integrator resolves real conflicts)")
	return b.String()
}
