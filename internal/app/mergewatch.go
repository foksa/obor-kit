package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Watches of a set of tasks' merges (T-523, T-528; docs/state-model.md row 49): clean merges stay quiet for the lead
// (docs/decisions.md "Lead noise"), so a lead that must act on a merge subscribes to it. `obor watch <T-xx>` or
// `obor watch T-a,T-b` (the lead or the owner) records fact watch{task: the comma list, by} on the task key watch; a
// single task is a set of one. Once every task of the set merged, or the first one is dropped or rejected, watchLines
// records the lead line (outcome: "[obor] T-xx merged <short sha>", "[obor] T-a,T-b merged (<last short sha>)",
// "[obor] T-b dropped: <why>" with " (watch T-a,T-b ended)" for a set; fact message{task: the list, watch: the
// watch's seq}, delivered as every lead line, messages.go); its fold ends the watch, so a replay finds the line
// recorded and queues none again. A watch may carry the lead's note (T-666): what it meant to do when the set ends,
// which the line, `obor watch`, `obor status` and the monitor add after the outcome. `obor watch --rm <list>` ends one
// (fact unwatch); `obor watch` lists the open ones.

// MethodWatch is the app socket's watch request: Call.Task the task or comma list watched (""; the open watches
// listed), Call.Clear ends its watch.
const MethodWatch = "watch"

// WatchData is a watch fact's data: the task or comma list watched, who watches it, the lead's note (T-666: what it
// meant to do when the set ends; one line), and the call that recorded it with its reply.
type WatchData struct {
	Task  string `json:"task"`
	By    string `json:"by,omitempty"`
	Note  string `json:"note,omitempty"`
	Call  string `json:"call,omitempty"`
	Reply string `json:"reply,omitempty"`
}

// mergeWatch is an open watch: its fact's seq and time, who watches, its note, and whether a pass is recording its
// line now (watchLines' claim, not folded).
type mergeWatch struct {
	seq    int64
	at     time.Time
	by     string
	note   string
	lining bool
}

// foldWatch folds r, a record of the project's watches.
func (p *proj) foldWatch(r Record) {
	if r.Task != WatchTask || r.Kind != KindFact || r.Step != StepWatch && r.Step != StepUnwatch {
		return
	}
	var d WatchData
	_ = json.Unmarshal(r.Data, &d)
	if d.Call != "" {
		p.folded.calls[verdictStep(MethodWatch, d.Call)] = d.Reply
	}
	if p.folded.watches == nil {
		p.folded.watches = map[string]*mergeWatch{}
	}
	delete(p.folded.watches, d.Task)
	if r.Step == StepWatch {
		p.folded.watches[d.Task] = &mergeWatch{seq: r.Seq, at: recordTime(r), by: d.By, note: d.Note}
	}
}

// watchEnded ends the watch of set whose fact is seq: its line is recorded (a message fact's fold, under mu).
func (p *proj) watchEnded(set string, seq int64) {
	if w := p.folded.watches[set]; w != nil && w.seq == seq {
		delete(p.folded.watches, set)
	}
}

// outcome is how the watch of set (a task or a comma list) ended in p, as its line names it after "[obor] ": its
// first task dropped or rejected, "T-b dropped: <why>" (a set adds " (watch <set> ended)"); every task merged,
// "T-xx merged <short sha>" or "T-a,T-b merged (<the last merge's short sha>)"; a watch with a note (T-666) adds
// " — <note>". "" while it has not (under mu).
func (p *proj) outcome(set string) string {
	note := ""
	if w := p.folded.watches[set]; w != nil {
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
func (a *App) watchCall(c Call) (string, error) {
	if c.CallID == "" {
		return "", errors.New(MethodWatch + ": a call id is required")
	}
	p, reply, stored := a.callProj(MethodWatch, c)
	switch {
	case p == nil:
		return "", fmt.Errorf("%s: no served project at %s", MethodWatch, c.Root)
	case stored:
		return reply, nil
	case !a.merges(p):
		return "", mergeOff(p)
	case c.Role != "" && c.Role != "lead":
		return "", fmt.Errorf("watch: the lead or the owner watches, not %s (role %s)", Dash(c.Agent), c.Role)
	case c.Task == "" && c.Clear:
		return "", errors.New("watch: --rm needs a task")
	case c.Task == "":
		return a.watchList(p), nil
	}
	tasks := strings.Split(c.Task, ",")
	a.mu.Lock()
	end, w := p.outcome(c.Task), p.folded.watches[c.Task]
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
	step := StepWatch
	switch {
	case c.Clear && w == nil:
		return "NOT WATCHING " + c.Task, nil
	case c.Clear:
		step, d.Reply = StepUnwatch, "UNWATCHED "+c.Task
	case end != "":
		return end, nil
	case w != nil:
		return "WATCHING " + c.Task + " (since " + Age(since) + ")", nil
	}
	if !a.record(p, Record{Task: WatchTask, Step: step, Kind: KindFact}, d) {
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
	var sets []string
	for set := range p.folded.watches {
		sets = append(sets, set)
	}
	sort.Slice(sets, func(i, j int) bool { return p.folded.watches[sets[i]].seq < p.folded.watches[sets[j]].seq })
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

// watchLines records, once per watch that ended (outcome), the lead's line naming it; the line's fold ends the
// watch. A pass claims each watch under mu before it records (as needsLines: the loop's notify
// and the watchdog's run at once); a refused record leaves it open, so a later pass tries again.
func (a *App) watchLines() {
	type todo struct {
		set  string
		w    *mergeWatch
		line string
	}
	var out []todo
	a.mu.Lock()
	p := a.p
	if p != nil && p.merging && p.out.lead() != "" {
		for set, w := range p.folded.watches {
			if end := p.outcome(set); end != "" && !w.lining {
				w.lining = true
				out = append(out, todo{set, w, "[obor] " + end})
			}
		}
	}
	a.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].w.seq < out[j].w.seq })
	queued := false
	for _, t := range out {
		m := Message{To: p.out.lead(), From: "obor", Line: t.line, Task: t.set, Watch: t.w.seq}
		if a.record(p, Record{Task: MsgTask, Step: StepMessage, Kind: KindFact}, m) {
			queued = true
		}
		a.mu.Lock()
		t.w.lining = false
		a.mu.Unlock()
	}
	if queued {
		a.wake()
	}
}
