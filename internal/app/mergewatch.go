package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"obor/internal/app/event"
	"obor/internal/app/wire"
	"sort"
	"strings"
	"time"
)

// Watches of a set of tasks' merges (T-523, T-528; docs/state-model.md row 49): clean merges stay quiet for the lead
// (docs/decisions.md "Lead noise"), so a lead that must act on a merge subscribes to it. `obor watch <T-xx>` or
// `obor watch T-a,T-b` (the lead or the owner) records fact watch{task: the comma list, by} on the task key watch; a
// single task is a set of one. Once every task of the set merged, or the first one is dropped or rejected, the watch
// rule (watchRule, T-927) records the lead line (outcome: "[obor] T-xx merged <short sha>", "[obor] T-a,T-b merged
// (<last short sha>)", "[obor] T-b dropped: <why>" with " (watch T-a,T-b ended)" for a set; fact message{of: watch
// and the watch's seq}, delivered as every lead line, messages.go); that line recorded (its did key) ends the watch.
// A watch may carry the lead's note (T-666): what it meant to do when the set ends,
// which the line, `obor watch` and `obor status` add after the outcome. `obor watch --rm <list>` ends one
// (fact unwatch); `obor watch` lists the open ones.

// WatchData is a watch fact's data: the task or comma list watched, who watches it, the lead's note (T-666: what it
// meant to do when the set ends; one line), and the call that recorded it with its reply.
type WatchData struct {
	Task  string `json:"task"`
	By    string `json:"by,omitempty"`
	Note  string `json:"note,omitempty"`
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
}

// mergeWatch is a set's last watch: its fact's seq and time, who watches, its note. It is open until its line is
// recorded (openWatch).
type mergeWatch struct {
	seq  int64
	at   time.Time
	by   string
	note string
}

// foldWatch folds r, a record of the project's watches.
func (p *proj) foldWatch(r event.Record) {
	if r.Task != event.WatchTask || r.Kind != event.KindFact || r.Step != event.StepWatch && r.Step != event.StepUnwatch {
		return
	}
	var d WatchData
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(wire.MethodWatch, d.Call)] = d.Reply
	}
	if p.folded.watches == nil {
		p.folded.watches = map[string]*mergeWatch{}
	}
	delete(p.folded.watches, d.Task)
	if r.Step == event.StepWatch {
		p.folded.watches[d.Task] = &mergeWatch{seq: r.Seq, at: event.RecordTime(r), by: d.By, note: d.Note}
	}
}

// openWatch is set's open watch: watched, its line not recorded (nil: none; under mu).
func (p *proj) openWatch(set string) *mergeWatch {
	w := p.folded.watches[set]
	if w == nil {
		return nil
	}
	if _, did := p.folded.did[event.Of{Kind: ruleWatch, Seq: w.seq}]; did {
		return nil
	}
	return w
}

// openWatches is the sets of p's open watches, oldest first (under mu).
func (p *proj) openWatches() []string {
	var sets []string
	for set := range p.folded.watches {
		if p.openWatch(set) != nil {
			sets = append(sets, set)
		}
	}
	sort.Slice(sets, func(i, j int) bool { return p.folded.watches[sets[i]].seq < p.folded.watches[sets[j]].seq })
	return sets
}

// outcome is how the watch of set (a task or a comma list) ended in p, as its line names it after "[obor] ": its
// first task dropped or rejected, "T-b dropped: <why>" (a set adds " (watch <set> ended)"); every task merged,
// "T-xx merged <short sha>" or "T-a,T-b merged (<the last merge's short sha>)"; an open watch with a note (T-666)
// adds " — <note>" (T-929: a closed watch's note is gone, so a later watch gets the outcome alone). "" while it has
// not (under mu).
func (p *proj) outcome(set string) string {
	note := ""
	if w := p.openWatch(set); w != nil {
		note = w.note
	}
	tasks := strings.Split(set, ",")
	for _, task := range tasks {
		var end string
		switch mt := p.folded.loop[task]; {
		case mt == nil:
			continue
		case mt.state == stDropped:
			end = task + " dropped: " + strings.Join(strings.Fields(mt.dropped), " ")
		case mt.state == stRejected:
			end = task + " rejected: " + strings.Join(strings.Fields(mt.line), " ")
		default:
			continue
		}
		if len(tasks) > 1 {
			end += " (watch " + set + " ended)"
		}
		return withNote(end, note)
	}
	var last *mtask
	for _, task := range tasks {
		mt := p.folded.loop[task]
		if mt == nil || mt.state != stMerged {
			return ""
		}
		if last == nil || mt.mergedSeq > last.mergedSeq {
			last = mt
		}
	}
	sha := "(outside obor serve)"
	switch {
	case last.head != "" && len(tasks) > 1:
		sha = "(" + last.head[:min(len(last.head), 7)] + ")"
	case last.head != "":
		sha = last.head[:min(len(last.head), 7)]
	}
	return withNote(set+" merged "+sha, note)
}

// withNote is a watch's outcome line with its note after it (" — <note>"), or the line alone when it has none.
func withNote(line, note string) string {
	if note == "" {
		return line
	}
	return line + " — " + note
}

// waiting is the tasks of set not merged yet (under mu).
func (p *proj) waiting(set string) []string {
	var open []string
	for _, task := range strings.Split(set, ",") {
		if mt := p.folded.loop[task]; mt == nil || mt.state != stMerged {
			open = append(open, task)
		}
	}
	return open
}

// watchCall answers the socket's watch request (the lead or the owner): a call id is required and a repeated one gets
// the stored reply. A task or comma list: WATCHING <set> (fact watch, with Call.Note when the lead gave one, one
// line); ended already (all merged, one dropped or rejected), its outcome at once and nothing recorded; a task
// neither dispatched nor specified, refused. --rm: UNWATCHED <set> (fact unwatch), or NOT WATCHING <set>. No task:
// the open watches (watchList; NO WATCHES).
func (a *App) watchCall(c wire.Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(wire.MethodWatch + ": a call id is required")
	}
	p, reply, stored := a.callProj(wire.MethodWatch, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", wire.MethodWatch, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	}
	if err := leadOr(p.root, wire.MethodWatch, c, fmt.Sprintf("watch: the lead or the owner watches, not %s (role %s)", Dash(c.Agent),
		c.Role)); err != nil {
		return "", err
	}
	switch {
	case c.Task == "" && c.Clear:
		return "", errors.New("watch: --rm needs a task")
	case c.Task == "":
		return a.watchList(p), nil
	}
	tasks := strings.Split(c.Task, ",")
	a.mu.Lock()
	end, w := p.outcome(c.Task), p.openWatch(c.Task)
	var unknown []string
	for _, task := range tasks {
		if p.folded.loop[task] == nil {
			unknown = append(unknown, task)
		}
	}
	var since time.Duration
	if w != nil {
		since = a.opt.Now().Sub(w.at)
	}
	a.mu.Unlock()
	if c.Clear {
		unknown = nil
	}
	for _, task := range unknown {
		_, ok, err := specOf(p, task)
		if err != nil {
			return "", fmt.Errorf("watch: %v", err)
		}
		if !ok {
			return "", fmt.Errorf("watch: unknown task %s (no dispatch or spec in %s)", task, p.name)
		}
	}
	by := c.Agent
	if by == "" {
		by = "owner"
	}
	d := WatchData{Task: c.Task, By: by, Note: strings.Join(strings.Fields(c.Note), " "), Call: c.CallID,
		Reply: "WATCHING " + c.Task}
	step := event.StepWatch
	switch {
	case c.Clear && w == nil:
		return "NOT WATCHING " + c.Task, nil
	case c.Clear:
		step, d.Reply = event.StepUnwatch, "UNWATCHED "+c.Task
	case end != "":
		return end, nil
	case w != nil:
		return "WATCHING " + c.Task + " (since " + Age(since) + ")", nil
	}
	if !a.record(p, event.Record{Task: event.WatchTask, Step: step, Kind: event.KindFact}, d) {
		return "", fmt.Errorf("watch: the event log of %s refused it", p.name)
	}
	a.wake() // a task that ended meanwhile gets its line at once
	return d.Reply, nil
}

// watchList is p's open watches as obor watch lists them, oldest first: WATCH <set> since <age>, a set of more than
// one adding ", waiting <its tasks not merged yet>", and a watch with a note " — <note>".
func (a *App) watchList(p *proj) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opt.Now()
	sets := p.openWatches()
	if len(sets) == 0 {
		return "NO WATCHES"
	}
	lines := make([]string, 0, len(sets))
	for _, set := range sets {
		line := "WATCH " + set + " since " + Age(now.Sub(p.folded.watches[set].at))
		if strings.Contains(set, ",") {
			line += ", waiting " + strings.Join(p.waiting(set), ",")
		}
		lines = append(lines, withNote(line, p.folded.watches[set].note))
	}
	return strings.Join(lines, "\n")
}

// ruleWatch is the merge watch's rule in the rule pass (rules.go, T-927).
const ruleWatch = "watch"

// watchRule lines p's lead once per open watch that ended (outcome), the oldest first. Key: the watch fact's seq; the
// line recorded (its did key) ends the watch.
func watchRule(_ context.Context, a *App, p *proj, _ *Loop, _ time.Time) (int64, action, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p.out.lead() == "" {
		return 0, action{}, false
	}
	for _, set := range p.openWatches() {
		if end := p.outcome(set); end != "" {
			return p.folded.watches[set].seq, action{line: "[obor] " + end}, true
		}
	}
	return 0, action{}, false
}
