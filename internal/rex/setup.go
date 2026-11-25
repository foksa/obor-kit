package rex

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"

	"obor/internal/app/layout"
	"obor/internal/app/port"
)

// setup.go: port.TerminalSetup over Rex beside Place (place.go): the agents, the restart hint, a project rename's
// share, the project's close, and the lead's home. The project's group is its session (labelled with its name), a
// tab its window.

var _ port.TerminalSetup = (*Client)(nil)

// Agents is every block whose agent's report counts (Pane.Agent), with its label and cwd.
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

// ResumeHint is "": a Rex server that restores its sessions brings each block back as a fresh login shell, never its
// program (docs/research/rex-probes.md R5), so it resumes no agent obor would restart itself.
func (cl *Client) ResumeHint() string { return "" }

// RenameProject relabels the project's session from old to name (session.set_label; a session labelled name already:
// a rerun) and each of its blocks whose label relabel maps to another (session.set_block_label). No server running:
// nothing to relabel, said.
func (cl *Client) RenameProject(ctx context.Context, old, name string, relabel func(label string) string) (port.Renamed, error) {
	ss, err := cl.sessions(ctx)
	if port.NotRunning(err) {
		return port.Renamed{Line: "Rex is not running: no session to relabel"}, nil
	}
	if err != nil {
		return port.Renamed{}, err
	}
	var did []string
	for _, s := range ss {
		if s.Label != old && s.Label != name {
			continue
		}
		if s.Label != name {
			if err := cl.call(ctx, nil, "session.set_label", map[string]any{"session_id": s.ID, "label": name}); err != nil {
				return port.Renamed{}, err
			}
			did = append(did, fmt.Sprintf("Rex session %s → %s", s.Label, name))
		}
		blocks, err := cl.blocks(ctx, s.ID)
		if err != nil {
			return port.Renamed{}, err
		}
		for _, b := range blocks {
			l := label(b)
			if b.Creator != Terminal || l == "" || relabel(l) == l {
				continue
			}
			if err := cl.setLabel(ctx, s.ID, b.ID, relabel(l)); err != nil {
				return port.Renamed{}, err
			}
			did = append(did, fmt.Sprintf("block %s %s → %s", b.ID, l, relabel(l)))
		}
	}
	if len(did) == 0 {
		return port.Renamed{Already: true, Line: fmt.Sprintf("no Rex session is labelled %s, no block label follows the old prefix", old)}, nil
	}
	return port.Renamed{Line: strings.Join(did, "; ")}, nil
}

// CloseProject closes in the session labelled c.Name each terminal block c.Ours takes (nil: every one; block.close,
// a window closing with its last block), then the session itself when no block of it is left (session.destroy). A
// block c.Ours refuses (the caller's own) stays, and so does its session. With c.Dry nothing is closed.
func (cl *Client) CloseProject(ctx context.Context, c port.CloseProject) ([]string, error) {
	ss, err := cl.sessions(ctx)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, s := range ss {
		if s.Label != c.Name {
			continue
		}
		ts, err := cl.reads().session(ctx, s, nil)
		if err != nil {
			return lines, err
		}
		blocks, err := cl.blocks(ctx, s.ID)
		if err != nil {
			return lines, err
		}
		left := len(blocks)
		for _, t := range ts {
			if c.Ours != nil && !c.Ours(t.pane(cl.kinds())) {
				continue
			}
			if !c.Dry {
				if err := cl.call(ctx, nil, "block.close", map[string]any{"session_id": s.ID, "block_id": t.Block.ID}); err != nil {
					return lines, fmt.Errorf("block %s: %w", t.Block.ID, err)
				}
			}
			left--
			lines = append(lines, fmt.Sprintf("block %s (%s)", t.Block.ID, label(t.Block)))
		}
		if left > 0 {
			continue
		}
		if !c.Dry {
			if err := cl.call(ctx, nil, "session.destroy", map[string]any{"session_id": s.ID}); err != nil {
				return lines, err
			}
		}
		lines = append(lines, "Rex session "+s.Label)
	}
	return lines, nil
}

// TakeLead makes the caller's block p.Pane the lead's home (port.TerminalSetup) when it qualifies (leadCaller):
// labelled p.Agent, its window labelled lead, its session labelled with the project's name.
func (cl *Client) TakeLead(ctx context.Context, p port.LeadPane) (port.LeadTaken, error) {
	c, why, err := cl.leadCaller(ctx, p)
	if err != nil || why != "" {
		return port.LeadTaken{Line: why}, err
	}
	if err := cl.setLabel(ctx, c.session.ID, p.Pane, p.Agent); err != nil {
		return port.LeadTaken{}, err
	}
	lead := layout.Label(port.KindLead)
	if err := cl.call(ctx, nil, "session.set_window_label", map[string]any{"session_id": c.session.ID, "window_id": c.window, "label": lead}); err != nil {
		return port.LeadTaken{}, err
	}
	did := fmt.Sprintf("block %s labelled %s, window %s labelled %s", p.Pane, p.Agent, c.window, lead)
	if line, err := cl.nameSession(ctx, c.session, p.Project.Name); err != nil {
		return port.LeadTaken{}, err
	} else if line != "" {
		did += ", " + line
	}
	return port.LeadTaken{Taken: true, Line: did}, nil
}

// LeadSpace makes the caller's session the project's before anything is placed (port.TerminalSetup) when the caller's
// block qualifies (leadCaller): labelled with the project's name, so Place finds it. The block and its window are not
// touched.
func (cl *Client) LeadSpace(ctx context.Context, p port.LeadPane) (port.LeadTaken, error) {
	c, why, err := cl.leadCaller(ctx, p)
	if err != nil || why != "" {
		return port.LeadTaken{Line: why}, err
	}
	line, err := cl.nameSession(ctx, c.session, p.Project.Name)
	if err != nil {
		return port.LeadTaken{}, err
	}
	return port.LeadTaken{Taken: true, Line: cmp.Or(line, fmt.Sprintf("Rex session %s is the project's", c.session.Label))}, nil
}

// caller is the caller's block that qualifies as the lead's home: its session and window.
type caller struct {
	session Session
	window  string
}

// leadCaller is the caller's block p.Pane when it qualifies as the lead's home (layout.Home over the facts read
// here), else why it does not. Rex's session is the group: a block in it is never another project's (no Foreign).
func (cl *Client) leadCaller(ctx context.Context, p port.LeadPane) (caller, string, error) {
	c := layout.Caller{Pane: p.Pane, Project: p.Project.Name}
	t, err := cl.look(ctx, p.Pane)
	if err != nil && !port.NotFound(err) {
		return caller{}, "", err
	}
	var a at
	if err == nil {
		c.Found = true
		if a, err = cl.find(ctx, p.Pane); err != nil {
			return caller{}, "", err
		}
		if t.ownerRuns() {
			c.Agent = cmp.Or(t.Status.App, t.Status.State)
		}
		if f := t.Proc.Foreground; t.programRuns() && (p.Caller == 0 || f.PID != p.Caller) {
			c.Program = cmp.Or(f.program(), strconv.Itoa(f.PID))
		}
		c.Tabbed = a.Window != ""
		ss, err := cl.sessions(ctx)
		if err != nil {
			return caller{}, "", err
		}
		for _, s := range ss {
			if s.ID == t.Session {
				c.Grouped, c.Group = true, s.Label
			} else if s.Label == p.Project.Name {
				c.Other = true
			}
		}
		if c.Tabbed {
			v, err := cl.sessionView(ctx, t.Session)
			if err != nil {
				return caller{}, "", err
			}
			c.TabMates = len(windowOf(v, a.Window).Blocks) - 1
		}
	}
	if why := layout.Home(c); why != "" {
		return caller{}, why, nil
	}
	return caller{session: Session{ID: t.Session, Label: c.Group}, window: a.Window}, "", nil
}

// nameSession labels session s name unless it is labelled so: what it did ("" when nothing).
func (cl *Client) nameSession(ctx context.Context, s Session, name string) (string, error) {
	if s.Label == name {
		return "", nil
	}
	if err := cl.call(ctx, nil, "session.set_label", map[string]any{"session_id": s.ID, "label": name}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Rex session %s labelled %s", s.Label, name), nil
}
