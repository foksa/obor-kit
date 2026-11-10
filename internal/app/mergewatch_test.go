package app

import (
	"slices"
	"testing"

	"obor/internal/app/event"
	"obor/internal/app/wire"
)

// The merge watch's rule, one table (condition → action). A watch whose set all merged, or one of it dropped,
// gets one line naming it (its note after it), and the line ends the watch; an unwatched one or one not ended gets
// none; a restart, or an older line (message{watch}, no of), sends it no second time.
func TestWatchRule(t *testing.T) {
	t.Parallel()
	watchR := rule{name: ruleWatch, when: watchRule}
	pass := passOf(watchR, 0)
	watch := func(set, note string) lineStep {
		return func(t *testing.T, h *harness) {
			addRecord(t, h, event.WatchTask, event.StepWatch, event.KindFact, WatchData{Task: set, By: "pj-lead", Note: note})
			if h.proj().openWatch(set) == nil {
				t.Fatalf("precondition: no open watch of %s", set)
			}
		}
	}
	unwatch := func(t *testing.T, h *harness) {
		addRecord(t, h, event.WatchTask, event.StepUnwatch, event.KindFact, WatchData{Task: "T-1", By: "pj-lead"})
	}
	dropped := func(task, why string) lineStep {
		return func(t *testing.T, h *harness) {
			addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
			addRecord(t, h, task, event.StepDropped, event.KindFact, event.FactData{Reason: why, Merge: true})
		}
	}
	oldLine := func(t *testing.T, h *harness) { // T-1's watch line as an older obor recorded it
		w := h.proj().folded.watches["T-1"]
		addRecord(t, h, event.MsgTask, event.StepMessage, event.KindFact, map[string]any{"to": "pj-lead", "from": "obor",
			"line": "[obor] T-1 merged", "task": "T-1", "watch": w.seq})
	}
	const sha1, sha2 = "1111111aaaa", "2222222bbbb"
	cases := []struct {
		name  string
		steps []lineStep
		lines []string
		open  bool // T-1's watch (or its set's) open after the last step
	}{
		{name: "not ended: no line", steps: []lineStep{watch("T-1", ""), pass, pass}, open: true},
		{name: "a single task merged: its line with the note, the watch ended",
			steps: []lineStep{watch("T-1", "release v1"), pass, taskMerged("T-1", sha1), pass, pass},
			lines: []string{"[obor] T-1 merged 1111111 — release v1"}},
		{name: "a set: one line once all merged",
			steps: []lineStep{watch("T-1,T-2", ""), taskMerged("T-1", sha1), pass, taskMerged("T-2", sha2), pass, pass},
			lines: []string{"[obor] T-1,T-2 merged (2222222)"}},
		{name: "one of the set dropped: its line, the watch ended",
			steps: []lineStep{watch("T-1,T-2", ""), dropped("T-2", "superseded\nby T-3"), pass, taskMerged("T-1", sha1),
				pass},
			lines: []string{"[obor] T-2 dropped: superseded by T-3 (watch T-1,T-2 ended)"}},
		{name: "unwatched (obor watch --rm): no line",
			steps: []lineStep{watch("T-1", ""), unwatch, taskMerged("T-1", sha1), pass, pass}},
		{name: "a restart after the line: not sent again",
			steps: []lineStep{watch("T-1", ""), taskMerged("T-1", sha1), pass, restartRig, pass, pass},
			lines: []string{"[obor] T-1 merged 1111111"}},
		{name: "a line recorded without of: not sent again",
			steps: []lineStep{watch("T-1", ""), taskMerged("T-1", sha1), oldLine, restartRig, pass, pass}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			for _, s := range c.steps {
				s(t, h)
			}
			_, lines := ruleLines(t, h)
			if !slices.Equal(lines, c.lines) {
				t.Errorf("lines %q, want %q", lines, c.lines)
			}
			p := h.proj()
			open := p.openWatch("T-1") != nil || p.openWatch("T-1,T-2") != nil
			if open != c.open || open != (len(p.openWatches()) > 0) {
				t.Errorf("watch open %v (open watches %v), want %v", open, p.openWatches(), c.open)
			}
		})
	}
}

// A watch's note belongs to its watch, not to the task: the open watch's line carries it, and a
// later watch of the same task (a new call id) gets the outcome alone, before a restart and after one (a replay
// keeps the watch closed).
func TestWatchNoteEndsWithItsLine(t *testing.T) {
	t.Parallel()
	h := ruleHarness(t)
	watchR := rule{name: ruleWatch, when: watchRule}
	pass := passOf(watchR, 0)
	const sha = "1111111aaaa"
	watchAgain := func(callID string) string {
		t.Helper()
		reply, err := h.a.watchCall(wire.WatchReq{Envelope: wire.Envelope{Root: h.root, Role: "lead", Agent: "pj-lead",
			CallID: callID}, Task: "T-1"})
		if err != nil {
			t.Fatalf("watch %s: %v", callID, err)
		}
		return reply
	}
	addRecord(t, h, event.WatchTask, event.StepWatch, event.KindFact, WatchData{Task: "T-1", By: "pj-lead",
		Note: "release v1"})
	if w := h.proj().openWatch("T-1"); w == nil || w.note != "release v1" {
		t.Fatalf("precondition: open watch %+v, want T-1 with its note", w)
	}
	taskMerged("T-1", sha)(t, h)
	pass(t, h)
	pass(t, h)
	const line = "[obor] T-1 merged 1111111 — release v1"
	if _, lines := ruleLines(t, h); !slices.Equal(lines, []string{line}) {
		t.Fatalf("lines %q, want the open watch's line %q with its note", lines, line)
	}
	const alone = "T-1 merged 1111111"
	if reply := watchAgain("w2"); reply != alone {
		t.Errorf("a watch after the line: %q, want %q (its note was already acted on)", reply, alone)
	}
	restartRig(t, h)
	if reply := watchAgain("w3"); reply != alone {
		t.Errorf("a watch after a replay: %q, want %q", reply, alone)
	}
}
