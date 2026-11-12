package checks

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// fakeGit is gitIO over a small model of the repository: its refs, each worktree's HEAD, branch, status and
// open merge, and the commits Scratch made (their parents, messages and dates). Writes change the model as git does
// for the calls Scratch makes (a commit moves HEAD and closes a merge, a reset clears the worktree, a fast-forward
// moves the branch); the reads it does not model (diffs, the log, a blob's sha) answer from answers, keyed by the
// call ("Diff <from> <to>"), "" when unscripted. A merge of a sha in conflicts stops: its paths stay unmerged (none:
// a replayed resolution). cut fails the cut-th call (as a git shim over real git would); every call is
// logged in calls, "<Method> <args…>".
type fakeGit struct {
	root      string
	refs      map[string]string   // ref → sha
	head      map[string]string   // a detached worktree's HEAD
	branch    map[string]string   // the worktree's branch: its HEAD is refs/heads/<branch>
	status    map[string][]string // status --porcelain lines
	merging   map[string]string   // MERGE_HEAD
	unmerged  map[string][]string
	parents   map[string][]string // a commit's parents (a sha absent here is a root commit)
	msgs      map[string]string
	dates     map[string]int64
	answers   map[string]string
	conflicts map[string][]string // a merge of this sha stops with these paths unmerged
	failing   map[string]error    // a method that fails (before any effect)
	before    map[string]func()   // run as a method is called (another process moving main meanwhile, say)
	calls     []string
	cut       int
}

var _ gitIO = (*fakeGit)(nil)

// errCut is the error of the cut call.
var errCut = errors.New("cut: git call failed")

// writes are fakeGit's writing methods: the cut table cuts each.
var writes = []string{"AddAll", "Commit", "Amend", "Merge", "WriteTree", "UpdateRef", "DeleteRef", "FF", "Reset", "Clean",
	"Detach", "Switch", "AddWorktree"}

// newFakeGit is the model of the fixture repo (fixture_test.go's template): main at main0 checked out in root, lane
// dir on task/T-05 forked there with its uncommitted feature.txt.
func newFakeGit(root, lane string) *fakeGit {
	f := &fakeGit{root: root, refs: map[string]string{}, head: map[string]string{}, branch: map[string]string{},
		status: map[string][]string{}, merging: map[string]string{}, unmerged: map[string][]string{},
		parents: map[string][]string{}, msgs: map[string]string{}, dates: map[string]int64{}, answers: map[string]string{},
		conflicts: map[string][]string{}, failing: map[string]error{}, before: map[string]func(){}}
	f.refs["refs/heads/main"], f.branch[root], f.dates["main0"] = "main0", "main", 100
	f.refs["refs/heads/task/T-05"], f.branch[lane] = "main0", "task/T-05"
	f.status[lane] = []string{"?? feature.txt"}
	return f
}

// clone is a deep copy of f, its calls and cut reset.
func (f *fakeGit) clone() *fakeGit {
	g := *f
	g.refs, g.head, g.branch, g.merging = maps.Clone(f.refs), maps.Clone(f.head), maps.Clone(f.branch), maps.Clone(f.merging)
	g.status, g.unmerged, g.parents = maps.Clone(f.status), maps.Clone(f.unmerged), maps.Clone(f.parents)
	g.msgs, g.dates, g.answers, g.conflicts = maps.Clone(f.msgs), maps.Clone(f.dates), maps.Clone(f.answers), maps.Clone(f.conflicts)
	g.failing, g.before = maps.Clone(f.failing), maps.Clone(f.before)
	g.calls, g.cut = nil, 0
	return &g
}

// call logs a call and fails it when it is the cut one.
func (f *fakeGit) call(method string, args ...string) error {
	f.calls = append(f.calls, strings.TrimSpace(method+" "+strings.Join(args, " ")))
	if fn := f.before[method]; fn != nil {
		fn()
	}
	if len(f.calls) == f.cut {
		return fmt.Errorf("git %s: %w", method, errCut)
	}
	return f.failing[method]
}

// writeCalls is the logged calls that write.
func (f *fakeGit) writeCalls() []string {
	var out []string
	for _, c := range f.calls {
		if m, _, _ := strings.Cut(c, " "); slices.Contains(writes, m) {
			out = append(out, c)
		}
	}
	return out
}

// headOf is dir's HEAD.
func (f *fakeGit) headOf(dir string) string {
	if b := f.branch[dir]; b != "" {
		return f.refs["refs/heads/"+b]
	}
	return f.head[dir]
}

// setHead moves dir's HEAD (its branch when it is on one).
func (f *fakeGit) setHead(dir, sha string) {
	if b := f.branch[dir]; b != "" {
		f.refs["refs/heads/"+b] = sha
		return
	}
	f.head[dir] = sha
}

// known reports whether sha is a commit of the model.
func (f *fakeGit) known(sha string) bool {
	_, p := f.parents[sha]
	_, d := f.dates[sha]
	return p || d || slices.Contains(slices.Collect(maps.Values(f.refs)), sha)
}

// resolve is rev's sha in dir ("" when it does not resolve): HEAD, MERGE_HEAD, <rev>^N, a ref, a known sha.
func (f *fakeGit) resolve(dir, rev string) string {
	if base, n, ok := strings.Cut(rev, "^"); ok {
		i, err := strconv.Atoi(n)
		if p := f.parents[f.resolve(dir, base)]; err == nil && i >= 1 && i <= len(p) {
			return p[i-1]
		}
		return ""
	}
	switch {
	case rev == "HEAD":
		return f.headOf(dir)
	case rev == "MERGE_HEAD":
		return f.merging[dir]
	case f.refs[rev] != "":
		return f.refs[rev]
	case f.known(rev):
		return rev
	}
	return ""
}

// commitSha is a made-up sha for a commit: a function of what makes it (a repeat makes the same one).
func commitSha(what ...string) string {
	sum := sha1.Sum([]byte(strings.Join(what, "\x00")))
	return hex.EncodeToString(sum[:])
}

// commitOn makes a commit on dir's HEAD (a merge when one is open), the worktree then clean.
func (f *fakeGit) commitOn(dir, msg string, date int64) string {
	parents := []string{f.headOf(dir)}
	if m := f.merging[dir]; m != "" {
		parents = append(parents, m)
	}
	sha := commitSha(append(slices.Clone(parents), msg, strconv.FormatInt(date, 10))...)
	f.parents[sha], f.msgs[sha], f.dates[sha] = parents, msg, date
	f.setHead(dir, sha)
	delete(f.status, dir)
	delete(f.merging, dir)
	delete(f.unmerged, dir)
	return sha
}

// isAncestor reports whether a is b or one of its ancestors.
func (f *fakeGit) isAncestor(a, b string) bool {
	seen := map[string]bool{}
	for todo := []string{b}; len(todo) > 0; {
		c := todo[0]
		todo = todo[1:]
		if c == a {
			return true
		}
		if !seen[c] {
			seen[c] = true
			todo = append(todo, f.parents[c]...)
		}
	}
	return false
}

// date is git's "@<s> +0000" as seconds (0: none).
func date(d string) int64 {
	s, _ := strings.CutPrefix(d, "@")
	n, _ := strconv.ParseInt(strings.TrimSuffix(s, " +0000"), 10, 64)
	return n
}

func (f *fakeGit) Verify(dir, ref string) (string, bool, error) {
	if err := f.call("Verify", dir, ref); err != nil {
		return "", false, err
	}
	if strings.Contains(ref, ":") { // <rev>:<path>, a blob
		v := f.answers["Verify "+ref]
		return v, v != "", nil
	}
	sha := f.resolve(dir, ref)
	return sha, sha != "", nil
}

func (f *fakeGit) Rev(dir, rev string) (string, error) {
	if err := f.call("Rev", dir, rev); err != nil {
		return "", err
	}
	if sha := f.resolve(dir, rev); sha != "" {
		return sha, nil
	}
	return "", fmt.Errorf("git rev-parse in %s: unknown revision %s", dir, rev)
}

func (f *fakeGit) Branch(dir string) (string, error) {
	return f.branch[dir], f.call("Branch", dir)
}

func (f *fakeGit) Status(dir string, paths ...string) ([]string, error) {
	if err := f.call("Status", append([]string{dir}, paths...)...); err != nil {
		return nil, err
	}
	var out []string
	for _, l := range f.status[dir] {
		if len(paths) == 0 || slices.Contains(paths, l[min(3, len(l)):]) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeGit) Dirt(dir string) ([]string, error) {
	if err := f.call("Dirt", dir); err != nil {
		return nil, err
	}
	return statusPaths(f.status[dir]), nil
}

func (f *fakeGit) Ancestor(dir, a, b string) (bool, error) {
	if err := f.call("Ancestor", dir, a, b); err != nil {
		return false, err
	}
	return f.isAncestor(f.resolve(dir, a), f.resolve(dir, b)), nil
}

func (f *fakeGit) MergeBase(a, b string) (string, error) {
	if err := f.call("MergeBase", a, b); err != nil {
		return "", err
	}
	for todo := []string{b}; len(todo) > 0; todo = todo[1:] { // the first of b's history in a's
		if f.isAncestor(todo[0], a) {
			return todo[0], nil
		}
		todo = append(todo, f.parents[todo[0]]...)
	}
	return "", fmt.Errorf("git merge-base: no common ancestor of %s and %s", a, b)
}

func (f *fakeGit) Parents(dir, sha string) ([]string, error) {
	if err := f.call("Parents", dir, sha); err != nil {
		return nil, err
	}
	sha = f.resolve(dir, sha)
	return append([]string{sha}, f.parents[sha]...), nil
}

func (f *fakeGit) Dates(dir string, shas ...string) (string, error) {
	if err := f.call("Dates", append([]string{dir}, shas...)...); err != nil {
		return "", err
	}
	var out []string
	for _, s := range shas {
		out = append(out, strconv.FormatInt(f.dates[f.resolve(dir, s)], 10))
	}
	return strings.Join(out, "\n"), nil
}

func (f *fakeGit) Message(dir, sha string) (string, error) {
	if err := f.call("Message", dir, sha); err != nil {
		return "", err
	}
	sha = f.resolve(dir, sha)
	return fmt.Sprintf("%d\n%s", f.dates[sha], f.msgs[sha]), nil
}

// answer is a scripted read's answer.
func (f *fakeGit) answer(method string, args ...string) (string, error) {
	if err := f.call(method, args...); err != nil {
		return "", err
	}
	return f.answers[strings.Join(append([]string{method}, args...), " ")], nil
}

func (f *fakeGit) Changed(from, to string) ([]string, error) {
	out, err := f.answer("Changed", from, to)
	return lines(out), err
}

func (f *fakeGit) Unmerged(dir string) ([]string, error) {
	return f.unmerged[dir], f.call("Unmerged", dir)
}

func (f *fakeGit) Staged(dir, rev string) (string, error) { return f.answer("Staged", dir, rev) }
func (f *fakeGit) Patch(dir, from, to string) (string, error) {
	return f.answer("Patch", dir, f.resolve(dir, from), f.resolve(dir, to))
}
func (f *fakeGit) Diff(from, to string) (string, error) { return f.answer("Diff", from, to) }
func (f *fakeGit) Stat(from, to string) (string, error) { return f.answer("Stat", from, to) }
func (f *fakeGit) Log(args ...string) (string, error)   { return f.answer("Log", args[len(args)-1]) }

func (f *fakeGit) Toplevel(dir string) (string, error) { return dir, f.call("Toplevel", dir) }

func (f *fakeGit) Refs(prefix string) ([]string, error) {
	if err := f.call("Refs", prefix); err != nil {
		return nil, err
	}
	var out []string
	for r := range f.refs {
		if strings.HasPrefix(r, prefix) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (f *fakeGit) AddAll(dir string) error { return f.call("AddAll", dir) }

func (f *fakeGit) Commit(dir, msg, d string) error {
	if err := f.call("Commit", dir, msg, d); err != nil {
		return err
	}
	f.commitOn(dir, msg, date(d))
	return nil
}

func (f *fakeGit) Amend(dir, msg, d string) error {
	if err := f.call("Amend", dir, msg, d); err != nil {
		return err
	}
	old := f.headOf(dir)
	parents := f.parents[old]
	sha := commitSha(append(slices.Clone(parents), msg, d, "amend")...)
	f.parents[sha], f.msgs[sha], f.dates[sha] = parents, msg, date(d)
	f.setHead(dir, sha)
	return nil
}

func (f *fakeGit) Merge(dir, input string) error {
	if err := f.call("Merge", dir, input); err != nil {
		return err
	}
	f.merging[dir] = f.resolve(dir, input)
	if files, ok := f.conflicts[input]; ok {
		f.unmerged[dir] = files
		return fmt.Errorf("git merge in %s: exit status 1", dir)
	}
	return nil
}

func (f *fakeGit) WriteTree(dir string) (string, error) {
	if err := f.call("WriteTree", dir); err != nil {
		return "", err
	}
	return "tree-" + f.merging[dir], nil
}

func (f *fakeGit) UpdateRef(ref, sha string) error {
	if err := f.call("UpdateRef", ref, sha); err != nil {
		return err
	}
	f.refs[ref] = sha
	return nil
}

func (f *fakeGit) DeleteRef(ref string) error {
	if err := f.call("DeleteRef", ref); err != nil {
		return err
	}
	delete(f.refs, ref)
	return nil
}

func (f *fakeGit) FF(sha string) error {
	if err := f.call("FF", sha); err != nil {
		return err
	}
	if !f.isAncestor(f.headOf(f.root), sha) {
		return fmt.Errorf("git merge in %s: Not possible to fast-forward, aborting", f.root)
	}
	f.setHead(f.root, sha)
	return nil
}

func (f *fakeGit) Reset(dir, rev string) error {
	if err := f.call("Reset", dir, rev); err != nil {
		return err
	}
	if rev != "" {
		f.setHead(dir, f.resolve(dir, rev))
	}
	delete(f.status, dir)
	delete(f.merging, dir)
	delete(f.unmerged, dir)
	return nil
}

func (f *fakeGit) Clean(dir string) error { return f.call("Clean", dir) }

func (f *fakeGit) Detach(dir, sha string) error {
	if err := f.call("Detach", dir, sha); err != nil {
		return err
	}
	sha = f.resolve(dir, sha)
	f.branch[dir], f.head[dir] = "", sha
	return nil
}

func (f *fakeGit) Switch(dir, branch string) error {
	if err := f.call("Switch", dir, branch); err != nil {
		return err
	}
	f.branch[dir] = branch
	return nil
}

func (f *fakeGit) AddWorktree(dir, rev string) error {
	if err := f.call("AddWorktree", dir, rev); err != nil {
		return err
	}
	f.head[dir] = f.resolve(f.root, rev)
	return os.MkdirAll(dir, 0o755)
}

// fk is a fixture whose Scratch works on fakeGit: a plain project dir (no git, testkit.NewDir) with the fixture's
// config and lane dirs L-01 (on task/T-05) and L-02; serve's worktree is created by the first gate.
type fk struct {
	*fx
	g *fakeGit
	s Scratch
}

// newFk is the fake fixture; edits change its config as newSx's do.
func newFk(t testing.TB, edits ...func(map[string]any)) *fk {
	t.Helper()
	p := testkit.NewDir(t)
	lane := p.Lane("L-01", "task/T-05")
	p.Lane("L-02", "task/T-20")
	p.Config(fxConfig(edits...)...)
	x := &fx{Project: p, t: t, lane: lane, lane2Dir: p.LaneDir("L-02"), scratch: filepath.Join(p.Base, "proj-wt", "merge"),
		slots: filepath.Join(p.Base, "slots"), sig: make(chan int, 1)}
	if err := os.MkdirAll(p.Path(".obor/state"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := newFakeGit(p.Root, lane)
	return &fk{fx: x, g: g, s: Scratch{Options: x.opts(), fake: g}}
}

// use makes g the fixture's git.
func (k *fk) use(g *fakeGit) { k.g, k.s.fake = g, g }

// main is main's sha.
func (k *fk) main() string { return k.g.refs["refs/heads/main"] }

// move commits on main (another task's merge, say): the new head.
func (k *fk) move(msg string) string {
	return k.g.commitOn(k.Root, msg, k.g.dates[k.main()]+10)
}

// laneCommit is the lane's worker committing its work: the new lane head.
func (k *fk) laneCommit(msg string) string { return k.g.commitOn(k.lane, msg, 50) }

func (k *fk) submit() string {
	k.t.Helper()
	sha, err := k.s.Submit(ctx, "T-05", "1", "L-01", "feature")
	if err != nil {
		k.t.Fatalf("submit: %v", err)
	}
	return sha
}

func (k *fk) gate(in port.GateInput) port.GateOutcome {
	k.t.Helper()
	o, err := k.s.Gate(ctx, in)
	if err != nil {
		k.t.Fatalf("gate %+v: %v\noutput:\n%s", in, err, k.out.String())
	}
	return o
}

func (k *fk) green(in port.GateInput) port.Candidate {
	k.t.Helper()
	o := k.gate(in)
	if o.Kind != port.OutcomeGreen {
		k.t.Fatalf("gate %+v: %s %q; want green\noutput:\n%s", in, o.Kind, o.Line, k.out.String())
	}
	return o.Candidate
}

// conflicted is fx's conflicted on the fake: the lane's submitted sha conflicts with a main edit in shared.txt, the
// conflict open in serve's worktree.
func (k *fk) conflicted() string {
	k.t.Helper()
	k.move("main edit")
	sha := k.submit()
	k.g.conflicts[sha] = []string{"shared.txt"}
	if o := k.gate(lane(sha)); o.Kind != port.OutcomeConflict {
		k.t.Fatalf("precondition: conflict, got %s %q", o.Kind, o.Line)
	}
	return sha
}

// resolveServe is the integrator resolving the open conflict and committing it in serve's worktree (msg: its
// message): the resolution's sha (not pinned).
func (k *fk) resolveServe(msg string) string {
	k.t.Helper()
	if k.g.merging[k.serve()] == "" {
		k.t.Fatal("precondition: a merge open in serve's worktree")
	}
	return k.g.commitOn(k.serve(), msg, k.g.dates[k.main()]+5)
}

// resolved is a conflicted gate resolved and pinned: the submitted sha and the resolution.
func (k *fk) resolved() (string, string) {
	k.t.Helper()
	input := k.conflicted()
	k.resolveServe("Merge T-05: resolved")
	res, err := k.s.Resolved(ctx, "T-05", input, nil)
	if err != nil {
		k.t.Fatalf("precondition: resolved: %v", err)
	}
	return input, res
}

func (k *fk) serve() string { return k.scratch }

// pins is the task's pins as sx.pins has them.
func (k *fk) pins(task string) string {
	var out []string
	for _, ref := range []string{submittedRefs + task + "/1", candidateRefs + task, resolvedRefs + task} {
		v := k.g.refs[ref]
		if v == "" {
			v = "-"
		}
		name := strings.TrimPrefix(ref, "refs/obor/")
		name, _, _ = strings.Cut(name, "/")
		out = append(out, name+"="+v)
	}
	return strings.Join(out, " ")
}
