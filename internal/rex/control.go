package rex

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/app/shellpane"
)

// control.go: port.PaneControl and port.AgentStarter over a Rex server. A pane is a terminal block named by its id
// alone, and every block method needs the session that holds it, so each operation first finds the block
// (target.resolve, one call). Typing is a paste (block.event) and the keys `rex send-key` presses; Rex refuses no
// input of its own, so obor's guard (port.GuardPrompts) in front of Prompt is the only rule: a dialog that opens
// between the guard's read and the paste gets the text (docs/research/rex-probes.md R3: a record arrives with its
// dialog, within ~0.1-0.25 s).

var (
	_ port.PaneControl  = (*Client)(nil)
	_ port.AgentStarter = (*Client)(nil)
)

// at is a block found by its id: the session and the window that hold it ("" for a detached block) and its label.
type at struct {
	Session, Window, Label string
}

// find is target.resolve of block: where it is. An id that is no block id, or that no session holds, is not found.
func (cl *Client) find(ctx context.Context, block string) (at, error) {
	missing := &Error{Op: "target.resolve", Code: "not_found", Msg: "no block " + block}
	if !strings.HasPrefix(block, "block:") {
		return at{}, missing
	}
	var r struct {
		Results []struct {
			Status string `json:"status"`
			Target *struct {
				SessionID string `json:"session_id"`
				WindowID  string `json:"window_id"`
				Label     string `json:"label"`
			} `json:"target"`
		} `json:"results"`
	}
	if err := cl.call(ctx, &r, "target.resolve", map[string]any{"kinds": []string{"block"}, "queries": []string{block}}); err != nil {
		return at{}, err
	}
	if len(r.Results) != 1 || r.Results[0].Status != "resolved" || r.Results[0].Target == nil || r.Results[0].Target.SessionID == "" {
		return at{}, missing
	}
	t := r.Results[0].Target
	return at{Session: t.SessionID, Window: t.WindowID, Label: t.Label}, nil
}

// look is block read now: where it is, its processes and its root record. A block whose program exited (a kept
// block: no program runs in it again) is no pane: not found.
func (cl *Client) look(ctx context.Context, block string) (term, error) {
	a, err := cl.find(ctx, block)
	if err != nil {
		return term{}, err
	}
	t := term{Session: a.Session, Block: Block{ID: block, Label: a.Label, Creator: Terminal}}
	if err := cl.term(ctx, &t); err != nil {
		return term{}, err
	}
	if t.Proc.Child == nil {
		return term{}, &Error{Op: Terminal + ".process", Code: "not_found", Msg: block + "'s program has exited"}
	}
	return t, nil
}

// Pane is the block as a pane, no Group (PaneControl's answers carry none).
func (cl *Client) Pane(ctx context.Context, pane string) (port.Pane, error) {
	t, err := cl.look(ctx, pane)
	if err != nil {
		return port.Pane{}, err
	}
	return t.pane(cl.kinds()), nil
}

// Panes is every terminal block of every session whose program runs, as panes with no Group.
func (cl *Client) Panes(ctx context.Context) ([]port.Pane, error) {
	_, terms, err := cl.reads().all(ctx)
	if err != nil {
		return nil, err
	}
	out := []port.Pane{}
	for _, t := range terms {
		p := t.pane(cl.kinds())
		p.Group = ""
		out = append(out, p)
	}
	return out, nil
}

// ProcessInfo is the block's shell and its foreground (the process method): the shell is the first process of the
// block's chain that is no login wrapper (programRuns), the foreground the one process Rex names. An agent's tools
// are not the terminal's foreground and never show; whether the agent works is its status, not this.
func (cl *Client) ProcessInfo(ctx context.Context, pane string) (port.ProcessInfo, error) {
	t, err := cl.look(ctx, pane)
	if err != nil {
		return port.ProcessInfo{}, err
	}
	info := port.ProcessInfo{Pane: pane}
	if sh := t.shell(); sh != nil {
		info.ShellPID = sh.PID
	}
	if f := t.Proc.Foreground; f != nil {
		info.FgPGID = f.PID
		info.Processes = []port.Process{{PID: f.PID, Name: f.Name, Argv0: f.Argv0, Cmdline: f.Argv0, Cwd: f.Cwd}}
	}
	return info, nil
}

// Screen is the block's screen and scrollback as plain text (format, lines trimmed), its last lines (lines ≤ 0: all).
func (cl *Client) Screen(ctx context.Context, pane string, lines int) (string, error) {
	return cl.text(ctx, pane, lines, false)
}

// Recent is the same text with the lines the terminal wrapped joined (format's unwrap): each line one the program
// wrote.
func (cl *Client) Recent(ctx context.Context, pane string, lines int) (string, error) {
	return cl.text(ctx, pane, lines, true)
}

func (cl *Client) text(ctx context.Context, pane string, lines int, unwrap bool) (string, error) {
	a, err := cl.find(ctx, pane)
	if err != nil {
		return "", err
	}
	return cl.format(ctx, a.Session, pane, lines, unwrap)
}

// format is the format method of block in session: plain text, each line's trailing spaces and the blank lines at the
// end trimmed, its last lines (lines ≤ 0: all).
func (cl *Client) format(ctx context.Context, session, block string, lines int, unwrap bool) (string, error) {
	var r struct {
		Content *string `json:"content"`
	}
	args := formatArgs{Format: "text", Trim: true, Unwrap: unwrap}
	if err := cl.call(ctx, &r, Terminal+".format", blockParams(session, block, args)); err != nil {
		return "", err
	}
	if r.Content == nil {
		return "", &Error{Op: Terminal + ".format", Msg: "no content in the answer"}
	}
	return lastLines(strings.TrimRight(*r.Content, "\n"), lines), nil
}

// formatArgs are the format method's own args obor sends (contract.go's Args).
type formatArgs struct {
	Format string `json:"format"`
	Trim   bool   `json:"trim"`
	Unwrap bool   `json:"unwrap"`
}

// lastLines is text's last n lines (n ≤ 0: text).
func lastLines(text string, n int) string {
	if n <= 0 {
		return text
	}
	ls := strings.Split(text, "\n")
	if len(ls) <= n {
		return text
	}
	return strings.Join(ls[len(ls)-n:], "\n")
}

// WaitStatus waits up to timeout for the agent in pane to be in one of until: shellpane's wait on its own Subscribe.
func (cl *Client) WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error {
	return shellpane.WaitStatus(ctx, "rex", Panes{C: cl}, pane, until, timeout)
}

// RenamePane sets the block's label (session.set_block_label); label "" restores Rex's default, which reads as no
// label (label).
func (cl *Client) RenamePane(ctx context.Context, pane, label string) error {
	a, err := cl.find(ctx, pane)
	if err != nil {
		return err
	}
	return cl.setLabel(ctx, a.Session, pane, label)
}

func (cl *Client) setLabel(ctx context.Context, session, block, label string) error {
	return cl.call(ctx, nil, "session.set_block_label", map[string]any{"session_id": session, "block_id": block, "label": label})
}

// Write types text into pane, whatever runs there: a paste (block.event), and with submit Enter after it.
func (cl *Client) Write(ctx context.Context, pane, text string, submit bool) error {
	a, err := cl.find(ctx, pane)
	if err != nil {
		return err
	}
	_, err = cl.write(ctx, a.Session, pane, text, submit)
	return err
}

// write is Write in session: the paste (none for an empty text), then Enter with submit. began: a byte may have
// reached the block (the paste went, or was cut off after the CLI ran).
func (cl *Client) write(ctx context.Context, session, block, text string, submit bool) (began bool, err error) {
	if text != "" {
		if err := cl.call(ctx, nil, "block.event", pasteParams(session, block, text)); err != nil {
			return mayHaveActed(err), err
		}
		began = true
	}
	if submit {
		if err := cl.keys(ctx, session, block, port.KeyEnter); err != nil {
			return began || mayHaveActed(err), err
		}
	}
	return true, nil
}

// pasteParams are block.event's params for a paste of text into block (R7: a multi-line paste stands in an agent's
// input as one prompt, not submitted).
func pasteParams(session, block, text string) map[string]any {
	return map[string]any{"session_id": session, "block_id": block, "event": inputEvent{Type: "paste", Payload: pastePayload{Text: text}}}
}

// inputEvent is block.event's event: a paste of Payload's text.
type inputEvent struct {
	Type    string       `json:"type"`
	Payload pastePayload `json:"payload"`
}

type pastePayload struct {
	Text string `json:"text"`
}

// keyNames is obor's key names (port.Keys) as Rex's key binding syntax; any other (a digit, a kind's own key) is
// passed as it is.
var keyNames = map[string]string{port.KeyEnter: "enter", port.KeyEsc: "escape", port.KeyCtrlC: "ctrl+c", port.KeyCtrlU: "ctrl+u"}

// rexKeys is keys in Rex's names.
func rexKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, cmp.Or(keyNames[k], k))
	}
	return out
}

// SendKeys presses keys (obor's names) in pane, whatever runs there (`rex send-key`).
func (cl *Client) SendKeys(ctx context.Context, pane string, keys ...string) error {
	a, err := cl.find(ctx, pane)
	if err != nil {
		return err
	}
	return cl.keys(ctx, a.Session, pane, keys...)
}

func (cl *Client) keys(ctx context.Context, session, block string, keys ...string) error {
	_, err := cl.run(ctx, "send-key", nil, keyArgs(session, block, keys)...)
	return err
}

// keyArgs is the send-key command line pressing keys (obor's names) in block.
func keyArgs(session, block string, keys []string) []string {
	return append([]string{"send-key", "-s", session, "-b", block}, rexKeys(keys)...)
}

// Prompt types text to the agent in pane (port.Prompter): with clear ctrl+u first, then the paste and Enter. Rex
// refuses no input, so this is only the typing behind obor's guard (port.GuardPrompts, where the terminal is built),
// which read the pane first: an agent that opens a dialog, or exits, between that read and this paste gets the text.
// began is false only when nothing was typed.
func (cl *Client) Prompt(ctx context.Context, pane, text string, clear bool) (bool, error) {
	a, err := cl.find(ctx, pane)
	if err != nil {
		return false, err
	}
	if clear {
		if err := cl.keys(ctx, a.Session, pane, port.KeyCtrlU); err != nil {
			return mayHaveActed(err), err
		}
	}
	return cl.write(ctx, a.Session, pane, text, true)
}

// Named is the block labelled name whose agent's report counts (Pane.Agent): obor's labels are the only names Rex's
// blocks carry. NotFound: no such block.
func (cl *Client) Named(ctx context.Context, name string) (port.Pane, error) {
	ps, err := cl.Panes(ctx)
	if err != nil {
		return port.Pane{}, err
	}
	for _, p := range ps {
		if p.Label == name && p.Agent != "" {
			return p, nil
		}
	}
	return port.Pane{}, &Error{Op: "session.list_blocks", Code: "not_found", Msg: "no live agent is labelled " + name}
}

// Start is port.AgentStarter's start on Rex: shellpane's (the agent's command line typed at the block's shell, up by
// the status rule) over the block: its shell is up when something holds its foreground, a program other than its
// shell runs there (programRuns), and its status counts only while the root record's owner is the foreground
// (ownerRuns: a record a dead agent left reads no status). The mark is the block's label (Rex keeps no kind: the
// agent's own report names it).
func (cl *Client) Start(ctx context.Context, s port.AgentStart) (port.Pane, error) {
	return shellpane.Start(ctx, starter{cl}, s)
}

// starter is the client as shellpane's Terminal.
type starter struct{ cl *Client }

func (starter) Name() string { return "rex" }

func (t starter) Look(ctx context.Context, pane string) (shellpane.Look, error) {
	b, err := t.cl.look(ctx, pane)
	if err != nil {
		return shellpane.Look{}, err
	}
	return b.started(t.cl.kinds()), nil
}

// started is t as a start reads it: up while something holds its foreground, the program other than its shell that
// does (programRuns), and its status a report that counts only while the root record's owner is the foreground
// (ownerRuns).
func (t term) started(kinds agents.Kinds) shellpane.Look {
	l := shellpane.Look{Pane: t.pane(kinds), Up: t.Proc.Foreground != nil, Reported: t.ownerRuns()}
	if t.programRuns() {
		f := t.Proc.Foreground
		l.Program = cmp.Or(f.program(), strconv.Itoa(f.PID))
	}
	return l
}

func (t starter) Mark(ctx context.Context, pane, label, _ string) error {
	if label == "" {
		return nil
	}
	return t.cl.RenamePane(ctx, pane, label)
}

func (t starter) Type(ctx context.Context, pane, line string) (bool, error) {
	a, err := t.cl.find(ctx, pane)
	if err != nil {
		return false, err
	}
	return t.cl.write(ctx, a.Session, pane, line, true)
}

// shell is t's shell: the first process of its chain (ancestors, then the foreground) that is no login wrapper; nil
// when Rex names no foreground.
func (t term) shell() *Process {
	f := t.Proc.Foreground
	if f == nil {
		return nil
	}
	chain := append(append([]Process{}, t.Proc.Ancestors...), *f)
	for _, p := range chain {
		if !isLogin(p) {
			return &p
		}
	}
	return nil
}

// errorf is an *Error of op with no code.
func errorf(op, format string, a ...any) *Error {
	return &Error{Op: op, Msg: fmt.Sprintf(format, a...)}
}
