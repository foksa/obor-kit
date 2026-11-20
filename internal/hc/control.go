package hc

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
)

// SendBound is how long a terminal.send may take: hc pastes the text only after a person's unsent draft is sent or
// cleared, up to its draft_wait of 60 s (hc's config, the person's: obor leaves it as it is), else after a quiet
// pause (a send to an idle agent answered in under 1.6 s, one that starts no turn in about 5 s).
const SendBound = 70 * time.Second

// Prompt types text to the agent in pane (port.Prompter): a read first, since hc types into whatever runs there; a
// terminal whose program reports no status runs no agent and is refused as not found, and a blocked one is refused
// before any key (a key would answer its dialog). clear: ctrl+u clears the input first (a send appends to what is
// already typed there). Then terminal.send, which hc refuses with kind `blocked` when the agent reported a dialog in
// the meantime. began is false only when nothing was typed: a refusal (obor's or hc's answer), or a request that
// never went out; a send whose answer never came (the connection went, or SendBound passed) may have typed.
//
// The read and the send are a check and then an act: an agent that exits between them gets the text at its shell.
// herdr makes both one call (agent.prompt); hc would need a send refused unless the program reports a status, an
// upstream ask.
func (cl *Client) Prompt(ctx context.Context, pane, text string, clear bool) (bool, error) {
	r, err := cl.live(ctx, pane)
	if err != nil {
		return false, err
	}
	switch {
	case r.Status == nil:
		return false, &Error{Op: "terminal.send", Kind: "not_found", Msg: pane + " runs no agent (its program reports no status)"}
	case r.Status.State == "blocked":
		return false, &Error{Op: "terminal.send", Kind: "blocked",
			Msg: fmt.Sprintf("%s is blocked (%s): typing would answer its dialog", pane, joined(r.Status.Kind, r.Status.Msg))}
	}
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

// joined is the non-empty ones of a and b, joined by ": ".
func joined(a, b string) string {
	var out []string
	for _, s := range []string{a, b} {
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ": ")
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
		if p := rec.pane(cl.Kinds); p.Agent != "" {
			p.Group = ""
			return p, nil
		}
	}
	return port.Pane{}, &Error{Op: "terminal.list", Kind: "not_found", Msg: "no live agent is labelled " + name}
}

// timedOut is a wait that ended at its own bound (port.TimedOut).
type timedOut struct{ msg string }

func (e *timedOut) Error() string  { return e.msg }
func (e *timedOut) TimedOut() bool { return true }

// turnEnded: hc's status word s meets until. Any of the ready words (idle, done) is met by either: a turn ends when
// the status leaves working, and Claude and Codex then say done (and stay there) while OpenCode says idle (it never
// says done).
func turnEnded(s string, until []string) bool {
	ready := func(w string) bool { return w == "idle" || w == "done" }
	return slices.ContainsFunc(until, func(u string) bool { return u == s || ready(u) && ready(s) })
}

// WaitStatus waits up to timeout for the agent in pane to be in one of until, on its own events.subscribe: the
// subscription's snapshot, then the terminal's events (a missed gives a fresh snapshot). hc's `error` is idle, its
// msg logged. The error matches port.TimedOut at timeout; it is not found when the terminal has none or it finished,
// or when no agent runs there (no status while the shell holds the foreground: none ran, or it exited), and a lost
// connection (no line for HeartbeatLost) is NotRunning.
func (cl *Client) WaitStatus(ctx context.Context, pane string, until []string, timeout time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failed := func(err error) error {
		if ctx.Err() == nil && errors.Is(wctx.Err(), context.DeadlineExceeded) {
			return &timedOut{fmt.Sprintf("hc: the agent in %s is not %s after %s", pane, strings.Join(until, " or "), timeout)}
		}
		return err
	}
	s, err := cl.open(wctx, "events.subscribe")
	if err != nil {
		return failed(err)
	}
	defer s.c.Close()
	var sub struct {
		Terminals []Record `json:"terminals"`
	}
	if err := s.call(wctx, "events.subscribe", nil, &sub); err != nil {
		return failed(err)
	}
	r, known := findRecord(sub.Terminals, pane)
	logged := ""
	for {
		switch {
		case !known:
			return &Error{Op: "events.subscribe", Kind: "not_found", Msg: "no terminal " + pane}
		case r.State != "live":
			return &Error{Op: "events.subscribe", Kind: "finished", Msg: fmt.Sprintf("%s has finished (%s)", pane, r.Reason)}
		case r.Status == nil && !r.programRuns():
			return &Error{Op: "events.subscribe", Kind: "not_found", Msg: pane + " runs no agent (no status, its shell in the foreground)"}
		case r.Status != nil:
			if r.Status.State == "error" && r.Status.Msg != logged {
				logged = r.Status.Msg
				log.Printf("hc: the agent in %s ended its turn on an error: %s", pane, r.Status.Msg)
			}
			if turnEnded(statusWord(r.Status.State), until) {
				return nil
			}
		}
		_ = s.c.SetReadDeadline(time.Now().Add(HeartbeatLost))
		b, err := s.r.ReadBytes('\n')
		if errors.Is(err, os.ErrDeadlineExceeded) && wctx.Err() == nil {
			return &Error{Op: "events.subscribe", Msg: fmt.Sprintf("no heartbeat for %s", HeartbeatLost), down: true}
		}
		if err != nil {
			return failed(connError(wctx, "events.subscribe", err))
		}
		if r, known, err = waitEvent(b, pane, r, known); err != nil {
			return err
		}
	}
}

// waitEvent applies one line of a wait's subscription to r, pane's record (known: hc listed it).
func waitEvent(b []byte, pane string, r Record, known bool) (Record, bool, error) {
	var n struct {
		Method string `json:"method"`
		Params struct {
			Type      string          `json:"type"`
			Terminal  string          `json:"terminal"`
			Data      json.RawMessage `json:"data"`
			Terminals []Record        `json:"terminals"`
		} `json:"params"`
	}
	if err := json.Unmarshal(b, &n); err != nil {
		return r, known, &Error{Op: "events.subscribe", Msg: "unreadable event: " + project.FirstLine(string(b))}
	}
	switch {
	case n.Method == "missed":
		r, known = findRecord(n.Params.Terminals, pane)
		return r, known, nil
	case n.Method != "event" || n.Params.Terminal != pane:
		return r, known, nil
	}
	var err error
	switch n.Params.Type {
	case "created", "restarted":
		var rec Record
		if err = json.Unmarshal(n.Params.Data, &rec); err == nil {
			r, known = rec, true
		}
	case "status":
		var d struct {
			Status   *Status `json:"status"`
			Reported bool    `json:"reported"`
		}
		if err = json.Unmarshal(n.Params.Data, &d); err == nil {
			r.Status, r.Reported = d.Status, d.Reported
		}
	case "foreground":
		var f Foreground
		if err = json.Unmarshal(n.Params.Data, &f); err == nil {
			r.Foreground = &f
		}
	case "finished", "removed":
		var d struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(n.Params.Data, &d)
		r.State, r.Reason = "finished", cmp.Or(d.Reason, n.Params.Type)
	}
	if err != nil {
		return r, known, &Error{Op: "events.subscribe", Msg: fmt.Sprintf("unreadable %s event of %s: %v", n.Params.Type, pane, err)}
	}
	return r, known, nil
}

// findRecord is the record of address in recs.
func findRecord(recs []Record, address string) (Record, bool) {
	i := slices.IndexFunc(recs, func(r Record) bool { return r.Address == address })
	if i < 0 {
		return Record{}, false
	}
	return recs[i], true
}
