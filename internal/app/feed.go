package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"obor/internal/app/wire"
	"obor/internal/store"
)

// feed answers an events request on conn (wire.MethodEvents): the project's records after c.Seq, in seq order,
// one line each (wire.FeedSince: serve's down path reads the same), then wire.FeedEnd; with c.Follow it waits for the
// next record written (event.Log.Changed) and streams on until the caller closes the connection or serve ends. Anyone
// may read it, as status. It reads on its own read-only handle: serve's one connection stays the writer's.
func (a *App) feed(conn net.Conn, c *wire.EventsReq) {
	write := func(ans wire.Answer) bool {
		b, _ := json.Marshal(ans)
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		_, err := conn.Write(append(b, '\n'))
		return err == nil
	}
	if c == nil {
		write(wire.Answer{Error: wire.MethodEvents + ": no call"})
		return
	}
	a.mu.Lock()
	p := a.projAt(c.Root)
	a.mu.Unlock()
	if p == nil {
		write(wire.Answer{Error: fmt.Sprintf("%s: no served project at %s", wire.MethodEvents, c.Root)})
		return
	}
	db, err := store.OpenAt(p.root)
	if err != nil || db == nil {
		write(wire.Answer{Error: fmt.Sprintf("%s: the store of %s: %v", wire.MethodEvents, p.name, err)})
		return
	}
	defer db.Close()
	// The caller closing the connection ends a follow that waits: the request line is read, so any read now ends with
	// its close.
	gone := make(chan struct{})
	_ = conn.SetReadDeadline(time.Time{})
	go func() {
		_, _ = io.Copy(io.Discard, conn)
		close(gone)
	}()
	seq := c.Seq
	for {
		changed := p.log.Changed() // before the read: a record written after it closes this one
		recs, err := wire.FeedSince(db, seq, wire.FeedPage)
		if err != nil {
			write(wire.Answer{Error: wire.MethodEvents + ": " + err.Error()})
			return
		}
		for i := range recs {
			if !write(wire.Answer{Record: &recs[i]}) {
				return
			}
			seq = recs[i].Seq
		}
		switch {
		case len(recs) == wire.FeedPage: // more there: the next page now
			continue
		case !c.Follow:
			write(wire.Answer{Reply: wire.FeedEnd})
			return
		}
		select {
		case <-a.life.Done():
			return
		case <-gone:
			return
		case <-changed:
		}
	}
}
