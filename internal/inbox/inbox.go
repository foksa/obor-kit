// Package inbox posts one line into an agent's session inbox: the project's record for the agent's slot, the pane's
// kind's lines from it, and one write to the socket it names — the path serve's lines and job wakes take (app.Poster).
// The live conformance tier's inbox row posts through it too, so a green row is the shipped path's.
package inbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/project"
)

// Poster posts into a project's inbox (app.Poster): the config at Root, its kinds.
type Poster struct {
	Root  string
	Kinds agents.Kinds
}

// wait bounds one post: the dial, the write, and the wait for the session to close its end after reading.
const wait = 3 * time.Second

// Post posts line to agent's session as kind posts it (the pane's kind): the record read, the kind's lines from it, one
// dial, the lines written, then close; or, for a record the kind sends with itself (agy's), its deliver sends it (began
// once it says sent). A send the kind refused marks that record refused (project.RefuseInbox): not live, the lead's
// lines go through its hooks until an obor command of the session records it again; so does a dial no socket answers (a
// session that ended left its socket, or the path names no socket): not live until the session records one again.
// began: a byte of it went out (a refusal before is safe to retry). No ack comes back.
func (p Poster) Post(ctx context.Context, kind, agent, line string) (began bool, err error) {
	k, ok := p.Kinds.Lookup(kind)
	if !ok || !k.Caps().Inbox {
		return false, fmt.Errorf("%s: kind %s posts through no inbox", agent, kind)
	}
	cfg, slot, err := slotOf(p.Root, agent)
	if err != nil {
		return false, fmt.Errorf("no session recorded for %s: %w", agent, err)
	}
	f, err := project.InboxAt(cfg, slot)
	if err != nil {
		return false, fmt.Errorf("no session recorded for %s: %w", agent, err)
	}
	rec, err := os.ReadFile(f)
	if err != nil {
		return false, fmt.Errorf("no session recorded for %s: %w", agent, err)
	}
	in, err := project.ReadInbox(cfg, slot)
	if err == nil && in.Record != "" {
		rec = []byte(in.Record) // a plugin kind's deliver is given back its own record
	}
	d, ok := k.Deliver(line, rec)
	switch {
	case ok && d.Sent: // the kind sent it itself (agy's agentapi): no socket, no ack
		return true, nil
	case in.Sends && !ok: // its record goes stale (a new token per start): not live until recorded again
		why := d.Refused
		if why == "" {
			why = "its deliver sends nothing with the record"
		}
		_ = project.RefuseInbox(cfg, slot, in, why)
		return false, fmt.Errorf("%s refused the send: %s", agent, why)
	case !ok || d.Typed || d.Socket == "":
		return false, fmt.Errorf("no session recorded for %s: %s names no socket", agent, f)
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var dialer net.Dialer
	c, err := dialer.DialContext(ctx, "unix", d.Socket)
	if err != nil {
		if noSocket(err) { // a dead session's socket, or the path names none: the record is not live either
			_ = project.RefuseInbox(cfg, slot, in, err.Error())
		}
		return false, err
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if n, err := c.Write([]byte(strings.Join(d.Lines, "\n") + "\n")); err != nil {
		return n > 0, err
	}
	// Half-close and let the session read to the end before the connection goes: it closes its end once read (a
	// silent session: the deadline ends the wait; the lines are written either way).
	if uc, ok := c.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
		_, _ = io.Copy(io.Discard, c)
	}
	return true, nil
}

// noSocket: err is a dial that no socket answered: nothing listens on the path (ECONNREFUSED), the path is gone
// (ENOENT), or it names no socket (ENOTSOCK). A dial refused for another reason (a permission, a too long path) says
// nothing about the record.
func noSocket(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTSOCK)
}

// slotOf is the slot agent holds in the project at root, its inbox record's key (project.AgentSlot under the config as
// it is now, returned with it: a rename moves no record); an error when agent holds none there.
func slotOf(root, agent string) (*config.Config, string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, "", err
	}
	slot := project.AgentSlot(cfg, agent)
	if slot == "" {
		return nil, "", fmt.Errorf("%s holds no slot of the project at %s", agent, root)
	}
	return cfg, slot, nil
}
