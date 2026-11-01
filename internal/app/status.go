package app

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Status is the answer to the app socket's one request, status.
type Status struct {
	Since     string         `json:"since,omitempty"`   // when this serve started (RFC 3339)
	Connected bool           `json:"connected"`         // the herdr subscription is live
	Project   *ProjectStatus `json:"project,omitempty"` // the served project once loaded (nil before)
}

// ProjectStatus is the served project: its observed agents, whether the merge loop runs for it and its open tasks,
// the facts in force (the watchdogs' and the merge loop's needs-you) and the lead's open watches. LogError is its
// event log's last append error (cleared by a good append): every step that needs a record fails while it stays.
type ProjectStatus struct {
	Name     string        `json:"name"`
	Root     string        `json:"root"`
	Merging  bool          `json:"merging"`
	Agents   []AgentStatus `json:"agents"`
	Tasks    []TaskStatus  `json:"tasks"`
	Facts    []Record      `json:"facts"`
	LogError string        `json:"log_error,omitempty"`
	Paused   *Pause        `json:"paused,omitempty"` // the pause in force (pause.go)
	// Watches is the lead's open watches (mergewatch.go), oldest first: obor monitor lists them (T-658).
	Watches []WatchStatus `json:"watches,omitempty"`
	// Messages is the lines not delivered yet (messages.go): obor send's, obor brief's, a brief's finished line.
	Messages []MessageStatus `json:"messages,omitempty"`
	// Review is the open review request, else "review due: …" when more than 3 tasks merged since the last review
	// (review.go; "": neither).
	Review string `json:"review,omitempty"`
	// Answers is, by a needs-you fact's seq, the commands that answer it in its task's state (answerPath; none: "").
	Answers map[int64]string `json:"answers,omitempty"`
	// Running is what serve does for the project without an agent now (T-657): its command steps' open intents and a
	// pending self-restart, oldest first.
	Running []RunningStep `json:"running"`
}

// StepServeRestart is the running line's item for a pending self-restart (T-657): serve ends itself on the new build
// once after_merge ran green (T-652).
const StepServeRestart = "restart"

// RunningStep is one step that runs without an agent, as obor status' running line lists it (T-657): the merge loop's
// gate, publish (the merge) or after_merge, its nightly check, or a pending self-restart. Since is when its intent was
// written (Unix seconds): the elapsed time the line shows.
type RunningStep struct {
	Step  string `json:"step"`
	Task  string `json:"task,omitempty"`
	Since int64  `json:"since"`
}

// WatchStatus is one of the lead's open watches: the task or comma list watched, who watches it, and since when
// (Unix seconds, its fact's time).
type WatchStatus struct {
	Task  string `json:"task"`
	By    string `json:"by,omitempty"`
	Since int64  `json:"since"`
}

// TaskStatus is one task of the merge loop that is not merged.
type TaskStatus struct {
	Task    string   `json:"task"`
	Lane    string   `json:"lane"`
	Attempt int      `json:"attempt"`
	State   string   `json:"state"`
	Running string   `json:"running,omitempty"` // the step whose effect runs now in its own goroutine (gate, publish)
	After   []string `json:"after,omitempty"`   // waiting: the tasks it waits for
	Why     string   `json:"why,omitempty"`     // waiting, all it waits for merged: its dispatch's last refusal
	Level   string   `json:"level,omitempty"`   // waiting: the difficulty level its lane types came from
	Types   []string `json:"types,omitempty"`   // waiting: the lane types it takes
}

// AgentStatus is one observed agent: its address, lane and task (when its cwd is a lane) and status for Age seconds.
type AgentStatus struct {
	Machine   string `json:"machine"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Name      string `json:"name,omitempty"`
	Agent     string `json:"agent"`
	Lane      string `json:"lane,omitempty"`
	Task      string `json:"task,omitempty"`
	Status    string `json:"status"`
	Age       int64  `json:"age"`
	// The agent's current recorded session (sessions.go, T-577): its id, transcript file and start (Unix seconds);
	// none while the pane's agent is not identified or has none recorded.
	Session    string `json:"session,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Started    int64  `json:"started,omitempty"`
}

// Status is the app's state now.
func (a *App) Status() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.opt.Now()
	st := Status{Since: a.since.UTC().Format(time.RFC3339), Connected: a.connected}
	if p := a.p; p != nil {
		ps := ProjectStatus{Name: p.name, Root: p.root, Merging: p.merging, Agents: []AgentStatus{}, Tasks: []TaskStatus{},
			Facts: []Record{}}
		if err := p.log.Err(); err != nil {
			ps.LogError = err.Error()
		} else if p.folded.lr.err != nil {
			ps.LogError = "lane rows: " + p.folded.lr.err.Error()
		}
		for _, o := range a.obs {
			if o.pane.Agent == "" || a.projectOf(o.pane) != p {
				continue
			}
			as := AgentStatus{Machine: Machine, Workspace: a.spaces[o.pane.Workspace], Pane: o.pane.ID, Name: a.shownAs(p, o.pane),
				Agent: o.pane.Agent, Status: o.pane.Status, Age: int64(now.Sub(o.since) / time.Second)}
			if l, ok := p.laneOf(o.pane.Cwd); ok {
				as.Lane, as.Task = l.Name, l.Task
			}
			if agent, ok := p.reg.whoIs(o.pane); ok {
				if s, ok := p.session(agent); ok {
					as.Session, as.Transcript, as.Started = s.ID, s.Transcript, s.Started
				}
			}
			ps.Agents = append(ps.Agents, as)
		}
		sort.Slice(ps.Agents, func(i, j int) bool { return ps.Agents[i].Pane < ps.Agents[j].Pane })
		for _, fs := range p.folded.active {
			ps.Facts = append(ps.Facts, fs...)
		}
		for _, mt := range p.folded.loop {
			if mt.needs.Seq != 0 {
				ps.Facts = append(ps.Facts, mt.needs)
			}
			if mt.state != stMerged {
				ts := TaskStatus{Task: mt.task, Lane: mt.lane, Attempt: mt.attempt, State: mt.state}
				if p.running.Task == mt.task {
					ts.Running = p.running.Step
				}
				if mt.state == stWaiting {
					ts.After, ts.Why = mt.disp.After, p.waitLine[mt.task].line
					ts.Level, ts.Types = mt.disp.Level, mt.disp.Types
				}
				ps.Tasks = append(ps.Tasks, ts)
			}
		}
		if held := p.paused(now); held != nil {
			c := *held
			ps.Paused = &c
		}
		ps.Messages = p.messageStatus(now)
		if s := p.reviewStatus(now); s != "" && p.folded.review != nil {
			ps.Review = "review request " + s
		} else {
			ps.Review = s
		}
		for _, f := range ps.Facts {
			if f.Step != StepNeedsYou {
				continue
			}
			if ans := p.answerPath(f); ans != "" {
				if ps.Answers == nil {
					ps.Answers = map[int64]string{}
				}
				ps.Answers[f.Seq] = ans
			}
		}
		ps.Running = runningSteps(p.log.OpenIntents(), p.restarting)
		sets := make([]string, 0, len(p.folded.watches))
		for set := range p.folded.watches {
			sets = append(sets, set)
		}
		sort.Slice(sets, func(i, j int) bool { return p.folded.watches[sets[i]].seq < p.folded.watches[sets[j]].seq })
		for _, set := range sets {
			w := p.folded.watches[set]
			ps.Watches = append(ps.Watches, WatchStatus{Task: set, By: w.by, Since: w.at.Unix()})
		}
		sort.Slice(ps.Tasks, func(i, j int) bool { return ps.Tasks[i].Task < ps.Tasks[j].Task })
		sort.Slice(ps.Facts, func(i, j int) bool { return ps.Facts[i].Seq < ps.Facts[j].Seq })
		st.Project = &ps
	}
	return st
}

// runningSteps is the steps that run without an agent, in seq order: the open intents of the merge loop's command
// steps (gate, publish the merge, after_merge, the nightly check) and, last, a pending self-restart (restarting not
// zero). An intent with a result is no running step; another step's intent (a prompt, a start) runs no command the
// line shows.
func runningSteps(open []Record, restarting time.Time) []RunningStep {
	var out []RunningStep
	for _, r := range open {
		step, task := r.Step, r.Task
		switch {
		case strings.HasPrefix(step, StepNightly+"."):
			step, task = StepNightly, ""
		default:
			if step, _ = stepN(step); step != StepGate && step != StepPublish && step != StepAfterMerge {
				continue
			}
		}
		out = append(out, RunningStep{Step: step, Task: task, Since: recordTime(r).Unix()})
	}
	if !restarting.IsZero() {
		out = append(out, RunningStep{Step: StepServeRestart, Since: restarting.Unix()})
	}
	return out
}

// answerPath is the commands that answer the needs-you fact r (obor status, the lead's needs-you line), each one its
// verdict takes in the state mt of r's task (zero: the loop holds none), by the rules the verdict itself checks: a
// worker's question obor answer; a merge-loop needs-you obor sendback when the task goes back (unsendable), obor resume
// by default (a hold's with the owner's decision), else with each mode it takes (resumeTo), and obor drop unless the
// task is merged (droppable); a stalled worker its pane or obor drop; a loop error none ("": fix what it names).
func answerPath(r Record, mt mtask) string {
	d := r.Fact()
	drop := `obor drop ` + cmp.Or(d.Lane, r.Task) + ` "<why>"` // a waiting task has no lane: obor drop takes its task
	switch {
	case r.Task == ReviewTask: // a failed review start's: a new request ends it
		return "obor review [--after T-a,T-b]"
	case projectKey(r.Task): // a GREEN nightly ends a nightly's; a line typed, a line's
		return ""
	case strings.HasPrefix(d.Reason, reasonAsks):
		return `obor answer ` + r.Task + ` "<text>"`
	case !d.Merge:
		if !mt.droppable(d.Lane != "") {
			return "its pane"
		}
		return "its pane, or " + drop
	}
	var cmds []string
	if mt.unsendable() == "" && d.Lane != "" { // obor sendback takes a lane: a waiting task's needs-you has none
		cmds = append(cmds, `obor sendback `+d.Lane+` "<why>"`)
	}
	resume := "obor resume " + r.Task
	if _, err := mt.resumeTo(r.Task, ""); err == nil && d.Cause == causeHeld { // the decision goes to the integrator's next job (T-511)
		cmds = append(cmds, resume+` ["<decision>"]`)
	} else if err == nil {
		cmds = append(cmds, resume)
	} else {
		for _, to := range []string{ResumeRegate, ResumeReprompt} { // no default: the owner says which (retry is a default's)
			if _, err := mt.resumeTo(r.Task, to); err == nil {
				cmds = append(cmds, resume+" "+to)
			}
		}
	}
	if mt.droppable(d.Lane != "") {
		cmds = append(cmds, drop)
	}
	return strings.Join(cmds, " | ")
}

// answerPath is answerPath of p's needs-you r in the state p's merge loop holds of its task (under mu).
func (p *proj) answerPath(r Record) string {
	var mt mtask
	if m := p.folded.loop[r.Task]; m != nil {
		mt = *m
	}
	return answerPath(r, mt)
}

// request and answer are the app socket's lines: {"method":"status"} → {"status":…} or {"error":…}; a verdict
// {"method":"accept","call":{…}} (or hold, resume, dispatch …) → {"reply":…} or {"error":…}.
type request struct {
	Method string `json:"method"`
	Call   *Call  `json:"call,omitempty"`
}

type answer struct {
	Status *Status `json:"status,omitempty"`
	Reply  string  `json:"reply,omitempty"`
	Error  string  `json:"error,omitempty"`
}

// requestTimeout bounds a socket request's effect (a resolved's Git). It outlasts the connection's 10 s: an effect
// the caller stopped waiting for still completes, its reply stored for the repeated call id.
const requestTimeout = time.Minute

// Listen answers the app socket's requests on ln until it is closed: one request line per connection. A request's
// effect runs under the daemon's lifetime and requestTimeout; once Serve has ended, a connection is closed unanswered.
// Serve binds the socket only once its project is loaded (cmd/obor/serve.go, Loaded; T-659): a request never waits
// for the replay, a client retries the socket that is not there yet inside its own deadline.
func (a *App) Listen(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		a.mu.Lock()
		stopped := a.stopped
		if !stopped {
			a.reqs.Add(1)
		}
		a.mu.Unlock()
		if stopped {
			c.Close()
			continue
		}
		go func() {
			defer a.reqs.Done()
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			b, err := bufio.NewReader(c).ReadBytes('\n')
			if err != nil {
				return
			}
			var req request
			var ans answer
			unreadable := json.Unmarshal(b, &req) != nil
			switch {
			case unreadable:
				ans.Error = "unreadable request"
			case req.Method == "status":
				st := a.Status()
				ans.Status = &st
			case req.Method == MethodSpecSet || req.Method == MethodPause || req.Method == MethodSend || req.Method == MethodBrief ||
				req.Method == MethodWatch || req.Method == MethodReview || req.Method == MethodStopped || req.Method == MethodTurn || req.Method == MethodSession:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				do := map[string]func(Call) (string, error){MethodSpecSet: a.specSet, MethodPause: a.pauseCall,
					MethodSend: a.sendCall, MethodBrief: a.briefCall, MethodWatch: a.watchCall, MethodReview: a.reviewCall,
					MethodStopped: a.stoppedCall, MethodTurn: a.turnCall, MethodSession: a.sessionCall}[req.Method]
				var err error
				if ans.Reply, err = do(*req.Call); err != nil {
					ans.Error = err.Error()
				}
			case req.Method == MethodBackup:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				var err error
				if ans.Reply, err = a.backup(*req.Call); err != nil {
					ans.Error = err.Error()
				}
			case req.Method == MethodAccept || req.Method == MethodSendback || req.Method == MethodAnswer || req.Method == MethodReject ||
				req.Method == MethodResolved || req.Method == MethodHold || req.Method == MethodResume || req.Method == MethodDrop ||
				req.Method == MethodDispatch || req.Method == MethodJobNext || req.Method == MethodTaskDone:
				if req.Call == nil {
					ans.Error = req.Method + ": no call"
					break
				}
				var reply string
				var err error
				switch req.Method {
				case MethodResume:
					reply, err = a.resume(*req.Call)
				case MethodDrop:
					reply, err = a.drop(*req.Call)
				case MethodJobNext:
					reply, err = a.jobNext(*req.Call)
				case MethodTaskDone:
					reply, err = a.taskDone(*req.Call)
				case MethodDispatch:
					ctx, cancel := context.WithTimeout(a.life, requestTimeout)
					reply, err = a.dispatchCall(ctx, *req.Call)
					cancel()
				default:
					ctx, cancel := context.WithTimeout(a.life, requestTimeout)
					reply, err = a.verdict(ctx, req.Method, *req.Call)
					cancel()
				}
				if err != nil {
					ans.Error = err.Error()
				}
				ans.Reply = reply
			default:
				ans.Error = "unknown method " + req.Method
			}
			out, _ := json.Marshal(ans)
			_, _ = c.Write(append(out, '\n'))
		}()
	}
}

// Ask is the status of the app listening on the unix socket at path.
func Ask(ctx context.Context, path string) (Status, error) {
	ans, err := ask(ctx, path, request{Method: "status"})
	if err != nil {
		return Status{}, err
	}
	if ans.Status == nil {
		return Status{}, errors.New("no status in the answer")
	}
	return *ans.Status, nil
}

// SocketPath is the app socket of the serve of the project at root: <root>/.obor/state/serve.sock (one serve per
// project, T-522).
func SocketPath(root string) string { return filepath.Join(root, ".obor", "state", "serve.sock") }

// NewCallID is a new call's id: a repeated call id gets the app's stored reply, so every call (a CLI request, an MCP
// tool call) has its own.
func NewCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Verdict sends the verdict call c (MethodAccept, …) to the app listening at path: its reply line, or the app's
// refusal as the error. The call has its method's deadline (callDeadline) or ctx's, the earlier.
func Verdict(ctx context.Context, path, method string, c Call) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callDeadline(method))
	defer cancel()
	ans, err := verdictAsk(ctx, path, request{Method: method, Call: &c})
	if err != nil {
		return "", err
	}
	return ans.Reply, nil
}

// The deadlines of the socket's callers, the one table (T-659): a call is retried inside its own deadline, so a serve
// restart that ends before it loses nothing. The hooks' are under their timeouts (10 s and 15 s); status has none: a
// read fails at once.
const (
	verdictDeadline     = 15 * time.Second // a CLI verdict, an MCP tool's call
	turnDeadline        = 8 * time.Second  // a hook's turn and SessionStart (session) calls
	stopFailureDeadline = 5 * time.Second  // the StopFailure hook's stopped call
)

// callDeadline is method's deadline from the table above.
func callDeadline(method string) time.Duration {
	switch method {
	case MethodTurn, MethodSession:
		return turnDeadline
	case MethodStopped:
		return stopFailureDeadline
	}
	return verdictDeadline
}

// ask's retries (T-659): every askRetryWait, flat, and none later than askMargin before the call's deadline, so the
// last attempt still has time to be answered.
const (
	askRetryWait = 500 * time.Millisecond
	askMargin    = 200 * time.Millisecond
)

// ask sends req once to the app socket at path and reads its answer; an answer's error is the error. It is not
// retried: obor status reports a serve that is not there at once.
func ask(ctx context.Context, path string, req request) (answer, error) {
	return askOnce(ctx, path, req)
}

// verdictAsk is ask for a verdict's request (a CLI verdict, an MCP tool's, a hook's: task_done, job_next, turn …),
// retried until ctx's deadline less askMargin while it cannot have done harm: a refused or missing socket carried
// nothing (serve binds only once its project is loaded, so a restart is a socket not there yet), and a reply lost
// after the request was written is sent again with the same call id, which the app's stored reply answers
// (docs/state-model.md, class F). Any other error, the app's refusal included, is returned at once.
func verdictAsk(ctx context.Context, path string, req request) (answer, error) {
	last := time.Now().Add(-askMargin) // no deadline: one attempt
	if d, ok := ctx.Deadline(); ok {
		last = d.Add(-askMargin)
	}
	for {
		ans, err := askOnce(ctx, path, req)
		if err == nil || ctx.Err() != nil || !(absent(err) || lostReply(err)) {
			return ans, err
		}
		wait := min(askRetryWait, time.Until(last))
		if wait <= 0 {
			return answer{}, err
		}
		select {
		case <-ctx.Done():
			return answer{}, err
		case <-time.After(wait):
		}
	}
}

// lostReply is a transport failure that may follow an app which recorded the call and then went away unanswered: the
// connection closed before a line, or reset.
func lostReply(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF)
}

// absent is a request that certainly did not land: the dial was refused or the socket missing (no serve, or one that
// has not loaded its project yet: it binds after its replay, T-659).
func absent(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT)
}

// askOnce is one request on its own connection; see ask.
func askOnce(ctx context.Context, path string, req request) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, verdictDeadline) // a caller with no deadline still has one
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return answer{}, fmt.Errorf("obor serve is not running (%w)", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	b, _ := json.Marshal(req) // strings only: cannot fail
	if _, err := c.Write(append(b, '\n')); err != nil {
		return answer{}, err
	}
	b, err = bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return answer{}, err
	}
	var ans answer
	if err := json.Unmarshal(b, &ans); err != nil {
		return answer{}, err
	}
	if ans.Error != "" {
		return answer{}, errors.New(ans.Error)
	}
	return ans, nil
}
