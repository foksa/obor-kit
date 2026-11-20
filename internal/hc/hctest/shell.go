package hctest

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"obor/internal/hc"
)

// shell.go: what typing reaches in the in-memory core, as hc 0.1.21 did it (docs/research/hc-probes.md P2, P3, P8):
// terminal.send pastes its text after the terminal's unsent input and presses Enter, refused at a program that reports
// blocked unless forced; a shell terminal whose shell holds the foreground runs the line: a program the test named
// (Program) takes the foreground and reports its status, any other line prints the word its `&& echo <word>` clause
// echoes. terminal.keys presses keys (ctrl+u clears the unsent input), terminal.label sets labels.

// shell is the core's typing state, under the core's lock.
type shell struct {
	programs map[string]*hc.Status // name → the status it reports once up (nil: none)
	input    map[string]string     // address → its unsent input
	sent     map[string][]string   // address → each line a send delivered, unsent input first
	keys     map[string][]string   // address → each key pressed
	sendWait time.Duration         // how long terminal.send holds its answer (a person's draft)
	pid      int                   // the last pid a program got
}

func newShell() shell {
	return shell{programs: map[string]*hc.Status{}, input: map[string]string{}, sent: map[string][]string{},
		keys: map[string][]string{}, pid: 60000}
}

// event is one event an answer made, sent after the answer.
type event struct {
	address, typ string
	data         any
}

// Program makes a command line typed at a shell terminal whose first word is name run the program: it takes the
// foreground and reports status (nil: it reports nothing, as at a dialog that sends no OSC 7501 record).
func (c *Core) Program(name string, status *hc.Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shell.programs[name] = status
}

// Type is text a person typed into address and did not send: a later send is pasted after it.
func (c *Core) Type(address, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shell.input[address] += text
}

// SetSendWait makes terminal.send hold its answer for d, as hc does while a person's draft is open (up to its
// draft_wait, 60 s).
func (c *Core) SetSendWait(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shell.sendWait = d
}

// Sent is each line a send delivered to address (its unsent input, then the text).
func (c *Core) Sent(address string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.shell.sent[address])
}

// Keys is each key pressed in address, by hc's name.
func (c *Core) Keys(address string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.shell.keys[address])
}

// Exit ends address's program by its own exit with code (a person typed `exit` at a kept terminal's shell): finished
// with reason exited for good; keep brings back only a program that was lost.
func (c *Core) Exit(address string, code int) {
	c.Update(address, func(r *hc.Record) { r.State, r.Reason, r.Status, r.Foreground = "finished", "exited", nil, nil })
	c.Event(address, "finished", map[string]any{"reason": "exited", "exit_code": code, "signal": nil, "finished_ms": nil})
}

// hold waits sendWait before a terminal.send is answered.
func (c *Core) hold(method string) {
	if method != "terminal.send" {
		return
	}
	c.mu.Lock()
	d := c.shell.sendWait
	c.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
}

// flush sends the events the answers made.
func (c *Core) flush() {
	c.mu.Lock()
	evs := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, e := range evs {
		c.Event(e.address, e.typ, e.data)
	}
}

// write answers terminal.send, terminal.keys and terminal.label, under the core's lock.
func (c *Core) write(method string, raw json.RawMessage) (any, string, string) {
	var p struct {
		Terminal string            `json:"terminal"`
		Text     string            `json:"text"`
		Force    bool              `json:"force"`
		Keys     []string          `json:"keys"`
		Labels   map[string]string `json:"labels"`
	}
	_ = json.Unmarshal(raw, &p)
	i := slices.IndexFunc(c.recs, func(r hc.Record) bool { return r.Address == p.Terminal })
	if i < 0 {
		return nil, "not_found", "no terminal " + p.Terminal
	}
	r := &c.recs[i]
	if method == "terminal.label" {
		r.Labels = maps.Clone(r.Labels)
		if r.Labels == nil {
			r.Labels = map[string]string{}
		}
		for k, v := range p.Labels {
			if v == "" {
				delete(r.Labels, k)
			} else {
				r.Labels[k] = v
			}
		}
		c.pending = append(c.pending, event{r.Address, "labels", map[string]any{"labels": maps.Clone(r.Labels)}})
		return *r, "", ""
	}
	if r.State != "live" {
		return nil, "finished", p.Terminal + " has finished"
	}
	if method == "terminal.keys" {
		for _, k := range p.Keys {
			c.shell.keys[r.Address] = append(c.shell.keys[r.Address], k)
			if k == "ctrl+u" {
				delete(c.shell.input, r.Address)
			}
		}
		return map[string]any{"status": state(r.Status), "turn": 0}, "", ""
	}
	if r.Status != nil && r.Status.State == "blocked" && !p.Force {
		return nil, "blocked", p.Terminal + " is blocked (" + r.Status.Kind + ")"
	}
	before := state(r.Status)
	line := c.shell.input[r.Address] + p.Text
	delete(c.shell.input, r.Address)
	c.shell.sent[r.Address] = append(c.shell.sent[r.Address], line)
	if r.Foreground != nil && r.Foreground.PID == r.PID {
		c.run(r, line)
	}
	return map[string]any{"started": nil, "status": before, "turn": 0}, "", ""
}

// run is line run by r's shell: a named program starts, else the line's `&& echo <word>` word is printed.
func (c *Core) run(r *hc.Record, line string) {
	words := strings.Fields(line)
	if len(words) == 0 {
		return
	}
	status, ok := c.shell.programs[words[0]]
	if !ok {
		if w := echoWord(line); w != "" {
			c.screens[r.Address] += w + "\n% \n"
		}
		return
	}
	c.shell.pid++
	f := hc.Foreground{PID: c.shell.pid, Name: words[0], Argv: words, Cwd: r.Foreground.Cwd}
	r.Foreground = &f
	c.pending = append(c.pending, event{r.Address, "foreground", f})
	if status != nil {
		s := *status
		r.Status, r.Reported = &s, true
		c.pending = append(c.pending, event{r.Address, "status", map[string]any{"status": s, "reported": true, "records": map[string]any{}}})
	}
}

// state is s's word, nil (JSON null) for no status.
func state(s *hc.Status) any {
	if s == nil {
		return nil
	}
	return s.State
}

// echoWord is the word line's `&& echo ` clause echoes, quotes removed; "" when it has none.
func echoWord(line string) string {
	_, w, ok := strings.Cut(line, "&& echo ")
	if !ok {
		return ""
	}
	if j := strings.IndexByte(w, ' '); j >= 0 {
		w = w[:j]
	}
	return strings.ReplaceAll(w, `"`, "")
}
