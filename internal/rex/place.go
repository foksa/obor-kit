package rex

import (
	"context"
	"fmt"
	"time"

	"obor/internal/app/layout"
	"obor/internal/app/port"
	"obor/internal/project"
)

// place.go: port.TerminalSetup's Place over Rex. A project is one Rex session labelled with the project's name (its
// group); its windows are app/layout's tabs (`lead`, `lanes`, `roles`, MaxPanes blocks to a window), and app/layout
// decides where a pane goes from the windows' labels and their blocks' kinds (a block's kind is the kind of its
// label's agent; a block whose label names no agent of the project is a person's, aside: the session is the
// project's alone). Every window and block obor makes is made with focus false, so a person's view stays where it is,
// and every block runs its shell through the scrubbed environment (env.go).

// window is one window of session.view: its id, label and the terminal blocks of its tiled layer, in layout order.
type window struct {
	ID, Label string
	Blocks    []Block
}

// view is session.view: the session's label and windows.
type view struct {
	Label   string
	Windows []window
}

// sessionView is session.view of session sid.
func (cl *Client) sessionView(ctx context.Context, sid string) (view, error) {
	var r viewAnswer
	if err := cl.call(ctx, &r, "session.view", map[string]any{"session_id": sid}); err != nil {
		return view{}, err
	}
	return r.view(), nil
}

// viewAnswer is session.view's answer as far as obor reads it.
type viewAnswer struct {
	Label   string `json:"label"`
	Windows []struct {
		ID     string `json:"window_id"`
		Label  string `json:"label"`
		Layers []struct {
			Kind   string  `json:"kind"`
			Blocks []Block `json:"blocks"`
		} `json:"layers"`
	} `json:"windows"`
}

// view is r's windows with their terminal blocks, every layer's (a floating one's too: it shows in the window).
func (r viewAnswer) view() view {
	v := view{Label: r.Label}
	for _, w := range r.Windows {
		win := window{ID: w.ID, Label: w.Label}
		for _, l := range w.Layers {
			for _, b := range l.Blocks {
				if b.Creator == Terminal {
					win.Blocks = append(win.Blocks, b)
				}
			}
		}
		v.Windows = append(v.Windows, win)
	}
	return v
}

// projectSession is the session labelled name, made (session.create, no window) when there is none; made says this
// call made it. Two sessions of that label are an error: obor cannot tell which is the project's.
func (cl *Client) projectSession(ctx context.Context, name string) (sid string, made bool, err error) {
	if name == "" {
		return "", false, errorf("session.create", "no project name to label the project's session with")
	}
	ss, err := cl.sessions(ctx)
	if err != nil {
		return "", false, err
	}
	var ids []string
	for _, s := range ss {
		if s.Label == name {
			ids = append(ids, s.ID)
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], false, nil
	case 0:
	default:
		return "", false, project.Failf("%d Rex sessions are labelled %s: close or relabel all but the project's", len(ids), name)
	}
	var r struct {
		SessionID string `json:"session_id"`
	}
	if err := cl.call(ctx, &r, "session.create", map[string]any{"label": name}); err != nil {
		return "", false, err
	}
	if r.SessionID == "" {
		return "", false, errorf("session.create", "no session id in the answer")
	}
	return r.SessionID, true, nil
}

// ask is the placement decision's question for p over the session's windows (layout.Ask).
func ask(p port.Placement, v view) layout.Ask {
	a := layout.Ask{Kind: p.Kind, Group: p.Group, In: p.In, Home: fmt.Sprintf("the %s session", p.Project.Name),
		MaxPanes: p.Project.MaxPanes}
	for _, w := range v.Windows {
		t := layout.Tab{ID: w.ID, Name: w.Label}
		for _, b := range w.Blocks {
			var k port.PaneKind
			if l := label(b); l != "" && p.Project.KindOf != nil {
				k = p.Project.KindOf(l)
			}
			t.Panes = append(t.Panes, layout.Pane{Kind: k, Aside: k == ""})
		}
		a.Tabs = append(a.Tabs, t)
	}
	return a
}

// blockNode is one block of a layout: a terminal block of obor's shell flavor with its options.
type blockNode struct {
	Block struct {
		Flavor  string       `json:"flavor"`
		Options shellOptions `json:"options"`
	} `json:"block"`
}

// shellBlock is the layout of one block running e's command with env in cwd.
func shellBlock(e blockEnv, cwd string, env []string) blockNode {
	var n blockNode
	n.Block.Flavor = ShellFlavor
	n.Block.Options = shellOptions{Shell: "none", Command: e.command(env), Cwd: cwd}
	return n
}

// Place makes a block for p where app/layout puts it and returns its id. The project's session (made when there is
// none) holds the windows; the block goes in a new window or a split of one (placeCall). It runs the server's login
// shell with the server's identity names unset and p's env set, in p.Cwd (envProbe, command): ready as Place returns
// (its shell holds the foreground), nothing typed (p.Starts or not). Its refusals are project failures.
func (cl *Client) Place(ctx context.Context, p port.Placement) (string, error) {
	sid, made, err := cl.projectSession(ctx, p.Project.Name)
	if err != nil {
		return "", err
	}
	if made {
		say(p, "Rex session %s", p.Project.Name)
	}
	v, err := cl.sessionView(ctx, sid)
	if err != nil {
		return "", err
	}
	ans := layout.Place(ask(p, v))
	if ans.Refused != "" {
		return "", project.Failf("%s", ans.Refused)
	}
	if ans.Said != "" {
		say(p, "%s", ans.Said)
	}
	e, err := cl.envProbe(ctx, sid)
	if err != nil {
		return "", err
	}
	method, params, where := placeCall(sid, ans, v, shellBlock(e, p.Cwd, p.Env))
	var r struct {
		Blocks []string `json:"block_ids"`
	}
	if method == "session.new_window" { // each call names its method as a literal: TestSentCoversCallSites reads them
		err = cl.call(ctx, &r, "session.new_window", params)
	} else {
		err = cl.call(ctx, &r, "session.new_split", params)
	}
	if err != nil {
		return "", err
	}
	if len(r.Blocks) != 1 || r.Blocks[0] == "" {
		return "", errorf(method, "the new block's id is not in the answer (%d ids)", len(r.Blocks))
	}
	if err := cl.shellUp(ctx, sid, r.Blocks[0]); err != nil {
		return "", err
	}
	say(p, "window %s: block %s", where, r.Blocks[0])
	return r.Blocks[0], nil
}

// readyBound bounds the wait for a new block's shell; readyPoll is how often the block is read meanwhile.
const (
	readyBound = 10 * time.Second
	readyPoll  = 100 * time.Millisecond
)

// shellUp waits until block's shell holds its foreground (login has started it: its cwd is the block's), within
// readyBound; a shell that does not come up is the error.
func (cl *Client) shellUp(ctx context.Context, sid, block string) error {
	t := term{Session: sid, Block: Block{ID: block}}
	for end := time.Now().Add(readyBound); ; {
		if err := cl.call(ctx, &t.Proc, Terminal+".process", blockParams(sid, block, nil)); err != nil {
			return err
		}
		if f := t.Proc.Foreground; f != nil && !isLogin(*f) && !t.programRuns() {
			return nil
		}
		if time.Now().After(end) {
			return errorf("place", "the shell of block %s did not come up in %s", block, readyBound)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}
}

// placeCall is the call that carries ans out in session sid of view v with node as the new block, and the window's
// label: a new window of the answer's name, unfocused; else a split after the last block of the window it names (the
// window itself when it holds none), its direction alternating with the window's count so the blocks tile.
func placeCall(sid string, ans layout.Answer, v view, node blockNode) (method string, params map[string]any, where string) {
	if ans.New != "" {
		return "session.new_window", map[string]any{"session_id": sid, "window_label": ans.New, "focus": false, "layout": node}, ans.New
	}
	w := windowOf(v, ans.Tab.ID)
	params = map[string]any{"session_id": sid, "direction": splitDirection(len(w.Blocks)), "side": "after",
		"focus": false, "layout": node}
	if n := len(w.Blocks); n > 0 {
		params["anchor_id"] = w.Blocks[n-1].ID
	} else {
		params["window_id"] = w.ID
	}
	return "session.new_split", params, w.Label
}

// splitDirection is how a split beside a window's last block divides it when the window holds n blocks: side by
// side for an odd count, one above the other for an even one.
func splitDirection(n int) string {
	if n%2 == 0 {
		return "vertical"
	}
	return "horizontal"
}

// windowOf is the window of v with this id.
func windowOf(v view, id string) window {
	for _, w := range v.Windows {
		if w.ID == id {
			return w
		}
	}
	return window{ID: id}
}

func say(p port.Placement, format string, a ...any) {
	if p.Say != nil {
		p.Say(fmt.Sprintf(format, a...))
	}
}
