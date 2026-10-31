package checks

import (
	"context"
	"errors"
	"fmt"
	"obor/internal/app"
	"obor/internal/testkit"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// sx is the fixture with obor serve's Scratch on it, its git calls through a shim that logs each call and fails the
// cut-th one (exit 128, as a git I/O error) when armed.
type sx struct {
	*fx
	s     Scratch
	cut   string // the shim's dir: count, cut, calls
	serve string // serve's worktree
}

func newSx(t *testing.T, edits ...func(map[string]any)) *sx {
	t.Helper()
	x := newFx(t, edits...)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(x.Base, "cut")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := x.stub("git-cut", fmt.Sprintf(`n=$(( $(cat '%[1]s/count' 2>/dev/null || echo 0) + 1 ))
echo $n > '%[1]s/count'
printf '%%s\n' "$*" | tr '\n' ' ' >> '%[1]s/calls'; echo >> '%[1]s/calls'
if [ "$n" = "$(cat '%[1]s/cut' 2>/dev/null)" ]; then echo "cut: git call $n" >&2; exit 128; fi
exec '%[2]s' "$@"`, dir, real))
	o := x.opts()
	o.Git = shim
	return &sx{fx: x, s: Scratch{o}, cut: dir, serve: x.scratch}
}

// arm restarts the shim's count and log and fails its n-th call (0: none).
func (y *sx) arm(n int) {
	y.Write(filepath.Join(y.cut, "count"), "0")
	y.Write(filepath.Join(y.cut, "cut"), strconv.Itoa(n))
	y.Write(filepath.Join(y.cut, "calls"), "")
}

// calls is the git calls since arm, one per line (a message's newlines as spaces).
func (y *sx) calls() []string { return lines(y.Read(filepath.Join(y.cut, "calls"))) }

var ctx = context.Background()

func (y *sx) submit() string {
	y.t.Helper()
	sha, err := y.s.Submit(ctx, "T-05", "1", "O-01", "feature", "pj-o1")
	if err != nil {
		y.t.Fatalf("submit: %v", err)
	}
	return sha
}

func (y *sx) gate(in app.GateInput) app.GateOutcome {
	y.t.Helper()
	o, err := y.s.Gate(ctx, in)
	if err != nil {
		y.t.Fatalf("gate %+v: %v\noutput:\n%s", in, err, y.out.String())
	}
	return o
}

func (y *sx) green(in app.GateInput) app.Candidate {
	y.t.Helper()
	o := y.gate(in)
	if o.Kind != app.OutcomeGreen {
		y.t.Fatalf("gate %+v: %s %q; want green\noutput:\n%s", in, o.Kind, o.Line, y.out.String())
	}
	return o.Candidate
}

func lane(sha string) app.GateInput {
	return app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputLane, Input: sha, Summary: "feature"}
}

// conflicted makes the lane and main both edit shared.txt, submits and gates it: the conflict open in serve's worktree.
// The submitted sha, the resolution's input.
func (y *sx) conflicted() string {
	y.t.Helper()
	y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n")
	y.Write("shared.txt", "main\n")
	y.Git(y.Root, "commit", "-qam", "main edit")
	sha := y.submit()
	if o := y.gate(lane(sha)); o.Kind != app.OutcomeConflict {
		y.t.Fatalf("precondition: conflict, got %s %q", o.Kind, o.Line)
	}
	return sha
}

// resolveServe resolves the open conflict in serve's worktree and commits it (not pinned).
func (y *sx) resolveServe() string {
	y.t.Helper()
	y.Write(filepath.Join(y.serve, "shared.txt"), "both\n")
	y.Git(y.serve, "add", "shared.txt")
	y.Git(y.serve, "commit", "-qm", "Merge T-05: resolved")
	return y.head(y.serve)
}

func (y *sx) tree(sha string) string { y.t.Helper(); return y.Git(y.Root, "rev-parse", sha+"^{tree}") }

// pins is the task's pins, "submitted=<sha> candidate=… resolved=…" ("-": none); submitted is its first submit's.
func (y *sx) pins(task string) string {
	var out []string
	for _, ref := range []string{submittedRefs, candidateRefs, resolvedRefs} {
		name := ref + task
		if ref == submittedRefs {
			name += "/1"
		}
		v := y.ref(name)
		if v == "" {
			v = "-"
		}
		out = append(out, strings.TrimPrefix(strings.TrimSuffix(ref, "/"), "refs/obor/")+"="+v)
	}
	return strings.Join(out, " ")
}

// gateNorm is a gate outcome comparable across fixtures: the candidate by its tree and its patch, not its shas.
func (y *sx) gateNorm(o app.GateOutcome) string {
	c := o.Candidate
	if o.Kind != app.OutcomeGreen {
		return o.Kind + " " + strings.ReplaceAll(o.Line, y.Base, "$BASE")
	}
	return fmt.Sprintf("green tree=%s patch=%s checks=%q stat=%q full=%v target-is-main=%v line=%q", y.tree(c.SHA), c.Patch,
		c.Checks, c.Stat, c.Full, c.Target == y.Git(y.Root, "rev-parse", "HEAD"), o.Line)
}

// cutStep is one Scratch step of the cut table: setup brings a fresh fixture to the step's start, call runs the step
// (its outcome comparable across fixtures, or its error), end asserts the spec'd end state after it.
type cutStep struct {
	name  string
	edits []func(map[string]any)
	setup func(y *sx) cutIn
	call  func(y *sx, in cutIn) (string, any, error)
	end   func(y *sx, in cutIn, out any)
}

type cutIn struct {
	sha string // the step's input sha
	c   app.Candidate
}

func gateStep(kind string) func(y *sx, in cutIn) (string, any, error) {
	return func(y *sx, in cutIn) (string, any, error) {
		gi := lane(in.sha)
		gi.Kind = kind
		o, err := y.s.Gate(ctx, gi)
		return y.gateNorm(o), o, err
	}
}

func publishStep(y *sx, in cutIn) (string, any, error) {
	o, err := y.s.Publish(ctx, in.c)
	kind := o.Kind
	if kind == app.PublishAlready {
		kind = app.PublishMerged // a repeat after the fast-forward
	}
	return fmt.Sprintf("%s head=%q files=%v", kind, o.Head, o.Files), o, err
}

// T-360 (RV-39 #7): a moved-target gate keeps the task's subject rule — the supplied summary is the merge subject,
// the move goes in the body — not "Merge T-xx into moved <branch>".
func TestRV39MovedSubject(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	sha := y.submit()
	y.green(lane(sha))
	y.Write("other.txt", "other\n")
	y.Git(y.Root, "add", "other.txt")
	y.Git(y.Root, "commit", "-qm", "unrelated")
	o := y.gate(app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputMoved, Input: sha, Summary: "Expected spec title"})
	if o.Kind != app.OutcomeGreen {
		t.Fatalf("moved gate: %s %q; want green", o.Kind, o.Line)
	}
	if got := y.Git(y.Root, "log", "-1", "--format=%s", o.Candidate.SHA); got != "Merge T-05: Expected spec title" {
		t.Errorf("moved merge subject %q, want %q", got, "Merge T-05: Expected spec title")
	}
	if got := y.Git(y.Root, "log", "-1", "--format=%b", o.Candidate.SHA); !strings.Contains(got, "moved main") {
		t.Errorf("moved merge body %q, want the moved target named", got)
	}
}

// T-403: a moved gate merges the submitted sha, never the old candidate: main gets one merge commit for the task,
// whose second parent is the lane commit, and an unrelated move keeps the patch (accept.kept keeps the task).
func TestScratchMovedGateMergesSubmitted(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	sha := y.submit()
	old := y.green(lane(sha))
	y.Write("other.txt", "other\n")
	y.Git(y.Root, "add", "other.txt")
	y.Git(y.Root, "commit", "-qm", "unrelated")
	c := y.green(app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputMoved, Input: sha, Summary: "feature"})
	y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^2"), sha, "the moved merge's second parent is the submitted sha")
	if gitOK(y.Root, "merge-base", "--is-ancestor", old.SHA, c.SHA) {
		t.Errorf("%s is an ancestor of the moved candidate: the old merge went along", old.SHA)
	}
	y.eq(c.Patch, old.Patch, "an unrelated move keeps the patch: kept, no new review")
	o, err := y.s.Publish(ctx, c)
	if err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("publish the moved candidate: %+v %v", o, err)
	}
	y.eq(y.Git(y.Root, "rev-list", "--count", "--merges", "main"), "1", "one merge commit for the task on main")
	y.eq(y.Git(y.Root, "rev-parse", "main^2"), sha, "main's merge merges the submitted sha")
}

// T-403: the moved gate's merge of the submitted sha conflicts as a lane gate's does; the resolution is accepted
// against that sha and gates as committed, as before.
func TestScratchMovedGateConflict(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	y.Write(filepath.Join(y.lane, "shared.txt"), "lane\n")
	sha := y.submit()
	y.green(lane(sha))
	y.Write("shared.txt", "main\n")
	y.Git(y.Root, "commit", "-qam", "main edit")
	o := y.gate(app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputMoved, Input: sha, Summary: "feature"})
	if o.Kind != app.OutcomeConflict {
		t.Fatalf("moved gate: %s %q; want a conflict", o.Kind, o.Line)
	}
	y.eq(strings.Join(o.Files, " "), "shared.txt", "the conflicting file")
	res := y.resolveServe()
	got, err := y.s.Resolved(ctx, "T-05", sha)
	if err != nil {
		t.Fatalf("resolve of %s: %v", sha, err)
	}
	y.eq(got, res, "the resolution's sha pinned against the submitted sha")
	c := y.green(app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputResolved, Input: res})
	y.eq(c.SHA+" "+c.Target, res+" "+y.Git(y.Root, "rev-parse", res+"^1"), "the pinned resolution, against its first parent")
}

var cutSteps = []cutStep{
	{
		name:  "submit",
		setup: func(y *sx) cutIn { return cutIn{} },
		call: func(y *sx, _ cutIn) (string, any, error) {
			sha, err := y.s.Submit(ctx, "T-05", "1", "O-01", "feature", "pj-o1")
			if err != nil {
				return "", nil, err
			}
			return fmt.Sprintf("tree=%s commits=%s subject=%q", y.tree(sha), y.Git(y.Root, "rev-list", "--count", "main.."+sha),
				y.Git(y.Root, "log", "-1", "--format=%s", sha)), sha, nil
		},
		end: func(y *sx, _ cutIn, out any) {
			sha := out.(string)
			y.eq(sha, y.head(y.lane), "the lane's HEAD submitted")
			y.eq(y.ref(submittedRefs+"T-05/1"), sha, "submitted pin")
			y.eq(y.Git(y.lane, "status", "--porcelain"), "", "lane committed")
		},
	},
	{
		name:  "gate lane",
		edits: []func(map[string]any){cmd("check", "echo 1 pass")},
		setup: func(y *sx) cutIn { return cutIn{sha: y.submit()} },
		call:  gateStep(app.InputLane),
		end: func(y *sx, in cutIn, out any) {
			c := out.(app.GateOutcome).Candidate
			y.eq(y.pins("T-05"), "submitted="+in.sha+" candidate="+c.SHA+" resolved=-", "pins")
			y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^1")+" "+y.Git(y.Root, "rev-parse", c.SHA+"^2"), c.Target+" "+in.sha,
				"the candidate merges the submitted sha onto the target")
			y.eq(c.Target, y.head(y.Root), "the target untouched")
			y.eq(y.head(y.serve), c.SHA, "serve's worktree at the candidate")
			y.eq(y.Git(y.serve, "status", "--porcelain"), "", "serve's worktree clean")
		},
	},
	{
		name:  "gate resolved",
		edits: []func(map[string]any){cmd("check", "echo 1 pass")},
		setup: func(y *sx) cutIn {
			input := y.conflicted()
			y.resolveServe()
			sha, err := y.s.Resolved(ctx, "T-05", input)
			if err != nil {
				y.t.Fatalf("precondition: resolved: %v", err)
			}
			return cutIn{sha: sha}
		},
		call: gateStep(app.InputResolved),
		end: func(y *sx, in cutIn, out any) {
			c := out.(app.GateOutcome).Candidate
			y.eq(c.SHA, in.sha, "the resolution is the candidate")
			y.eq(c.Target, y.Git(y.Root, "rev-parse", in.sha+"^1"), "checked against its first parent")
			y.eq(y.ref(candidateRefs+"T-05")+" "+y.ref(resolvedRefs+"T-05"), in.sha+" "+in.sha, "pins")
			y.eq(y.head(y.serve), in.sha, "serve's worktree at the resolution")
		},
	},
	{
		name:  "gate moved",
		edits: []func(map[string]any){cmd("check", "echo 1 pass")},
		setup: func(y *sx) cutIn {
			sha := y.submit()
			c := y.green(lane(sha))
			y.Write("other.txt", "other\n")
			y.Git(y.Root, "add", "other.txt")
			y.Git(y.Root, "commit", "-qm", "unrelated")
			return cutIn{sha: sha, c: c}
		},
		call: gateStep(app.InputMoved),
		end: func(y *sx, in cutIn, out any) {
			c := out.(app.GateOutcome).Candidate
			y.eq(c.Target, y.head(y.Root), "checked on the moved target")
			y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^2"), in.sha, "merges the submitted sha, not the old candidate (T-403)")
			y.eq(c.Patch, in.c.Patch, "an unrelated move keeps the patch")
			y.eq(y.ref(candidateRefs+"T-05"), c.SHA, "the new candidate pinned")
		},
	},
	{
		name:  "publish",
		edits: []func(map[string]any){cmd("check", "echo 1 pass")},
		setup: func(y *sx) cutIn { return cutIn{c: y.green(lane(y.submit()))} },
		call:  publishStep,
		end: func(y *sx, in cutIn, _ any) {
			y.eq(y.head(y.Root), in.c.SHA, "the target fast-forwarded to the candidate")
			y.eq(y.pins("T-05"), "submitted=- candidate=- resolved=-", "pins deleted")
			y.eq(y.Git(y.Root, "status", "--porcelain"), "", "main checkout clean")
		},
	},
	{
		name: "after_merge",
		edits: []func(map[string]any){cmd("after_merge",
			map[string]any{"cmd": `echo ran >> "$TMP_DIR/after.ran"`, "when": []any{"*.txt"}})},
		setup: func(y *sx) cutIn {
			c := y.green(lane(y.submit()))
			if o, err := y.s.Publish(ctx, c); err != nil || o.Kind != app.PublishMerged {
				y.t.Fatalf("precondition: publish: %+v %v", o, err)
			}
			return cutIn{c: c}
		},
		call: func(y *sx, in cutIn) (string, any, error) {
			line, err := y.s.AfterMerge(ctx, in.c)
			return line, line, err
		},
		end: func(y *sx, in cutIn, _ any) {
			y.eq(y.Read(filepath.Join(y.Base, "after.ran")), "ran\n", "after_merge ran once")
			y.gone(y.Path(".obor/state/merges.log"), "no merges.log (the store's fact merged, T-525)")
		},
	},
	{
		name: "resolved verdict",
		setup: func(y *sx) cutIn {
			input := y.conflicted()
			return cutIn{sha: y.resolveServe(), c: app.Candidate{SHA: input}}
		},
		call: func(y *sx, in cutIn) (string, any, error) {
			sha, err := y.s.Resolved(ctx, "T-05", in.c.SHA)
			if err != nil {
				return "", nil, err
			}
			return "tree=" + y.tree(sha), sha, nil
		},
		end: func(y *sx, in cutIn, out any) {
			y.eq(out.(string), in.sha, "the resolution's sha")
			y.eq(y.ref(resolvedRefs+"T-05"), in.sha, "resolved pin")
		},
	},
	{
		name:  "unpin",
		setup: func(y *sx) cutIn { return cutIn{c: y.green(lane(y.submit()))} },
		call: func(y *sx, in cutIn) (string, any, error) {
			return "unpinned", nil, y.s.Unpin(ctx, "T-05")
		},
		end: func(y *sx, _ cutIn, _ any) {
			y.eq(y.pins("T-05"), "submitted=- candidate=- resolved=-", "pins deleted")
		},
	},
}

// The cut table: for each step and every N, the shim fails the step's N-th git
// call; that run is an error (not decided), and the step run again gives the uncut run's outcome and end state. The
// last N is the first one the step never reaches: that run is the uncut one.
func TestScratchCutTable(t *testing.T) {
	t.Parallel()
	for _, st := range cutSteps {
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()
			ref := newSx(t, st.edits...)
			in := st.setup(ref)
			ref.arm(0)
			want, out, err := st.call(ref, in)
			if err != nil {
				t.Fatalf("uncut: %v\noutput:\n%s", err, ref.out.String())
			}
			st.end(ref, in, out)
			calls := len(ref.calls())
			if calls == 0 {
				t.Fatal("precondition: the step calls git")
			}
			for n := 1; n <= calls; n++ {
				y := newSx(t, st.edits...)
				in := st.setup(y)
				y.arm(n)
				got, _, err := st.call(y, in)
				cut := y.calls()
				if len(cut) < n {
					t.Fatalf("cut %d: the step made only %d git calls (uncut: %d)", n, len(cut), calls)
				}
				if err == nil {
					t.Fatalf("cut %d (git %s): outcome %q, want an error", n, cut[n-1], got)
				}
				y.arm(0)
				got, out, err := st.call(y, in)
				if err != nil {
					t.Fatalf("cut %d (git %s), run again: %v\noutput:\n%s", n, cut[n-1], err, y.out.String())
				}
				if got != want {
					t.Errorf("cut %d (git %s), run again: %s\nwant %s", n, cut[n-1], got, want)
				}
				st.end(y, in, out)
			}
		})
	}
}

// A lane gate from any foreign state of serve's worktree (an open merge, a dirty tree, HEAD elsewhere, the worktree
// removed) is green with the same candidate as from a clean one.
func TestScratchGateForeignState(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	sha := y.submit()
	want := y.green(lane(sha))
	y.Write(filepath.Join(y.lane2, "other.txt"), "other\n")
	y.Git(y.lane2, "add", "other.txt")
	y.Git(y.lane2, "commit", "-qm", "other")
	for _, c := range []struct {
		name  string
		state func()
	}{
		{"open merge", func() {
			y.Git(y.serve, "merge", "-q", "--no-commit", "--no-ff", "task/T-20")
			if !gitOK(y.serve, "rev-parse", "-q", "--verify", "MERGE_HEAD") {
				t.Fatal("precondition: MERGE_HEAD")
			}
		}},
		{"dirty tree", func() {
			y.Write(filepath.Join(y.serve, "shared.txt"), "dirty\n")
			y.Write(filepath.Join(y.serve, "stray.txt"), "stray\n")
		}},
		{"HEAD elsewhere", func() { y.Git(y.serve, "checkout", "-q", "--detach", "task/T-20") }},
		{"worktree missing", func() {
			if err := os.RemoveAll(y.serve); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		c.state()
		got := y.green(lane(sha))
		y.eq(fmt.Sprintf("%+v", got), fmt.Sprintf("%+v", want), c.name+": the same candidate")
		y.eq(y.Git(y.serve, "status", "--porcelain"), "", c.name+": serve's worktree clean")
	}
}

// A resolved gate after a red one, with the worktree reset by another gate meanwhile: reset to the pin, green, its
// target the resolution's first parent.
func TestScratchGateResolvedAfterRed(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", `test -f "$TMP_DIR/green" && echo 1 pass || { echo 0 pass; exit 1; }`))
	input := y.conflicted()
	y.resolveServe()
	sha, err := y.s.Resolved(ctx, "T-05", input)
	if err != nil {
		t.Fatal(err)
	}
	resolved := app.GateInput{Task: "T-05", Lane: "O-01", Kind: app.InputResolved, Input: sha}
	if o := y.gate(resolved); o.Kind != app.OutcomeRed {
		t.Fatalf("precondition: red, got %s %q", o.Kind, o.Line)
	}
	y.Write(filepath.Join(y.Base, "green"), "")
	y.Write(filepath.Join(y.lane2, "other.txt"), "other\n")
	sha2, err := y.s.Submit(ctx, "T-20", "1", "O-02", "other", "pj-o2")
	if err != nil {
		t.Fatal(err)
	}
	y.green(app.GateInput{Task: "T-20", Lane: "O-02", Kind: app.InputLane, Input: sha2, Summary: "other"})
	if y.head(y.serve) == sha {
		t.Fatal("precondition: another gate moved serve's worktree off the resolution")
	}
	c := y.green(resolved)
	y.eq(c.SHA+" "+c.Target, sha+" "+y.Git(y.Root, "rev-parse", sha+"^1"), "the pinned resolution, against its first parent")
	y.eq(y.head(y.serve), sha, "serve's worktree reset to the pin")
}

// Submit twice commits once and pins the same sha; a worker commit after the submission is not what the gate merges.
func TestScratchSubmitTwice(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	base := y.head(y.lane)
	a := y.submit()
	b := y.submit()
	y.eq(b, a, "the same sha")
	y.eq(y.Git(y.lane, "rev-list", "--count", base+".."+a), "1", "one commit")
	y.change("late.txt", "late")
	y.Git(y.lane, "add", "late.txt")
	y.Git(y.lane, "commit", "-qm", "late")
	c := y.green(lane(y.ref(submittedRefs + "T-05/1")))
	y.eq(y.Git(y.Root, "rev-parse", c.SHA+"^2"), a, "the gate merges the pinned sha")
	y.gone(filepath.Join(y.serve, "late.txt"), "the late commit not merged")

	if _, err := y.s.Submit(ctx, "T-05", "2", "O-09", "x", "w"); !errors.As(err, new(app.Refused)) {
		t.Errorf("no lane: %v; want Refused", err)
	}
	if _, err := y.s.Submit(ctx, "T-20", "1", "O-02", "x", "w"); !errors.As(err, new(app.Refused)) {
		t.Errorf("lane already in the target: %v; want Refused", err)
	}
}

// Publish: a dirty merged file holds without a merge call; an index.lock a live git holds is waited for, then merged; a
// moved target is Moved with its head; a landed candidate is Already.
func TestScratchPublish(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	c := y.green(lane(y.submit()))
	base := y.head(y.Root)

	y.Write("feature.txt", "mine\n")
	y.arm(0)
	o, err := y.s.Publish(ctx, c)
	y.eq(fmt.Sprintf("%s %v %v", o.Kind, o.Files, err), "hold [feature.txt] <nil>", "dirty merged file")
	for _, call := range y.calls() {
		y.lacks(call, "merge -q --ff-only", "no merge call on hold")
	}
	y.eq(y.head(y.Root), base, "nothing merged on hold")
	os.Remove(y.Path("feature.txt"))

	lock := y.Path(".git/index.lock")
	y.Write(lock, "")
	old := time.Now().Add(-2 * staleLock) // stale but for its live holder (T-382)
	os.Chtimes(lock, old, old)
	asked, held := 0, ""
	y.s.GitAlive = func(string) bool {
		if asked++; asked == 2 { // the holder ends after a look
			held = y.head(y.Root)
			os.Remove(lock)
		}
		return true
	}
	o, err = y.s.Publish(ctx, c)
	y.s.GitAlive = nil
	y.eq(fmt.Sprintf("%s %v", o.Kind, err), "merged <nil>", "merged once the lock goes")
	y.eq(held, base, "nothing merged under the lock")
	y.eq(y.head(y.Root), c.SHA, "fast-forwarded")
	o, err = y.s.Publish(ctx, c)
	y.eq(fmt.Sprintf("%s %v", o.Kind, err), "already <nil>", "a landed candidate")

	z := newSx(t, cmd("check", "echo 1 pass"))
	c = z.green(lane(z.submit()))
	z.Write("other.txt", "other\n")
	z.Git(z.Root, "add", "other.txt")
	z.Git(z.Root, "commit", "-qm", "unrelated")
	o, err = z.s.Publish(ctx, c)
	z.eq(fmt.Sprintf("%s %s %v", o.Kind, o.Head, err), "moved "+z.head(z.Root)+" <nil>", "moved target")
	z.eq(z.ref(candidateRefs+"T-05"), c.SHA, "a moved candidate stays pinned")
}

// AfterMerge runs the command when its when matches, else skips it; it keeps no record of its own (T-525: its intent in
// the store is the once), so a second call runs it again, and it writes no merges.log.
func TestScratchAfterMerge(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ when, first string }{{"*.txt", "ran"}, {"cmd/**", "skipped"}} {
		y := newSx(t, cmd("check", "echo 1 pass"),
			cmd("after_merge", map[string]any{"cmd": `echo ran >> "$TMP_DIR/after.ran"`, "when": []any{c.when}}))
		cand := y.green(lane(y.submit()))
		if o, err := y.s.Publish(ctx, cand); err != nil || o.Kind != app.PublishMerged {
			t.Fatalf("precondition: publish %+v %v", o, err)
		}
		for i, want := range []string{c.first, c.first} {
			line, err := y.s.AfterMerge(ctx, cand)
			y.eq(fmt.Sprintf("%s %v", line, err), want+" <nil>", fmt.Sprintf("when %s, call %d", c.when, i+1))
		}
		ran := ""
		if c.first == "ran" {
			ran = "ran\nran\n"
		}
		if y.Exists(filepath.Join(y.Base, "after.ran")) {
			y.eq(y.Read(filepath.Join(y.Base, "after.ran")), ran, "when "+c.when+": runs")
		} else {
			y.eq("", ran, "when "+c.when+": runs")
		}
		y.gone(y.Path(".obor/state/merges.log"), "when "+c.when+": no merges.log")
	}
}

// RV-40 #1: serve's gate does not reuse a worker's receipt once the guard has staged a change; the check runs on the
// tree it merges, which holds the guard's file. Without that change the same receipt is reused.
func TestScratchGateReceiptGuardMutation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, guard, want string }{
		{"the guard stages a changed file", `echo broken > feature.txt; git add feature.txt; echo 1 pass`, `^RED check \(rc=1, partial:any\)`},
		{"the guard changes nothing", `echo 1 pass`, `^GREEN T-05 \(guard 1 pass, check reused O-01 [0-9a-f]{7}, partial:any\)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			y := newSx(t,
				cmd("check", map[string]any{"cmd": `if grep -q broken feature.txt; then echo 1 fail; exit 1; fi; echo 4 pass`, "heavy": true, "timeout": 20}),
				cmd("guard", map[string]any{"cmd": tc.guard}),
				testkit.Set("merge", map[string]any{"trailer": trailer, "summary_regex": "[0-9]+ pass", "guard": "guard", "receipt_max_age": 3600,
					"classes": []any{map[string]any{"name": "any", "paths": []any{"**"}, "checks": []any{"check"}}}}))
			y.Git(y.lane, "merge", "-q", "--ff-only", "main") // the lane is based on the current target, as right after a submit
			y.Write(filepath.Join(y.lane, "feature.txt"), "fine\n")
			if rc := y.worker("check"); rc != 0 {
				t.Fatalf("the worker's run: %d", rc)
			}
			o := y.gate(lane(y.submit()))
			y.match(o.Line, tc.want, "the gate's line")
		})
	}
}

// T-425: the target is branches.main whatever the main checkout has checked out. On another branch the gate merges
// onto main, publish refuses (its ff would move the other branch) and Landed reads main's log; back on main, publish
// merges.
func TestScratchTargetIsBranchesMain(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	sha := y.submit()
	main := y.head(y.Root)
	y.Git(y.Root, "switch", "-q", "-c", "side")
	y.Write("side.txt", "side\n")
	y.Git(y.Root, "add", "side.txt")
	y.Git(y.Root, "commit", "-qm", "Merge T-05: only on side")
	side := y.head(y.Root)
	if side == main {
		t.Fatal("precondition: side moved off main")
	}

	c := y.green(lane(sha))
	y.eq(c.Target, main, "the gate's target is main, not the checked-out side")
	if landed, err := y.s.Landed(ctx, "T-05"); landed || err != nil {
		t.Errorf("Landed with side's merge subject only: %v %v; want false (reads main)", landed, err)
	}
	o, err := y.s.Publish(ctx, c)
	if err == nil || !strings.Contains(err.Error(), "is not on main") {
		t.Errorf("publish off main: %s %v; want the not-on-main error", o.Kind, err)
	}
	y.eq(y.Git(y.Root, "rev-parse", "refs/heads/main"), main, "main unmoved")
	y.eq(y.head(y.Root), side, "side unmoved")

	y.Git(y.Root, "switch", "-q", "main")
	if o, err := y.s.Publish(ctx, c); err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("publish on main: %s %v", o.Kind, err)
	}
	y.Git(y.Root, "switch", "-q", "--detach", "side")
	if landed, err := y.s.Landed(ctx, "T-05"); !landed || err != nil {
		t.Errorf("Landed after the publish, checkout detached on side: %v %v; want true (reads main)", landed, err)
	}
}

// T-550 (RV-57 #1): an old spec commit on main — "T-05: spec" touching only the legacy spec dir — is no merge
// evidence: work that lives only in the lane has not landed. The default legacy path and a configured paths.tasks.
func TestLegacySpecIsNotLanded(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		edit func(map[string]any)
		path string
	}{
		{"default legacy path", nil, "docs/tasks/T-05.md"},
		{"configured paths.tasks", testkit.Set("paths.tasks", "specs"), "specs/T-05.md"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var edits []func(map[string]any)
			if c.edit != nil {
				edits = append(edits, c.edit)
			}
			y := newSx(t, edits...)
			y.Write(c.path, "spec\n")
			y.Git(y.Root, "add", c.path)
			y.Git(y.Root, "commit", "-qm", "T-05: spec")
			y.Git(y.lane, "add", "feature.txt")
			y.Git(y.lane, "commit", "-qm", "T-05: feature")
			// Preconditions: the old spec is main's newest commit and the lane's work is not on main.
			y.eq(y.Git(y.Root, "log", "-1", "--format=%s"), "T-05: spec", "main's newest subject")
			if err := exec.Command("git", "-C", y.Root, "merge-base", "--is-ancestor", y.head(y.lane), "main").Run(); err == nil {
				t.Fatal("precondition: the lane's work is not on main")
			}
			if landed, err := y.s.Landed(ctx, "T-05"); landed || err != nil {
				t.Errorf("Landed with only T-05's old spec on main: %v %v; want false", landed, err)
			}
			y.Git(y.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-05: feature")
			if landed, err := y.s.Landed(ctx, "T-05"); !landed || err != nil {
				t.Errorf("Landed after the merge: %v %v; want true", landed, err)
			}
		})
	}
}

// RV-38 #10: a submit replayed after its result was lost returns its pin, touching nothing: a worker commit since stays
// out. Another submit (a new attempt, submit.N) pins the lane as it is now under its own ref. A pin from before submits
// had an id (refs/obor/submitted/<task>) gives way.
func TestSubmitPinReplay(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	y.Git(y.Root, "update-ref", submittedRefs+"T-05", "HEAD")
	first := y.submit()
	y.eq(y.Git(y.Root, "for-each-ref", "--format=%(refname)", submittedRefs), submittedRefs+"T-05/1", "the old pin gone")
	y.change("late.txt", "later worker work")
	y.Git(y.lane, "add", "late.txt")
	y.Git(y.lane, "commit", "-qm", "later")
	y.arm(0)
	again := y.submit()
	y.eq(again, first, "the replay returns the pinned sha")
	y.eq(y.ref(submittedRefs+"T-05/1"), first, "the pin unchanged")
	if calls := y.calls(); len(calls) != 1 || !strings.Contains(calls[0], "rev-parse -q --verify "+submittedRefs+"T-05/1") {
		t.Errorf("replay git calls %q; want only the pin read", calls)
	}
	if gitOK(y.Root, "cat-file", "-e", first+":late.txt") {
		t.Error("late.txt is in the replayed submission")
	}

	next, err := y.s.Submit(ctx, "T-05", "2", "O-01", "feature", "pj-o1")
	if err != nil {
		t.Fatal(err)
	}
	y.eq(next, y.head(y.lane), "a new attempt pins the lane's HEAD")
	y.eq(y.ref(submittedRefs+"T-05/2"), next, "under its own ref")
	y.eq(y.ref(submittedRefs+"T-05/1"), first, "the first pin kept")
	if !gitOK(y.Root, "cat-file", "-e", next+":late.txt") {
		t.Error("late.txt is not in the new attempt's submission")
	}

	// Unpin deletes every submit's pin (and a pin from before submits had an id).
	y.Git(y.Root, "update-ref", submittedRefs+"T-050", first)
	if err := y.s.Unpin(ctx, "T-05"); err != nil {
		t.Fatal(err)
	}
	y.eq(y.Git(y.Root, "for-each-ref", "--format=%(refname)", submittedRefs), submittedRefs+"T-050",
		"T-05's pins gone, T-050's kept")
}

// RV-38 #9: Resolved pins only the task's own merge onto the target. Another task's gate in serve's worktree (the app
// no longer runs one while T-05's resolution is pending: TestPausedScratchReservation) leaves a HEAD Resolved
// refuses, instead of pinning T-20's candidate for T-05; so does a merge of T-05's input onto something else. T-05's
// own committed resolution is pinned, with its human edit.
func TestResolvedPinsOwnMergeOnly(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	input := y.conflicted()
	y.Write(filepath.Join(y.serve, "shared.txt"), "human resolution\n")

	y.Write(filepath.Join(y.lane2, "other.txt"), "other\n")
	sha2, err := y.s.Submit(ctx, "T-20", "1", "O-02", "other", "w")
	if err != nil {
		t.Fatal(err)
	}
	c := y.green(app.GateInput{Task: "T-20", Lane: "O-02", Kind: app.InputLane, Input: sha2, Summary: "other"})
	if y.head(y.serve) != c.SHA {
		t.Fatal("precondition: T-20's candidate in serve's worktree")
	}
	if _, err := y.s.Resolved(ctx, "T-05", input); !errors.As(err, new(app.Refused)) {
		t.Errorf("Resolved(T-05) on T-20's candidate: %v; want Refused", err)
	}
	y.eq(y.ref(resolvedRefs+"T-05"), "", "nothing pinned for T-05")

	y.Git(y.serve, "reset", "-q", "--hard", sha2)
	y.Git(y.serve, "merge", "-q", "--no-ff", "-X", "theirs", "-m", "Merge T-05 onto T-20's lane", input)
	if _, err := y.s.Resolved(ctx, "T-05", input); !errors.As(err, new(app.Refused)) {
		t.Errorf("Resolved(T-05) on a merge of its input onto another lane: %v; want Refused", err)
	}

	z := newSx(t, cmd("check", "echo 1 pass"))
	input = z.conflicted()
	z.Write(filepath.Join(z.serve, "shared.txt"), "human resolution\n")
	z.Git(z.serve, "add", "shared.txt")
	z.Git(z.serve, "commit", "-qm", "Merge T-05: resolved")
	sha, err := z.s.Resolved(ctx, "T-05", input)
	if err != nil {
		t.Fatal(err)
	}
	z.eq(z.Git(z.Root, "show", sha+":shared.txt"), "human resolution", "the human resolution pinned")
	z.eq(z.ref(resolvedRefs+"T-05"), sha, "resolved pin")
}

// RV-38 #12: the adapter's git ends with its ctx. Canceled before entry: no git call runs, nothing changes. Canceled
// while a git call blocks (a hook's child, say): the call's process group is killed, Submit returns promptly with no
// child left, and run again it completes.
func TestGitEndsWithCtx(t *testing.T) {
	t.Parallel()
	y := newSx(t)
	y.change("feature2.txt", "more")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	y.arm(0)
	if _, err := y.s.Submit(canceled, "T-05", "1", "O-01", "feature", "w"); err == nil {
		t.Fatal("canceled Submit: no error")
	}
	y.eq(strings.Join(y.calls(), "|"), "", "no git call ran")
	y.eq(y.ref(submittedRefs+"T-05/1"), "", "no pin")
	y.contains(y.Git(y.lane, "status", "--porcelain"), "feature2.txt", "the lane not committed")

	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	child, blocked := filepath.Join(y.Base, "child.pid"), filepath.Join(y.Base, "blocked")
	shim := y.stub("git-block", fmt.Sprintf(`case "$*" in *update-ref*)
  if [ ! -f '%[2]s' ]; then sleep 30 & echo $! > '%[1]s'; : > '%[2]s'; wait; fi;; esac
exec '%[3]s' "$@"`, child, blocked, real))
	o := y.opts()
	o.Git = shim
	c, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		y.waitFor(blocked)
		cancel()
	}()
	at := time.Now()
	if _, err := (Scratch{o}).Submit(c, "T-05", "1", "O-01", "feature", "w"); err == nil {
		t.Fatal("Submit canceled during a blocked git call: no error")
	}
	if d := time.Since(at); d > 10*time.Second {
		t.Errorf("Submit returned after %s; want promptly", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(y.Read(child)))
	if err != nil {
		t.Fatal(err)
	}
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !gone; time.Sleep(20 * time.Millisecond) {
		gone = syscall.Kill(pid, 0) != nil
	}
	if !gone {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the blocked git's child %d outlived the cancel", pid)
	}
	sha, err := (Scratch{o}).Submit(ctx, "T-05", "1", "O-01", "feature", "w")
	if err != nil {
		t.Fatal(err)
	}
	y.eq(sha, y.head(y.lane), "run again, the submit completes")
	y.eq(y.ref(submittedRefs+"T-05/1"), sha, "and pins")
}

// RV-38 #11: Landed returns git's error instead of "not landed"; it still finds a merge.
func TestLandedReturnsGitError(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	y.arm(1)
	if landed, err := y.s.Landed(ctx, "T-05"); err == nil {
		t.Errorf("Landed with git failing: %v, nil; want the error", landed)
	}
	y.arm(0)
	if landed, err := y.s.Landed(ctx, "T-05"); landed || err != nil {
		t.Errorf("Landed before the merge: %v %v; want false", landed, err)
	}
	if o, err := y.s.Publish(ctx, y.green(lane(y.submit()))); err != nil || o.Kind != app.PublishMerged {
		t.Fatalf("precondition: publish %+v %v", o, err)
	}
	if landed, err := y.s.Landed(ctx, "T-05"); !landed || err != nil {
		t.Errorf("Landed after the publish: %v %v; want true", landed, err)
	}
}

// T-602: the gate keeps the candidate's whole diff (git diff target sha, as the patch hash reads it) for the review
// job when it is at most app.InlineDiff bytes; a larger one is not kept.
func TestScratchGateKeepsSmallDiff(t *testing.T) {
	t.Parallel()
	y := newSx(t, cmd("check", "echo 1 pass"))
	c := y.green(lane(y.submit()))
	want := y.Git(y.Root, "diff", c.Target, c.SHA)
	if !strings.Contains(want, "+feature") {
		t.Fatalf("precondition: the candidate's diff %q has no feature line", want)
	}
	y.eq(c.Diff, want, "a small candidate's diff kept")

	big := newSx(t, cmd("check", "echo 1 pass"))
	big.Write(filepath.Join(big.lane, "big.txt"), strings.Repeat("a line of the big file\n", app.InlineDiff/20))
	c = big.green(lane(big.submit()))
	if n := len(big.Git(big.Root, "diff", c.Target, c.SHA)); n <= app.InlineDiff {
		t.Fatalf("precondition: the big diff is %d bytes, not over %d", n, app.InlineDiff)
	}
	y.eq(c.Diff, "", "a diff over InlineDiff not kept")
	if c.Patch == "" || c.Stat == "" {
		t.Errorf("the big candidate lost its patch %q or stat %q", c.Patch, c.Stat)
	}
}
