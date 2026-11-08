package app

import (
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"strings"
	"testing"
	"time"
)

// unknownReads are the failed lane reads that make a lane unknown (T-871: checks.Lanes, TestLaneUnknownOnFailedRead
// proves each one reads as an unknown lane with no task); a failed store read also loses the row's agent.
var unknownReads = []struct{ name, reason string }{
	{"git status", "git status: fatal: index file corrupt"},
	{"merge-base", "git merge-base: fatal: unable to read tree"},
	{"rev-parse", "git rev-parse: fatal: not a git repository"},
	{"store", "store: file is not a database"},
}

// unknownLane is lane l as a failed read reports it: unknown, with reason, no task (and, the store unread, no agent).
func unknownLane(l port.Lane, reason string) port.Lane {
	u := port.Lane{Name: l.Name, Dir: l.Dir, State: port.LaneUnknown, Reason: reason, Agent: l.Agent}
	if strings.HasPrefix(reason, "store:") {
		u.Agent = ""
	}
	return u
}

// T-897 (RV-120 #1): a lane whose read failed is no observation of its task: the task's watchdog facts (a worker's
// question, a stopped worker, a pending notification retry) stay in force through it on each entry point that reaches
// decideLanes (the periodic check, the check on connect, a restart's first check), and once the read recovers obor
// resume still takes them. A restart with the lane unknown keeps the folded dispatch's task on it.
func TestUnknownLaneKeepsItsTasksFacts(t *testing.T) {
	t.Parallel()
	for _, fact := range []string{"asks", "stopped"} {
		for _, entry := range []string{"tick", "connect", "restart"} {
			for _, read := range unknownReads {
				t.Run(fact+"/"+entry+"/"+read.name, func(t *testing.T) {
					t.Parallel()
					h, p := askSeed(t)
					known := h.lanes.lanes[0]
					switch fact {
					case "asks":
						if _, err := h.a.ask(p, "T-1", known, "may I edit x?"); err != nil {
							t.Fatal(err)
						}
					case "stopped":
						h.a.record(p, event.Record{Task: "T-1", Step: event.StepNeedsYou, Kind: event.KindFact},
							event.FactData{Lane: "L-01", Reason: "stopped: rate_limit", Cause: causeStopped})
					}
					h.tick(time.Minute) // the fact in force
					inForce := func() bool {
						if fact == "stopped" {
							return p.stops("T-1")
						}
						return p.asks("T-1")
					}
					if !inForce() || p.tasks["T-1"] == nil {
						t.Fatalf("precondition: %s not in force or T-1 unwatched: %v %v", fact, p.folded.active["T-1"], p.tasks)
					}
					before := h.lastSeq()

					// one failing read
					h.lanes.lanes[0] = unknownLane(known, read.reason)
					switch entry {
					case "tick":
						h.tick(time.Minute)
					case "connect":
						h.connect()
					case "restart":
						p.log.Close()
						h.a.closeLog()
						p = reloadProj(t, h)
						h.a.mu.Lock()
						w := p.tasks["T-1"]
						h.a.mu.Unlock()
						if w == nil || w.lane.Name != "L-01" || w.lane.Task != "T-1" {
							t.Errorf("restart: the folded dispatch's task is not kept on the unknown lane: %+v", w)
						}
						h.tick(time.Minute)
					}
					if p != h.a.p {
						t.Fatal("precondition: the project checked is not the one loaded")
					}
					if !inForce() {
						t.Errorf("%s: the failed read ended the %s fact: %v", entry, fact, p.folded.active["T-1"])
					}
					if w := p.tasks["T-1"]; w == nil || !w.unread() || w.lane.Task != "T-1" {
						t.Errorf("%s: T-1 not kept on its unknown lane: %+v", entry, w)
					}
					if got := h.facts(before); len(got) != 0 {
						t.Errorf("%s: facts recorded while the lane is unknown: %v", entry, got)
					}

					// the read recovers
					h.lanes.lanes[0] = known
					h.tick(time.Minute)
					if !inForce() {
						t.Fatalf("recovered: the %s fact is not in force: %v", fact, p.folded.active["T-1"])
					}
					h.a.mu.Lock()
					act, _, err := p.resumeFits(*p.folded.loop["T-1"])
					h.a.mu.Unlock()
					want := actAnswer
					if fact == "stopped" {
						want = actLine
					}
					if err != nil || act != want {
						t.Fatalf("recovered: obor resume fits %q, %v; want %q", act, err, want)
					}
					if fact != "stopped" {
						if _, err := h.a.resume(wire.ResumeReq{Envelope: wire.Envelope{Root: h.root, Role: "lead",
							Agent: "pj-lead", CallID: "a1"}, Task: "T-1", Text: "yes, edit x"}); err != nil {
							t.Fatalf("recovered: obor resume refused: %v", err)
						}
						if p.asks("T-1") {
							t.Error("recovered: obor resume left the question in force")
						}
					}
				})
			}
		}
	}
}

// T-897: an unknown lane is not judged: its pane closing while its read fails records nothing (no needs-you{pane
// exited}); once the read recovers the watchdog sees the closed pane.
func TestUnknownLaneIsNotJudged(t *testing.T) {
	t.Parallel()
	h, p := askSeed(t)
	h.tick(time.Minute)
	if w := p.tasks["T-1"]; w == nil || w.agent.ID != "w1:p1" {
		t.Fatalf("precondition: T-1's worker not seen in w1:p1: %+v", w)
	}
	known := h.lanes.lanes[0]
	before := h.lastSeq()
	h.lanes.lanes[0] = unknownLane(known, unknownReads[0].reason)
	h.remove("w1:p1", "closed")
	h.tick(time.Minute)
	h.tick(11 * time.Minute)
	if got := h.facts(before); len(got) != 0 {
		t.Errorf("facts recorded on the unknown lane: %v", got)
	}
	h.lanes.lanes[0] = known
	h.tick(time.Minute)
	if !p.has("T-1", event.StepNeedsYou, "pane exited") {
		t.Errorf("recovered: the closed pane is not seen: %v", h.facts(before))
	}
}
