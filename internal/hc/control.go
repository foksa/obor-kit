package hc

import (
	"context"
	"errors"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/app/shellpane"
	"obor/internal/project"
)

// SendBound is how long a terminal.send may take: hc pastes the text only after a person's unsent draft is sent or
// cleared, up to its draft_wait of 60 s (hc's config, the person's: obor leaves it as it is), else after a quiet
// pause (a send to an idle agent answered in under 1.6 s, one that starts no turn in about 5 s).
const SendBound = 70 * time.Second

// Prompt types text to the agent in pane (port.Prompter): terminal.send without force, which hc refuses with kind
// blocked when the agent reported a dialog in the meantime (port.Blocked). clear: ctrl+u clears the input first (a send
// appends to what is already typed there). began is false only when nothing was typed: a refusal (hc's answer), or a
// request that never went out; a send whose answer never came (the connection went, or SendBound passed) may have typed.
//
// hc types into whatever runs in the pane: the guard in front of every terminal's Prompt read the pane first (its agent,
// its blocked status). An agent that exits between that read and this send gets the text at its shell. herdr makes the
// read and the send one call (agent.prompt); hc would need a send refused unless the program reports a status, an
// upstream ask.
func (cl *Client) Prompt(ctx context.Context, pane, text string, clear bool) (bool, error) {
	if clear {
		if err := cl.SendKeys(ctx, pane, port.KeyCtrlU); err != nil {
			return false, err
		}
	}
	if err := cl.send(ctx, pane, text, false); err != nil {
		var e *Error
		return errors.As(err, &e) && e.sent && e.Kind == "", err
	}
	return true, nil
}

// send is terminal.send of text to pane, bounded by SendBound: pasted, then Enter. force types at a blocked program
// too.
func (cl *Client) send(ctx context.Context, pane, text string, force bool) error {
	p := map[string]any{"terminal": pane, "text": text}
	if force {
		p["force"] = true
	}
	return cl.doFor(ctx, "terminal.send", SendBound, func(ctx context.Context, s *session) error {
		return s.call(ctx, "terminal.send", p, nil)
	})
}

// Write types text into pane, whatever runs there (terminal.send with force: a dialog gets it too), and Enter after
// it. hc has no call that types without Enter, so submit false is refused (no caller types without it).
func (cl *Client) Write(ctx context.Context, pane, text string, submit bool) error {
	if !submit {
		return &Error{Op: "terminal.send", Msg: "hc types text only with Enter after it: " + project.FirstLine(text)}
	}
	return cl.send(ctx, pane, text, true)
}

// keyNames is obor's key names (port.Keys) as hc's.
var keyNames = map[string]string{port.KeyEnter: "enter", port.KeyEsc: "esc", port.KeyCtrlC: "ctrl+c", port.KeyCtrlU: "ctrl+u"}

// hcKeys is keys in hc's names: obor's names mapped (keyNames), any other (a digit, a kind's own key) lowercased. hc
// reads a key name without its case (`L` presses l), so no key is told from another by case.
func hcKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if h, ok := keyNames[k]; ok {
			k = h
		}
		out = append(out, strings.ToLower(k))
	}
	return out
}

// SendKeys is terminal.keys: keys (obor's names) pressed in pane, whatever runs there.
func (cl *Client) SendKeys(ctx context.Context, pane string, keys ...string) error {
	return cl.call(ctx, nil, "terminal.keys", map[string]any{"terminal": pane, "keys": hcKeys(keys)})
}

// RenamePane sets pane's obor.agent label (terminal.label); label "" removes it.
func (cl *Client) RenamePane(ctx context.Context, pane, label string) error {
	return cl.label(ctx, pane, map[string]string{LabelAgent: label})
}

// label is terminal.label: each of labels set on pane, a "" value removing that label.
func (cl *Client) label(ctx context.Context, pane string, labels map[string]string) error {
	return cl.call(ctx, nil, "terminal.label", map[string]any{"terminal": pane, "labels": labels})
}

// Named is the live terminal labelled obor.agent=name whose program reports a status naming a kind: obor's labels
// are the only names hc's terminals carry. NotFound: no such terminal.
func (cl *Client) Named(ctx context.Context, name string) (port.Pane, error) {
	var r struct {
		Terminals []Record `json:"terminals"`
	}
	if err := cl.call(ctx, &r, "terminal.list", nil); err != nil {
		return port.Pane{}, err
	}
	for _, rec := range r.Terminals {
		if rec.State != "live" || rec.Labels[LabelAgent] != name {
			continue
		}
		if p := rec.pane(cl.kinds()); p.Agent != "" {
			p.Group = ""
			return p, nil
		}
	}
	return port.Pane{}, &Error{Op: "terminal.list", Kind: "not_found", Msg: "no live agent is labelled " + name}
}

// WaitStatus waits up to timeout for the agent in pane to be in one of until: shellpane's wait on its own
// subscription to the core (Panes, no Core: it only dials). A lost connection (no line for HeartbeatLost) is
// NotRunning.
func (cl *Client) WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error {
	return shellpane.WaitStatus(ctx, "hc", Panes{C: cl}, pane, until, timeout)
}
