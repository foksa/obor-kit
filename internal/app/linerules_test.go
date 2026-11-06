package app

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"obor/internal/app/event"
)

// lineStep is a step of a line rule's table: a record, a rule pass, a delivery, a restart.
type lineStep func(t *testing.T, h *harness)

// passOf is a pass of rule r alone after the clock moved d.
func passOf(r rule, d time.Duration) lineStep {
	return func(_ *testing.T, h *harness) {
		h.now = h.now.Add(d)
		h.a.rulePass(context.Background(), []rule{r}, &Loop{})
	}
}

// deliverLines is the delivery of every rule line waiting: its post's result recorded now.
func deliverLines(t *testing.T, h *harness) {
	p := h.proj()
	var seqs []int64
	for _, q := range p.queuedLines() {
		if q.msg.Of.Kind != "" {
			seqs = append(seqs, q.seq)
		}
	}
	if len(seqs) == 0 {
		t.Fatal("precondition: no rule line waits to be delivered")
	}
	for _, seq := range seqs {
		addRecord(t, h, event.MsgTask, stepDeliver+strconv.FormatInt(seq, 10), event.KindResult, "sent")
	}
}

// restartRig is serve's restart: the log closed, the project loaded from it again.
func restartRig(t *testing.T, h *harness) {
	h.a.closeLog()
	p, err := h.a.load(h.root)
	if err != nil {
		t.Fatal(err)
	}
	h.a.mu.Lock()
	h.a.p = p
	h.a.mu.Unlock()
	if !p.merging || p.out.lead() != "pj-lead" {
		t.Fatalf("precondition: after the restart merging %v, lead %q", p.merging, p.out.lead())
	}
}

// taskMerged is task submitted and merged at head (outside the app when head is ""), no after-merge step left.
func taskMerged(task, head string) lineStep {
	return func(t *testing.T, h *harness) {
		addRecord(t, h, task, event.StepSubmitted, event.KindFact, submitData{Lane: "L-01"})
		addRecord(t, h, task, event.StepMerged, event.KindFact, event.MergedData{Head: head, Outside: head == ""})
	}
}

// T-927: the needs-you line's rule, one table (condition → action). The first line at once; a repeat 30 min after
// the first line's delivery, then 60 after each repeat's, never while the last line waits; nothing once the needs-you
// is answered, for a closed task's watchdog fact or for the project's own lines' needs-you; a restart sends nothing
// again; a line recorded before T-927 (message{needs}, no of) counts as the rule's.
func TestNeedsRule(t *testing.T) {
	t.Parallel()
	needs := rule{name: ruleNeeds, when: needsRule}
	pass := func(d time.Duration) lineStep { return passOf(needs, d) }
	fact := func(task string) lineStep {
		return func(t *testing.T, h *harness) {
			addRecord(t, h, task, event.StepNeedsYou, event.KindFact, event.FactData{Lane: "L-01", Cause: "lane",
				Reason: "lane: lane_setup\nboom"})
			if !slices.ContainsFunc(h.proj().needsYou(), func(r event.Record) bool { return r.Task == task }) {
				t.Fatalf("precondition: %s's needs-you not in force", task)
			}
		}
	}
	resumed := func(t *testing.T, h *harness) {
		addRecord(t, h, "T-1", event.StepResumed, event.KindFact, event.FactData{Reason: "retry"})
		if len(h.proj().needsYou()) != 0 {
			t.Fatal("precondition: the needs-you is still in force after the resume")
		}
	}
	lined := func(n int) lineStep { // n lines recorded so far
		return func(t *testing.T, h *harness) {
			if ofs, lines := ruleLines(t, h); len(ofs) != n {
				t.Fatalf("at %s: lines %q, want %d", h.now.Format(time.Kitchen), lines, n)
			}
		}
	}
	closedStale := func(t *testing.T, h *harness) { // T-1 merged with its watchdog fact still in force (T-635)
		p := h.proj()
		r := p.folded.active["T-1"]
		taskMerged("T-1", "")(t, h)
		p.folded.active["T-1"] = r
		if !p.closed("T-1") || len(p.needsYou()) != 1 {
			t.Fatalf("precondition: T-1 closed %v, needs-yous in force %v; want closed with its fact", p.closed("T-1"),
				p.needsYou())
		}
	}
	oldLine := func(t *testing.T, h *harness) { // a line of T-1's needs-you as obor before T-927 recorded it
		seq := h.proj().needsYou()[0].Seq
		addRecord(t, h, event.MsgTask, event.StepMessage, event.KindFact, map[string]any{"to": "pj-lead", "from": "obor",
			"line": "[obor] needs-you T-1 (L-01): old", "needs": seq})
	}
	const first = "[obor] needs-you T-1 (L-01): lane: lane_setup boom"
	cases := []struct {
		name  string
		steps []lineStep
		lines []string // the rule's lines: each its task and how it ends ("" the first, else the repeat's suffix)
		ns    []int    // their line numbers (the did key's)
	}{
		{name: "the first line at once, once", steps: []lineStep{fact("T-1"), pass(0), pass(0), pass(time.Minute)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "no repeat at 29 min after its delivery",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, pass(29 * time.Minute), lined(1)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "the repeat at 30 min after the first line's delivery, not its record",
			steps: []lineStep{fact("T-1"), pass(0), pass(10 * time.Minute), deliverLines, pass(29 * time.Minute),
				pass(time.Minute)},
			lines: []string{"T-1", "T-1 (again, 40m)"}, ns: []int{0, 1}},
		{name: "the next 60 min after the repeat's delivery",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, pass(30 * time.Minute), deliverLines,
				pass(59 * time.Minute), lined(2), pass(time.Minute), pass(0)},
			lines: []string{"T-1", "T-1 (again, 30m)", "T-1 (again, 1h)"}, ns: []int{0, 1, 2}},
		{name: "the last line undelivered: no repeat",
			steps: []lineStep{fact("T-1"), pass(0), pass(2 * time.Hour), deliverLines, pass(30 * time.Minute),
				pass(5 * time.Hour)},
			lines: []string{"T-1", "T-1 (again, 2h)"}, ns: []int{0, 1}},
		{name: "answered (resumed): no further line",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, resumed, pass(30 * time.Minute), pass(2 * time.Hour)},
			lines: []string{"T-1"}, ns: []int{0}},
		{name: "a closed task's watchdog fact: no line",
			steps: []lineStep{fact("T-1"), closedStale, pass(0), pass(time.Hour)}},
		{name: "the project's own lines' needs-you: no line", steps: []lineStep{fact(event.MsgTask), pass(0)}},
		{name: "two needs-yous: one line each, the oldest first",
			steps: []lineStep{fact("T-1"), fact("T-2"), pass(0), pass(0), pass(0)},
			lines: []string{"T-1", "T-2"}, ns: []int{0, 0}},
		{name: "a restart sends no line again and keeps the clock",
			steps: []lineStep{fact("T-1"), pass(0), deliverLines, restartRig, pass(0), pass(29 * time.Minute),
				restartRig, pass(time.Minute), pass(0)},
			lines: []string{"T-1", "T-1 (again, 30m)"}, ns: []int{0, 1}},
		{name: "a line recorded before T-927: no second first line, its repeat counts from its delivery",
			steps: []lineStep{fact("T-1"), oldLine, deliverLines, restartRig, pass(0), pass(29 * time.Minute),
				pass(time.Minute)},
			lines: []string{"T-1 (again, 30m)"}, ns: []int{1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := ruleHarness(t)
			for _, s := range c.steps {
				s(t, h)
			}
			ofs, lines := ruleLines(t, h)
			var got []string
			var ns []int
			for i, of := range ofs {
				if of.Kind != ruleNeeds {
					t.Fatalf("a line of rule %s: %q", of.Kind, lines[i])
				}
				task, _, _ := strings.Cut(strings.TrimPrefix(lines[i], "[obor] needs-you "), " ")
				body := strings.Replace(lines[i], "needs-you "+task, "needs-you T-1", 1)
				if !strings.HasPrefix(body, first+" → ") {
					t.Errorf("line %q, want %q and the commands that answer it", lines[i], first)
				}
				if _, again, ok := strings.Cut(lines[i], " (again, "); ok {
					task += " (again, " + again
				}
				got, ns = append(got, task), append(ns, int(of.Seq%needsSpan))
			}
			if !slices.Equal(got, c.lines) || !slices.Equal(ns, c.ns) {
				t.Errorf("lines %q (line numbers %v), want %q (%v)", got, ns, c.lines, c.ns)
			}
		})
	}
}

// T-927: the merge watch's rule, one table (condition → action). A watch whose set all merged, or one of it dropped,
// gets one line naming it (its note after it), and the line ends the watch; an unwatched one or one not ended gets
// none; a restart, or a line recorded before T-927 (message{watch}, no of), sends it no second time.
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
	oldLine := func(t *testing.T, h *harness) { // T-1's watch line as obor before T-927 recorded it
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
		{name: "a line recorded before T-927: not sent again",
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
