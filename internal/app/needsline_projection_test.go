package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/store"
)

// countPoster is a Poster and Liner that counts: the lines posted, and how many were typed.
type countPoster struct {
	posts []string
	typed int
}

func (c *countPoster) Post(_ context.Context, _, to, line string) (bool, error) {
	c.posts = append(c.posts, to+": "+line)
	return true, nil
}

func (c *countPoster) Type(context.Context, string, string, bool) (bool, error) {
	c.typed++
	return true, nil
}

// needsMessages is the message facts of p's log that announce a needs-you.
func needsMessages(t *testing.T, p *proj) []Message {
	t.Helper()
	recs, err := ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	var out []Message
	for _, r := range recs {
		if r.Task != MsgTask || r.Step != StepMessage || r.Kind != KindFact {
			continue
		}
		var m Message
		if err := json.Unmarshal(r.Data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Needs != 0 {
			out = append(out, m)
		}
	}
	return out
}

// RV-52-verify V1: a needs-you's line whose append succeeds while the lanes table's write fails (record false) stays
// recorded: a later pass appends no second line, and once the table writes again the lead gets the line once.
func TestRV52VerifyProjectionFailureDoesNotDuplicate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"name":"pj","prefix":"pj","roles":{"integrator":{}}}`)
	in := &countPoster{}
	fx := &Loop{Inbox: in, Typer: in}
	h.a.opt.Loop = func(string) (*Loop, error) { return fx, nil }
	h.a.opt.Inboxed = func(string) (string, bool) { return "", true }
	h.connect()
	p := h.proj()
	t.Cleanup(func() { p.log.Close(); h.a.closeLog() })

	d := FactData{Lane: "O-01", Cause: "lane", Reason: "lane: prepare\nfailed"}
	if !h.a.record(p, Record{Task: "T-1", Step: StepNeedsYou, Kind: KindFact}, d) {
		t.Fatal("the needs-you was refused")
	}
	r := p.folded.active["T-1"][len(p.folded.active["T-1"])-1]
	p.folded.lr.rows["O-01"] = store.LaneRow{Lane: "O-01", Task: "T-1", Agent: "pj-o1"} // a row to write: the write fails
	if _, err := p.log.DB().Exec(`CREATE TRIGGER lanes_fail BEFORE INSERT ON lanes BEGIN SELECT RAISE(ABORT, 'injected lane-row failure'); END`); err != nil {
		t.Fatal(err)
	}

	h.a.needsLines()
	if ms := needsMessages(t, p); len(ms) != 1 || p.folded.lr.err == nil {
		t.Fatalf("precondition: needs-you lines in the log = %d, lane-row error = %v; want 1 and an error", len(ms), p.folded.lr.err)
	}
	h.a.needsLines()
	if ms := needsMessages(t, p); len(ms) != 1 {
		t.Errorf("needs-you lines for one needs-you after a second pass = %d, want 1", len(ms))
	}

	if _, err := p.log.DB().Exec("DROP TRIGGER lanes_fail"); err != nil {
		t.Fatal(err)
	}
	h.a.needsLines()
	for range 4 {
		h.a.deliver(context.Background(), p, fx)
	}
	if len(in.posts) != 1 || in.typed != 0 {
		t.Errorf("after the table writes again: posted %d, typed %d; want 1 posted, none typed", len(in.posts), in.typed)
	}
	for _, m := range needsMessages(t, p) {
		if m.Needs != r.Seq || !strings.Contains(m.Line, "prepare failed") {
			t.Errorf("unexpected line %+v", m)
		}
	}
}
