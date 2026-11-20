package app

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/store"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// MergePreview is lane's scripted preview (SetPreview), else clean onto the target's head (branch main).
func (s *FakeWorkflow) MergePreview(ctx context.Context, lane string) (port.Preview, error) {
	if err := s.failing(ctx, "MergePreview"); err != nil {
		return port.Preview{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.previews[lane]; ok {
		return p, nil
	}
	return port.Preview{Target: "main", TargetSHA: s.head}, nil
}

// SetPreview scripts lane's merge preview.
func (s *FakeWorkflow) SetPreview(lane string, p port.Preview) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.previews == nil {
		s.previews = map[string]port.Preview{}
	}
	s.previews[lane] = p
}

// FakeWorkflow is the package's one port.Workflow for tests (as FakeScratch): the loop rig's (app_test)
// and the concurrency rig's, and the in-memory row of the workflow contract table (testkit.WorkflowContract). It runs
// no git and no command: shas are made up, the target is a head the test moves (Move, Land), pins and lane commits are
// maps. It keeps port.Workflow's contract (port/workflow.go): an outcome is final for its step, an
// error is "not decided". Outcomes are scripted per call and task (Script); unscripted, a gate is green, a lane test
// green and a publish merges while the target is still the candidate's. The git behaviour behind each outcome (a real
// conflict, a moved merge, a held fast-forward) is internal/checks' to test, over real git.
//
// It knows only what it is told: a lane it was given (AddLane, Prepare) has a committed snapshot of its dir, so Submit
// commits what changed there and Test refuses it dirty; an unknown lane, sha or resolution is taken as it comes (the
// concurrency rig's records name lanes and shas the fake never saw). Its review git has the one target (Head of any
// branch is its head) and records the role worktrees it detached and the closes it committed. A test's own fake of one part is combined with it by WorkflowOf.
type FakeWorkflow struct {
	Dir    string // serve's worktree, the one a conflict names (nothing is written there)
	Checks string // a green lane test's checks summary ("": "rc=0", merge.check ran)

	Submits, Gates, Tests, Publishes, AfterMerges atomic.Int32 // calls, a failed one too

	mu         sync.Mutex
	head       string                // the target's head
	history    map[string]bool       // every sha the target held (a published candidate's is "already")
	commits    map[string]fakeCommit // what the fake made: lane commits, merges, resolutions
	lanes      map[string]*fakeLane
	pins       map[string]string // "submitted/<task>/<id>", "candidate/<task>", "resolved/<task>" → sha
	landed     map[string]bool   // the target holds merge evidence of the task
	merges     []string          // the tasks merged onto the target, in order (a publish, or Land)
	published  map[string]port.Candidate
	script     map[string]string // "<call> <task>" → its outcome (Script)
	fail       map[string]func(ctx context.Context) error
	onTest     func(ctx context.Context, dir string)
	inputs     []port.GateInput
	subjects   []string // each Submit's lane commit subject ("<task>: <summary>"), a pinned one too
	afterRuns  map[string]int
	detached   map[string]string       // a role worktree's dir → the sha Detach put it on
	closes     []port.ReviewCommit     // the review closes committed, in order
	open       *fakeCommit             // the conflicting merge left open in Dir (nil: none)
	resolution string                  // the merge the integrator committed in Dir ("": none)
	previews   map[string]port.Preview // a lane's scripted merge preview (SetPreview)
	n          int

	// The concurrency rig's: a gate of hold, a publish of holdPub, a resolved check of holdResolved block until
	// release is closed (or their ctx ends), holdTest's lane test until testRelease is (release when nil); entered gets
	// each Gate's task as it begins ("publish <task>", "resolved <task>", "test <task>" when held; nil: not sent).
	// Every scratch call (Gate, Publish, Resolved) counts in inflight, max the most at once. unbased: a candidate has no
	// Base (green at once, no lane test).
	hold, holdPub, holdResolved, holdTest string
	unbased                               bool
	release, testRelease                  chan struct{}
	entered                               chan string
	gates, tests, pubs                    map[string]int
	inflight, max                         int
}

// fakeCommit is a commit the fake made: a lane's (parents: the lane's previous head; own: the task's files against its
// lane's base) or a merge (parents: the target, then the submitted sha).
type fakeCommit struct {
	task    string
	parents []string
	base    string            // a lane commit's fork point
	own     map[string]string // a lane commit's files changed since its base, by path ("": deleted)
}

// fakeLane is a lane the fake was given: its dir, the sha its branch forked from and its head, the files its head
// holds, and the sha a lane test checked out (at; "": on its branch).
type fakeLane struct {
	dir, branch string
	base, head  string
	files       map[string]string
	at          string
}

// NewFakeWorkflow is a scratch whose target is at head ("": a made-up sha).
func NewFakeWorkflow(head string) *FakeWorkflow {
	s := &FakeWorkflow{history: map[string]bool{}, commits: map[string]fakeCommit{}, lanes: map[string]*fakeLane{},
		pins: map[string]string{}, landed: map[string]bool{}, published: map[string]port.Candidate{},
		script: map[string]string{}, fail: map[string]func(context.Context) error{}, afterRuns: map[string]int{},
		detached: map[string]string{},
		gates:    map[string]int{}, tests: map[string]int{}, pubs: map[string]int{}}
	if head == "" {
		head = s.sha("target")
	}
	s.head, s.history[head] = head, true
	return s
}

// sha is a new made-up sha (under mu, or before the fake is shared).
func (s *FakeWorkflow) sha(what string) string {
	s.n++
	sum := sha1.Sum([]byte(fmt.Sprint(what, " ", s.n)))
	return hex.EncodeToString(sum[:])
}

// laneFiles is dir's files by path, as git would track them: .claude/ (obor init's settings, excluded) and a leftover
// done file are not.
func laneFiles(dir string) map[string]string {
	files := map[string]string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		switch {
		case d.IsDir() && (rel == ".claude" || rel == ".git"):
			return filepath.SkipDir
		case d.IsDir(), rel == ".task-done.md":
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil {
			files[rel] = string(b)
		}
		return nil
	})
	return files
}

// AddLane gives the fake lane name at dir on branch, forked from the target's head: what dir holds now is committed.
func (s *FakeWorkflow) AddLane(name, dir, branch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lanes[name] = &fakeLane{dir: dir, branch: branch, base: s.head, head: s.head, files: laneFiles(dir)}
}

// commit commits what changed in lane l since its head (under mu): the new head, or the old one when nothing did.
func (s *FakeWorkflow) commit(l *fakeLane, task string) string {
	now := laneFiles(l.dir)
	if maps.Equal(now, l.files) {
		return l.head
	}
	own := map[string]string{}
	if prev, ok := s.commits[l.head]; ok {
		maps.Copy(own, prev.own)
	}
	for path, text := range now {
		if l.files[path] != text {
			own[path] = text
		}
	}
	for path := range l.files {
		if _, ok := now[path]; !ok {
			own[path] = ""
		}
	}
	sha := s.sha("commit")
	s.commits[sha] = fakeCommit{task: task, parents: []string{l.head}, base: l.base, own: own}
	l.head, l.files = sha, now
	return sha
}

// Commit is the worker's own commit in lane (git add -A; git commit): its head after it.
func (s *FakeWorkflow) Commit(lane string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commit(s.lanes[lane], "")
}

// LaneHead is lane's head sha; LaneBase the sha its branch forked from; LaneAt the sha a lane test has checked out
// there ("": the lane is on its branch).
func (s *FakeWorkflow) LaneHead(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.head })
}

func (s *FakeWorkflow) LaneBase(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.base })
}

func (s *FakeWorkflow) LaneAt(lane string) string {
	return s.laneField(lane, func(l *fakeLane) string { return l.at })
}

func (s *FakeWorkflow) laneField(lane string, f func(*fakeLane) string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(s.lanes[lane])
}

// Commits is how many lane commits Submit made for task.
func (s *FakeWorkflow) Commits(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.commits {
		if c.task == task && c.base != "" {
			n++
		}
	}
	return n
}

// Script makes call's outcome for task outcome, for every call until scripted again ("": unscripted). call is Gate
// (any input), "Gate lane", "Gate moved" or "Gate resolved" (that input kind's: a conflict is the merge's, never a
// committed resolution's), Test, Publish (hold, pending) or AfterMerge. outcome is a kind (port.Outcome*, port.Publish*), then its
// line; a conflict's and a hold's rest is its files (default shared.txt). AfterMerge's outcome is its line.
func (s *FakeWorkflow) Script(call, task, outcome string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if outcome == "" {
		delete(s.script, call+" "+task)
		return
	}
	s.script[call+" "+task] = outcome
}

// scripted is the outcome of call (the input kind's first, for a gate): its kind and the rest.
func (s *FakeWorkflow) scripted(task string, calls ...string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, call := range calls {
		if out, ok := s.script[call+" "+task]; ok {
			kind, rest, _ := strings.Cut(out, " ")
			return kind, rest
		}
	}
	return "", ""
}

// Arm makes method (Submit, Gate, Test, Publish, AfterMerge, Landed, Unpin, Prepare, Free, Target, Head, Detach,
// Close) give fn's error instead of acting (nil:
// heals it); fn returning nil lets the method act.
func (s *FakeWorkflow) Arm(method string, fn func(ctx context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = fn
}

func (s *FakeWorkflow) failing(ctx context.Context, method string) error {
	s.mu.Lock()
	f := s.fail[method]
	s.mu.Unlock()
	if f == nil {
		return nil
	}
	return f(ctx)
}

// OnTest runs fn in each lane test, with the candidate checked out in the lane's dir.
func (s *FakeWorkflow) OnTest(fn func(ctx context.Context, dir string)) {
	s.mu.Lock()
	s.onTest = fn
	s.mu.Unlock()
}

// Move is a commit on the target outside the app (an unrelated one): its new head.
func (s *FakeWorkflow) Move() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head = s.sha("move")
	s.history[s.head] = true
	return s.head
}

// Land is task merged onto the target outside the app (obor merge by hand, "Merge <task>: by hand"): its new head.
func (s *FakeWorkflow) Land(task string) string {
	head := s.Move()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.landed[task] = true
	s.merges = append(s.merges, task)
	return head
}

// TargetHead is the target's head.
func (s *FakeWorkflow) TargetHead() string { s.mu.Lock(); defer s.mu.Unlock(); return s.head }

// Parents is sha's parents (a merge: the target, then the submitted sha; nil: a sha the fake did not make).
func (s *FakeWorkflow) Parents(sha string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commits[sha].parents)
}

// Merges is how many merges of task the target holds (all tasks': "").
func (s *FakeWorkflow) Merges(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.merges {
		if task == "" || m == task {
			n++
		}
	}
	return n
}

// Published is task's candidate the last publish merged (false: none).
func (s *FakeWorkflow) Published(task string) (port.Candidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.published[task]
	return c, ok
}

// Open: a conflict is open in Dir, not committed yet.
func (s *FakeWorkflow) Open() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.open != nil }

// Resolve is the integrator's resolution of the open conflict committed in Dir (git add; git commit): the merge of the
// conflicting gate's submitted sha onto its target. Its sha ("": no conflict was open).
func (s *FakeWorkflow) Resolve() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		return ""
	}
	sha := s.sha("resolution")
	s.commits[sha] = *s.open
	s.open, s.resolution = nil, sha
	return sha
}

// Pins is every pin (sorted); Pin one ("candidate/T-1": "" none).
func (s *FakeWorkflow) Pins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Keys(s.pins))
	sort.Strings(out)
	return out
}

func (s *FakeWorkflow) Pin(ref string) string { s.mu.Lock(); defer s.mu.Unlock(); return s.pins[ref] }

// Inputs is every gate's input, in order.
func (s *FakeWorkflow) Inputs() []port.GateInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.inputs)
}

// Subjects is each Submit's commit subject ("<task>: <summary>"), in order.
func (s *FakeWorkflow) Subjects() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.subjects)
}

// AfterRuns is how many after-merges of task ran (an armed failure is no run).
func (s *FakeWorkflow) AfterRuns(task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterRuns[task]
}

func (s *FakeWorkflow) enter() {
	s.mu.Lock()
	s.inflight++
	s.max = max(s.max, s.inflight)
	s.mu.Unlock()
}

func (s *FakeWorkflow) leave() {
	s.mu.Lock()
	s.inflight--
	s.mu.Unlock()
}

// count is m[task] under mu (m: gates, tests or pubs).
func (s *FakeWorkflow) count(m map[string]int, task string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return m[task]
}

// note sends what to entered (when set).
func (s *FakeWorkflow) note(what string) {
	if s.entered != nil {
		s.entered <- what
	}
}

// waitOn blocks until ch is closed (release when nil) or ctx ends.
func (s *FakeWorkflow) waitOn(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		ch = s.release
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Submit commits the lane's changes ("<task>: <summary>") and pins its head under id; a pin id has is returned as it
// is. A lane with nothing beyond its fork point is refused, as checks.Scratch refuses a branch already in the target.
// An unknown lane's sha is "submitted".
func (s *FakeWorkflow) Submit(ctx context.Context, task, id, lane, summary string) (string, error) {
	s.Submits.Add(1)
	if err := s.failing(ctx, "Submit"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subjects = append(s.subjects, task+": "+summary)
	pin := "submitted/" + task + "/" + id
	if sha, ok := s.pins[pin]; ok {
		return sha, nil
	}
	sha := "submitted"
	if l := s.lanes[lane]; l != nil {
		if sha = s.commit(l, task); sha == l.base {
			return "", port.Refused{Line: "FAIL " + l.branch + " is already in the target"}
		}
	}
	s.pins[pin] = sha
	return sha, nil
}

// Gate merges in.Input onto the target (a resolved input is taken as committed) and pins the candidate, unless
// scripted otherwise.
func (s *FakeWorkflow) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	s.enter()
	defer s.leave()
	s.Gates.Add(1)
	s.mu.Lock()
	s.gates[in.Task]++
	s.mu.Unlock()
	if err := s.failing(ctx, "Gate"); err != nil {
		return port.GateOutcome{}, err
	}
	s.mu.Lock()
	s.inputs = append(s.inputs, in)
	s.mu.Unlock()
	s.note(in.Task)
	if in.Task == s.hold {
		if err := s.waitOn(ctx, nil); err != nil {
			return port.GateOutcome{}, err
		}
	}
	kind, rest := s.scripted(in.Task, "Gate "+in.Kind, "Gate")
	s.mu.Lock()
	defer s.mu.Unlock()
	target, submitted := s.head, in.Input
	if c, ok := s.commits[in.Input]; ok && in.Kind == port.InputResolved && len(c.parents) == 2 {
		target, submitted = c.parents[0], c.parents[1]
	}
	switch kind {
	case port.OutcomeConflict:
		if in.Kind != port.InputResolved {
			s.open, s.resolution = &fakeCommit{task: in.Task, parents: []string{target, submitted}}, ""
			return port.GateOutcome{Kind: port.OutcomeConflict, Files: strings.Fields(cmp.Or(rest, "shared.txt")), Dir: s.Dir}, nil
		}
	case port.OutcomeRed:
		return port.GateOutcome{Kind: port.OutcomeRed, Line: cmp.Or(rest, "RED compile (rc 1)")}, nil
	case port.OutcomeFail:
		return port.GateOutcome{Kind: port.OutcomeFail, Line: cmp.Or(rest, "FAIL "+in.Task+": scripted")}, nil
	}
	sub := s.commits[submitted]
	sha := in.Input
	if in.Kind != port.InputResolved || len(s.commits[sha].parents) != 2 {
		sha = s.pins["candidate/"+in.Task] // a repeat makes the same commit, as git's gate does
		if !slices.Equal(s.commits[sha].parents, []string{target, submitted}) {
			sha = s.sha("merge")
			s.commits[sha] = fakeCommit{task: in.Task, parents: []string{target, submitted}}
		}
	}
	s.pins["candidate/"+in.Task] = sha
	c := port.Candidate{Task: in.Task, Lane: in.Lane, SHA: sha, Target: target}
	var stat, diff []string
	for _, path := range slices.Sorted(maps.Keys(sub.own)) {
		stat = append(stat, " "+path+" | 1 +")
		diff = append(diff, "diff --git a/"+path+" b/"+path+"\n+++ b/"+path+"\n+"+strings.TrimRight(sub.own[path], "\n"))
	}
	c.Stat, c.Diff = strings.Join(stat, "\n"), strings.Join(diff, "\n")
	sum := sha256.Sum256([]byte(cmp.Or(c.Diff, submitted)))
	c.Patch = hex.EncodeToString(sum[:])
	if !s.unbased {
		c.Base = cmp.Or(sub.base, "b0")
	}
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: c}, nil
}

// Test is the lane test: a dirty lane is an error (never checked out); the candidate checked out in the lane while
// OnTest's hook runs, then the lane back on its branch; a cut test (its ctx ended) is an error.
func (s *FakeWorkflow) Test(ctx context.Context, in port.TestInput) (port.TestOutcome, error) {
	s.Tests.Add(1)
	s.mu.Lock()
	s.tests[in.Task]++
	s.mu.Unlock()
	if err := s.failing(ctx, "Test"); err != nil {
		return port.TestOutcome{}, err
	}
	if in.Task == s.holdTest {
		s.note("test " + in.Task)
		if err := s.waitOn(ctx, s.testRelease); err != nil {
			return port.TestOutcome{}, err
		}
	}
	s.mu.Lock()
	l, during := s.lanes[in.Lane], s.onTest
	dir := ""
	if l != nil {
		if !maps.Equal(laneFiles(l.dir), l.files) {
			s.mu.Unlock()
			return port.TestOutcome{}, fmt.Errorf("lane %s dirty: its test runs once it is clean", in.Lane)
		}
		l.at, dir = in.SHA, l.dir
	}
	s.mu.Unlock()
	if during != nil {
		during(ctx, dir)
	}
	if l != nil {
		s.mu.Lock()
		l.at = ""
		s.mu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return port.TestOutcome{}, err
	}
	switch kind, rest := s.scripted(in.Task, "Test"); kind {
	case port.OutcomeRed:
		return port.TestOutcome{Kind: port.OutcomeRed, Line: cmp.Or(rest, "RED check (rc 1): 0 pass, 1 fail")}, nil
	case port.OutcomeFail:
		return port.TestOutcome{Kind: port.OutcomeFail, Line: cmp.Or(rest, "FAIL "+in.Task+": scripted")}, nil
	}
	checks := cmp.Or(s.Checks, "rc=0")
	return port.TestOutcome{Kind: port.OutcomeGreen, Line: "GREEN " + in.Task + " (" + checks + ")", Checks: checks}, nil
}

// Resolved pins the resolution committed in Dir: refused while the conflict is still open, or when it is no merge of
// input. With no conflict seen at all (the concurrency rig) it is "resolution".
func (s *FakeWorkflow) Resolved(ctx context.Context, task, input string, _ *port.Mismatch) (string, error) {
	s.enter()
	defer s.leave()
	if task == s.holdResolved {
		s.note("resolved " + task)
		if err := s.waitOn(ctx, nil); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch c := s.commits[s.resolution]; {
	case s.open != nil:
		return "", port.Refused{Line: "the merge in " + s.Dir + " is not committed"}
	case s.resolution == "":
		return "resolution", nil
	case c.parents[1] != input:
		return "", port.Refused{Line: s.Dir + " holds no merge of " + input}
	}
	s.pins["resolved/"+task] = s.resolution
	return s.resolution, nil
}

// Publish fast-forwards the target to c.SHA while it is at c.Target (unless scripted: a hold), then unpins the task.
func (s *FakeWorkflow) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.enter()
	defer s.leave()
	s.Publishes.Add(1)
	s.mu.Lock()
	s.pubs[c.Task]++
	s.mu.Unlock()
	if c.Task == s.holdPub {
		s.note("publish " + c.Task)
		if err := s.waitOn(ctx, nil); err != nil {
			return port.PublishOutcome{}, err
		}
	}
	if err := s.failing(ctx, "Publish"); err != nil {
		return port.PublishOutcome{}, err
	}
	kind, rest := s.scripted(c.Task, "Publish")
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.history[c.SHA]:
		s.unpin(c.Task)
		return port.PublishOutcome{Kind: port.PublishAlready}, nil
	case s.head != c.Target:
		return port.PublishOutcome{Kind: port.PublishMoved, Head: s.head}, nil
	case kind == port.PublishHold:
		return port.PublishOutcome{Kind: port.PublishHold, Files: strings.Fields(cmp.Or(rest, "shared.txt"))}, nil
	case kind == port.PublishPending: // the merge asked for, not done: Land is the forge's merge
		return port.PublishOutcome{Kind: port.PublishPending}, nil
	}
	s.head, s.history[c.SHA], s.landed[c.Task] = c.SHA, true, true
	s.merges, s.published[c.Task] = append(s.merges, c.Task), c
	s.unpin(c.Task)
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

// AfterMerge is "ran" (scripted: its line).
func (s *FakeWorkflow) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	s.AfterMerges.Add(1)
	if err := s.failing(ctx, "AfterMerge"); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.afterRuns[c.Task]++
	line := s.script["AfterMerge "+c.Task]
	s.mu.Unlock()
	return cmp.Or(line, "ran"), nil
}

// Landed: the target holds a merge of task.
func (s *FakeWorkflow) Landed(ctx context.Context, task string) (bool, error) {
	if err := s.failing(ctx, "Landed"); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.landed[task], nil
}

// Unpin deletes task's pins.
func (s *FakeWorkflow) Unpin(ctx context.Context, task string) error {
	if err := s.failing(ctx, "Unpin"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unpin(task)
	return nil
}

func (s *FakeWorkflow) unpin(task string) {
	for ref := range s.pins {
		if strings.HasPrefix(ref, "submitted/"+task+"/") || ref == "candidate/"+task || ref == "resolved/"+task {
			delete(s.pins, ref)
		}
	}
}

// Prepare puts lane in.Lane at in.Dir on in.Branch at in.Target (in.Redo, a lane it knows: kept as it is), committed
// as its dir is now; no lane dir is a final refusal, as checks.Lane's. The base is in.Target.
func (s *FakeWorkflow) Prepare(ctx context.Context, in port.PrepareInput) (port.Prepared, error) {
	if err := s.failing(ctx, "Prepare"); err != nil {
		return port.Prepared{}, err
	}
	if st, err := os.Stat(in.Dir); err != nil || !st.IsDir() {
		return port.Prepared{}, port.Refused{Line: "no lane dir " + in.Dir}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.lanes[in.Lane]; !in.Redo || l == nil {
		s.lanes[in.Lane] = &fakeLane{dir: in.Dir, branch: in.Branch, base: in.Target, head: in.Target, files: laneFiles(in.Dir)}
	}
	return port.Prepared{Base: in.Target}, nil
}

// Free gives lane in.Lane back: refused while it has uncommitted changes or commits the target lacks, unless
// in.Discard; then the fake forgets it (an unknown lane is gone already). Its line names what it discarded.
func (s *FakeWorkflow) Free(ctx context.Context, in port.FreeInput) (string, error) {
	if err := s.failing(ctx, "Free"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.lanes[in.Lane]
	if l == nil {
		return "", nil
	}
	var lost []string
	if !maps.Equal(laneFiles(l.dir), l.files) {
		lost = append(lost, "uncommitted changes")
	}
	if s.ahead(l) {
		lost = append(lost, "commits not in the target")
	}
	if len(lost) > 0 && !in.Discard {
		return "", port.Refused{Line: "lane " + in.Lane + " has " + strings.Join(lost, " and ")}
	}
	for path := range laneFiles(l.dir) { // the uncommitted changes discarded (git reset --hard, git clean -fd)
		if _, ok := l.files[path]; !ok {
			_ = os.Remove(filepath.Join(l.dir, path))
		}
	}
	for path, text := range l.files {
		_ = os.WriteFile(filepath.Join(l.dir, path), []byte(text), 0o644)
	}
	delete(s.lanes, in.Lane)
	if len(lost) == 0 {
		return "", nil
	}
	return "discarded " + strings.Join(lost, " and "), nil
}

// ahead: lane l's head has commits the target lacks (under mu): a commit past its base no merge on the target took,
// and its task not landed by hand.
func (s *FakeWorkflow) ahead(l *fakeLane) bool {
	if l.head == l.base || s.landed[s.commits[l.head].task] {
		return false
	}
	for h := range s.history {
		if c := s.commits[h]; len(c.parents) == 2 && c.parents[1] == l.head {
			return false
		}
	}
	return true
}

// Target is the target's head; ahead: a lane on task's branch (task/<task>) has commits the target lacks.
func (s *FakeWorkflow) Target(ctx context.Context, task string) (string, bool, error) {
	if err := s.failing(ctx, "Target"); err != nil {
		return "", false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range s.lanes {
		if l.branch == "task/"+task && s.ahead(l) {
			return s.head, true, nil
		}
	}
	return s.head, false, nil
}

// Head is the target's head, whatever branch is asked for (the fake has one target).
func (s *FakeWorkflow) Head(ctx context.Context, _ string) (string, error) {
	if err := s.failing(ctx, "Head"); err != nil {
		return "", err
	}
	return s.TargetHead(), nil
}

// Files is the files the task diffs merged from from to to changed: to's first parents back to from (from "": as far
// as the fake made them).
func (s *FakeWorkflow) Files(_ context.Context, from, to string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := map[string]bool{}
	for sha := to; sha != "" && sha != from; {
		c, ok := s.commits[sha]
		if !ok || len(c.parents) != 2 {
			break
		}
		for path := range s.commits[c.parents[1]].own {
			files[path] = true
		}
		sha = c.parents[0]
	}
	return slices.Sorted(maps.Keys(files)), nil
}

// Detach records role worktree dir at sha.
func (s *FakeWorkflow) Detach(ctx context.Context, dir, sha string) error {
	if err := s.failing(ctx, "Detach"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detached[dir] = sha
	return nil
}

// Detached is the sha Detach put role worktree dir on ("": never).
func (s *FakeWorkflow) Detached(dir string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached[dir]
}

// Close commits review close c: recorded, its subject Closed's answer for its id from then on.
func (s *FakeWorkflow) Close(ctx context.Context, c port.ReviewCommit) error {
	if err := s.failing(ctx, "Close"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes = append(s.closes, c)
	return nil
}

// Closed is the subject of review id's newest close ("": none).
func (s *FakeWorkflow) Closed(_ context.Context, _, _, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.closes) - 1; i >= 0; i-- {
		if s.closes[i].ID == id {
			return s.closes[i].Subject, nil
		}
	}
	return "", nil
}

var _ port.Workflow = (*FakeWorkflow)(nil)

// WorkflowOf is a port.Workflow of a test's own fakes: each part takes over every piece of port.Workflow it
// implements (port.Scratch, port.LaneSetup, port.Review, Target, MergePreview), a later
// part over an earlier one; the pieces no part implements are a NewFakeWorkflow's. A nil part is skipped.
func WorkflowOf(parts ...any) port.Workflow {
	f := NewFakeWorkflow("")
	w := &workflowOf{Scratch: f, LaneSetup: f, Review: f, targeter: f, previewer: f}
	for _, p := range parts {
		if x, ok := p.(port.Scratch); ok {
			w.Scratch = x
		}
		if x, ok := p.(port.LaneSetup); ok {
			w.LaneSetup = x
		}
		if x, ok := p.(port.Review); ok {
			w.Review = x
		}
		if x, ok := p.(targeter); ok {
			w.targeter = x
		}
		if x, ok := p.(previewer); ok {
			w.previewer = x
		}
	}
	return w
}

type targeter interface {
	Target(ctx context.Context, task string) (string, bool, error)
}

type previewer interface {
	MergePreview(ctx context.Context, lane string) (port.Preview, error)
}

type workflowOf struct {
	port.Scratch
	port.LaneSetup
	port.Review
	targeter
	previewer
}

// Integrator jobs start a fresh session each (Launch with Fresh, the job's wake its first prompt), not a wake
// typed into the session that runs.

// greenTask gates task green in r's project pj with candidate c.
func greenTask(r *concRig, task string, c port.Candidate) {
	r.add("pj", task, event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	r.add("pj", task, event.StepGate, event.KindResult, gateOut{Outcome: outGreen, Candidate: c})
}

// The identity table (registry.go), its case table, on the watchdogs'
// harness: lane L-01's worker pj-l-01 in w1:p1 (terminal t1, session s1, labelled by its launch), a shell w1:p2 beside
// it. Each test asserts whereIs and the consumer's observable call (the fake Agents' Stop pane, the Prompter/Poster/
// Liner target, the queued line, the fact written). Rows 12–20 are the transitions the model asks for.

// newIdentHarness is the harness with w1:p1 labelled pj-l-01 on terminal t1 (session s1), the lane row and the config
// naming pj-l-01 (lane L-01's worker), and a shell w1:p2 (terminal t2) in the project's workspace; its log lines in
// out.
func newIdentHarness(t *testing.T) (*harness, *bytes.Buffer) {
	h := newHarness(t)
	h.write(filepath.Join(h.root, ".obor", "config.json"), `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"lanes":{"count":1}}`)
	h.lanes.lanes[0].Agent = "pj-l-01"
	p := h.pane("w1:p1")
	p.Label, p.Terminal, p.Session = "pj-l-01", "t1", "s1"
	h.snap.Panes = append(h.snap.Panes, port.Pane{ID: "w1:p2", Group: "pj", Cwd: h.root, Terminal: "t2"})
	out := &bytes.Buffer{}
	h.a.opt.Out = out
	return h, out
}

// landedScratch: the target holds merge evidence of every task (a merge outside the app).
type landedScratch struct{ port.Scratch }

func (landedScratch) Landed(context.Context, string) (bool, error) { return true, nil }

// needsMessages is the message facts of p's log that announce a needs-you.
func needsMessages(t *testing.T, p *proj) []event.Message {
	t.Helper()
	recs, err := event.ReadLog(p.root)
	if err != nil {
		t.Fatal(err)
	}
	var out []event.Message
	for _, r := range recs {
		if r.Task != event.MsgTask || r.Step != event.StepMessage || r.Kind != event.KindFact {
			continue
		}
		var m event.Message
		if err := json.Unmarshal(r.Data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Of.Kind == ruleNeeds {
			out = append(out, m)
		}
	}
	return out
}

// schedScratch is a fake Scratch and Agents for the scheduling tests: gates are green and logged by task,
// Resolved logs the input it was given.
type schedScratch struct {
	h        *harness
	gated    []string // the task of each Gate call
	resolved []string // the input of each Resolved call
}

func (f *schedScratch) Submit(context.Context, string, string, string, string) (string, error) {
	return "submitted", nil
}

func (f *schedScratch) Gate(_ context.Context, in port.GateInput) (port.GateOutcome, error) {
	f.gated = append(f.gated, in.Task)
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: port.Candidate{Task: in.Task, Lane: in.Lane, SHA: "c-" + in.Task,
		Target: "b", Patch: "p"}}, nil
}

func (f *schedScratch) Test(_ context.Context, in port.TestInput) (port.TestOutcome, error) {
	return port.TestOutcome{Kind: port.OutcomeGreen, Line: "GREEN " + in.Task, Checks: "check 1 pass"}, nil
}

func (f *schedScratch) Resolved(_ context.Context, _, input string, _ *port.Mismatch) (string, error) {
	f.resolved = append(f.resolved, input)
	return "resolution", nil
}

func (f *schedScratch) Publish(context.Context, port.Candidate) (port.PublishOutcome, error) {
	return port.PublishOutcome{Kind: port.PublishMerged}, nil
}

func (f *schedScratch) Landed(context.Context, string) (bool, error) { return false, nil }

func (f *schedScratch) Unpin(context.Context, string) error { return nil }

func (f *schedScratch) AfterMerge(context.Context, port.Candidate) (string, error) { return "ran", nil }

func (f *schedScratch) Launch(context.Context, port.Launch) (port.Started, error) {
	f.h.a.obs["w1:p9"] = &seen{pane: port.Pane{ID: "w1:p9", Group: "pj", Cwd: f.h.root, Label: "pj-int",
		Agent: "claude", Status: "idle"}, since: f.h.now}
	return port.Started{Line: "started", Pane: "w1:p9", Kind: "claude"}, nil
}

func (f *schedScratch) Stop(context.Context, string, string, bool) (string, error) {
	return "stopped", nil
}

func (f *schedScratch) Prompt(context.Context, string, string, bool) (bool, error) {
	return true, nil
}

// seedSched is project pj with an integrator (serve false: without, so no merge loop), lane L-01's T-1 submitted at
// sha s1 and gated into a conflict. before, when given, acts on the harness's snapshot before the connect takes it.
func seedSched(t *testing.T, serve bool, before ...func(h *harness)) (*harness, *schedScratch) {
	h := newHarness(t)
	cfg := `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""}}}`
	if !serve {
		cfg = `{"version":2,"project":{"name":"pj","agent_prefix":"pj"}}`
	}
	h.write(filepath.Join(h.root, ".obor", "config.json"), cfg)
	f := &schedScratch{h: h}
	h.a.opt.Loop = func(root string) (*Loop, error) {
		return withCfg(root, &Loop{Workflow: WorkflowOf(f), Agents: f, Prompter: f}), nil
	}
	if err := os.MkdirAll(h.lanes.lanes[0].Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, fn := range before {
		fn(h)
	}
	h.connect()
	h.a.mu.Lock()
	h.a.p.folded.lr.rows["L-01"] = store.LaneRow{Lane: "L-01", Task: "T-1"} // the lane holds T-1
	h.a.mu.Unlock()
	if !serve {
		return h, f
	}
	addRecord(t, h, "T-1", event.StepSubmitted, event.KindFact, submitData{Lane: "L-01", Agent: "pj-l-01", Pane: "w1:p1"})
	addRecord(t, h, "T-1", event.StepSubmit, event.KindResult, submitOut{SHA: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindIntent, gateIn{Kind: port.InputLane, Lane: "L-01", Input: "s1"})
	addRecord(t, h, "T-1", event.StepGate, event.KindResult, gateOut{Outcome: outConflict})
	return h, f
}

func addRecord(t *testing.T, h *harness, task, step, kind string, d any) {
	t.Helper()
	p := h.a.p
	raw, _ := json.Marshal(d)
	r, err := p.log.Append(event.Record{Task: task, Attempt: 1, Step: step, Kind: kind, Data: raw}, h.now)
	if err != nil {
		t.Fatal(err)
	}
	p.foldRecord(r)
}
