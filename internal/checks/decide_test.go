package checks

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"obor/internal/app/port"
	"obor/internal/testkit"
)

// T-1011: Scratch's decisions over git's answers, on fakeGit (fakegit_test.go). What real git answers is
// TestScratchGitScenarios' (scratch_test.go).

// norm is s with the fixture's base dir as $BASE: an outcome comparable across fixtures.
func (k *fk) norm(s string) string { return strings.ReplaceAll(s, k.Base, "$BASE") }

// laneMsg is the submit commit's message for summary by pj-l1.
const laneMsg = "T-05: feature\n\nImplemented by pj-l1, reviewed by integrator.\n\n" + trailer

// fakeStep is one Scratch step of the cut table: setup brings a fresh fixture to the step's start, call runs the
// step (its outcome comparable across fixtures, or its error), end asserts the spec'd end state after it.
type fakeStep struct {
	name  string
	edits []func(map[string]any)
	setup func(k *fk) cutIn
	call  func(k *fk, in cutIn) (string, any, error)
	end   func(k *fk, in cutIn, out any)
}

type cutIn struct {
	sha string // the step's input sha
	c   port.Candidate
}

func fakeGate(kind string) func(k *fk, in cutIn) (string, any, error) {
	return func(k *fk, in cutIn) (string, any, error) {
		gi := lane(in.sha)
		gi.Kind = kind
		o, err := k.s.Gate(ctx, gi)
		return fmt.Sprintf("%s %q %+v", o.Kind, k.norm(o.Line), o.Candidate), o, err
	}
}

var fakeSteps = []fakeStep{
	{
		name:  "submit",
		setup: func(k *fk) cutIn { return cutIn{} },
		call: func(k *fk, _ cutIn) (string, any, error) {
			sha, err := k.s.Submit(ctx, "T-05", "1", "L-01", "feature", "pj-l1")
			return sha, sha, err
		},
		end: func(k *fk, _ cutIn, out any) {
			sha := out.(string)
			k.eq(sha, k.g.headOf(k.lane), "the lane's HEAD submitted")
			k.eq(k.g.msgs[sha], laneMsg, "the lane commit's message")
			k.eq(strings.Join(k.g.parents[sha], " "), "main0", "one commit on the lane's fork point")
			k.eq(k.g.refs[submittedRefs+"T-05/1"], sha, "submitted pin")
			k.eq(strings.Join(k.g.status[k.lane], " "), "", "lane committed")
		},
	},
	{
		name:  "gate lane",
		edits: []func(map[string]any){cmd("compile", "echo 1 pass")},
		setup: func(k *fk) cutIn { return cutIn{sha: k.submit()} },
		call:  fakeGate(port.InputLane),
		end: func(k *fk, in cutIn, out any) {
			c := out.(port.GateOutcome).Candidate
			k.eq(k.pins("T-05"), "submitted="+in.sha+" candidate="+c.SHA+" resolved=-", "pins")
			k.eq(strings.Join(k.g.parents[c.SHA], " "), c.Target+" "+in.sha, "the candidate merges the submitted sha onto the target")
			k.eq(c.Target, k.main(), "the target untouched")
			k.eq(k.g.headOf(k.serve()), c.SHA, "serve's worktree at the candidate")
			k.eq(k.g.merging[k.serve()], "", "no merge left open")
		},
	},
	{
		name:  "gate resolved",
		edits: []func(map[string]any){cmd("compile", "echo 1 pass")},
		setup: func(k *fk) cutIn { _, res := k.resolved(); return cutIn{sha: res} },
		call:  fakeGate(port.InputResolved),
		end: func(k *fk, in cutIn, out any) {
			c := out.(port.GateOutcome).Candidate
			k.eq(strings.Join(k.g.parents[c.SHA], " "), strings.Join(k.g.parents[in.sha], " "), "the resolution's parents (message amended)")
			k.eq(c.Target, k.g.parents[in.sha][0], "checked against its first parent")
			k.eq(k.g.refs[candidateRefs+"T-05"]+" "+k.g.refs[resolvedRefs+"T-05"], c.SHA+" "+in.sha, "pins")
			k.eq(k.g.headOf(k.serve()), c.SHA, "serve's worktree at the amended resolution")
		},
	},
	{
		name:  "gate moved",
		edits: []func(map[string]any){cmd("compile", "echo 1 pass")},
		setup: func(k *fk) cutIn {
			sha := k.submit()
			k.g.answers["Diff main0 "+sha] = "+feature\n"
			c := k.green(lane(sha))
			k.move("unrelated")
			return cutIn{sha: sha, c: c}
		},
		call: fakeGate(port.InputMoved),
		end: func(k *fk, in cutIn, out any) {
			c := out.(port.GateOutcome).Candidate
			k.eq(c.Target, k.main(), "checked on the moved target")
			k.eq(k.g.parents[c.SHA][1], in.sha, "merges the submitted sha, not the old candidate (T-403)")
			k.eq(c.Patch, in.c.Patch, "an unrelated move keeps the patch")
			k.eq(k.g.refs[candidateRefs+"T-05"], c.SHA, "the new candidate pinned")
		},
	},
	{
		name:  "publish",
		setup: func(k *fk) cutIn { return cutIn{c: k.green(lane(k.submit()))} },
		call: func(k *fk, in cutIn) (string, any, error) {
			o, err := k.s.Publish(ctx, in.c)
			kind := o.Kind
			if kind == port.PublishAlready {
				kind = port.PublishMerged // a repeat after the fast-forward
			}
			return fmt.Sprintf("%s head=%q files=%v", kind, o.Head, o.Files), o, err
		},
		end: func(k *fk, in cutIn, _ any) {
			k.eq(k.main(), in.c.SHA, "the target fast-forwarded to the candidate")
			k.eq(k.pins("T-05"), "submitted=- candidate=- resolved=-", "pins deleted")
		},
	},
	{
		name: "after_merge",
		edits: []func(map[string]any){cmd("after_merge",
			map[string]any{"cmd": `echo ran >> "$TMP_DIR/after.ran"`, "when": []any{"*.txt"}})},
		setup: func(k *fk) cutIn {
			c := k.published()
			k.g.answers["Changed "+c.Target+" "+c.SHA] = "feature.txt"
			return cutIn{c: c}
		},
		call: func(k *fk, in cutIn) (string, any, error) {
			line, err := k.s.AfterMerge(ctx, in.c)
			return line, line, err
		},
		end: func(k *fk, in cutIn, _ any) {
			k.eq(k.Read(filepath.Join(k.Base, "after.ran")), "ran\n", "after_merge ran once")
			k.gone(k.Path(".obor/state/merges.log"), "no merges.log (the store's fact merged, T-525)")
		},
	},
	{
		name: "resolved verdict",
		setup: func(k *fk) cutIn {
			input := k.conflicted()
			return cutIn{sha: k.resolveServe("Merge T-05: resolved"), c: port.Candidate{SHA: input}}
		},
		call: func(k *fk, in cutIn) (string, any, error) {
			sha, err := k.s.Resolved(ctx, "T-05", in.c.SHA, nil)
			return sha, sha, err
		},
		end: func(k *fk, in cutIn, out any) {
			k.eq(out.(string), in.sha, "the resolution's sha")
			k.eq(k.g.refs[resolvedRefs+"T-05"], in.sha, "resolved pin")
		},
	},
	{
		name:  "unpin",
		setup: func(k *fk) cutIn { return cutIn{c: k.green(lane(k.submit()))} },
		call: func(k *fk, in cutIn) (string, any, error) {
			return "unpinned", nil, k.s.Unpin(ctx, "T-05")
		},
		end: func(k *fk, _ cutIn, _ any) {
			k.eq(k.pins("T-05"), "submitted=- candidate=- resolved=-", "pins deleted")
		},
	},
}

// published is a green lane candidate of the fixture's submit, published.
func (k *fk) published() port.Candidate {
	k.t.Helper()
	c := k.green(lane(k.submit()))
	if o, err := k.s.Publish(ctx, c); err != nil || o.Kind != port.PublishMerged {
		k.t.Fatalf("precondition: publish: %+v %v", o, err)
	}
	return c
}

// The cut table: for each step, its N-th git call fails, one N per write and the step's first read; that run is an
// error (not decided) that writes nothing after the failed call, and the step run again gives the uncut run's outcome and end
// state. Before T-1011 a git shim cut real git's calls (47 s); the decisions are the same over the fake's answers.
// The lane test's step (Scratch.Test) is not here: its runner lists the changed files with git of its own
// (runner.go), so its cut cases are TestLaneTest*'s, on real git.
func TestScratchCutTable(t *testing.T) {
	t.Parallel()
	for _, st := range fakeSteps {
		t.Run(st.name, func(t *testing.T) {
			t.Parallel()
			ref := newFk(t, st.edits...)
			in := st.setup(ref)
			ref.g.calls = nil
			want, out, err := st.call(ref, in)
			if err != nil {
				t.Fatalf("uncut: %v\noutput:\n%s", err, ref.out.String())
			}
			st.end(ref, in, out)
			calls := ref.g.calls
			if len(calls) == 0 {
				t.Fatal("precondition: the step calls git")
			}
			for _, n := range fakeCutPoints(calls) {
				k := newFk(t, st.edits...)
				in := st.setup(k)
				k.g.calls, k.g.cut = nil, n
				got, _, err := st.call(k, in)
				if !errors.Is(err, errCut) {
					t.Fatalf("cut %d (%s): outcome %q, error %v; want the cut's error", n, calls[n-1], got, err)
				}
				after := &fakeGit{calls: k.g.calls[n:]}
				if w := after.writeCalls(); len(w) > 0 {
					t.Errorf("cut %d (%s): the step wrote after the failed call: %q", n, calls[n-1], w)
				}
				k.g.calls, k.g.cut = nil, 0
				got, out, err := st.call(k, in)
				if err != nil {
					t.Fatalf("cut %d (%s), run again: %v\noutput:\n%s", n, calls[n-1], err, k.out.String())
				}
				if k.norm(got) != ref.norm(want) {
					t.Errorf("cut %d (%s), run again: %s\nwant %s", n, calls[n-1], k.norm(got), ref.norm(want))
				}
				st.end(k, in, out)
			}
		})
	}
}

// fakeCutPoints is the 1-based number of every write in calls and of the first read, in call order.
func fakeCutPoints(calls []string) []int {
	read := 0
	var ns []int
	for i, c := range calls {
		m, _, _ := strings.Cut(c, " ")
		switch {
		case slices.Contains(writes, m):
			ns = append(ns, i+1)
		case read == 0:
			read = i + 1
		}
	}
	if read > 0 {
		ns = append(ns, read)
		slices.Sort(ns)
	}
	return ns
}

// Submit: the lane's work committed once and its head pinned; a pinned submit is returned with only the pin read; a
// new attempt pins under its own ref; a pin from before submits had an id gives way; refused: no lane, a detached
// lane, a lane whose head the target holds.
func TestSubmitDecisions(t *testing.T) {
	t.Parallel()
	t.Run("commits the lane's work once, pins its head", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		k.eq(strings.Join(k.g.writeCalls(), "|"), "AddAll "+k.lane+"|Commit "+k.lane+" "+laneMsg+"|UpdateRef "+
			submittedRefs+"T-05/1 "+sha, "the writes")
		k.g.calls = nil
		k.eq(k.submit(), sha, "a repeat: the pinned sha")
		k.eq(strings.Join(k.g.calls, "|"), "Verify "+k.Root+" "+submittedRefs+"T-05/1", "a repeat reads the pin, nothing else")
	})
	t.Run("a worker commit after the submission stays out; a new attempt pins it", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		first := k.submit()
		late := k.laneCommit("late")
		k.eq(k.submit(), first, "the replay returns the pinned sha")
		k.eq(k.g.refs[submittedRefs+"T-05/1"], first, "the pin unchanged")
		next, err := k.s.Submit(ctx, "T-05", "2", "L-01", "feature", "pj-l1")
		if err != nil {
			t.Fatal(err)
		}
		k.eq(next, late, "a new attempt pins the lane's HEAD")
		k.eq(k.g.refs[submittedRefs+"T-05/2"], late, "under its own ref")
		k.eq(k.g.refs[submittedRefs+"T-05/1"], first, "the first pin kept")
		if err := k.s.Unpin(ctx, "T-05"); err != nil {
			t.Fatal(err)
		}
		k.eq(k.pins("T-05")+" "+k.g.refs[submittedRefs+"T-05/2"], "submitted=- candidate=- resolved=- ", "Unpin: every submit's pin")
	})
	t.Run("a clean lane is pinned as it is", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		head := k.laneCommit("worker's own commit")
		k.eq(k.submit(), head, "the lane's head")
		k.eq(strings.Join(k.g.writeCalls(), "|"), "UpdateRef "+submittedRefs+"T-05/1 "+head, "no commit")
	})
	t.Run("a pin from before submits had an id gives way", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		k.g.refs[submittedRefs+"T-05"] = "main0"
		sha := k.submit()
		refs, _ := k.g.Refs(submittedRefs)
		k.eq(strings.Join(refs, " "), submittedRefs+"T-05/1", "the flat pin gone")
		k.eq(k.g.refs[submittedRefs+"T-05/1"], sha, "the pin")
		k.g.refs[submittedRefs+"T-05"] = "main0"
		if err := k.s.Unpin(ctx, "T-05"); err != nil {
			t.Fatal(err)
		}
		k.eq(k.g.refs[submittedRefs+"T-05"], "", "Unpin drops a flat pin too")
	})
	for _, c := range []struct {
		name, lane, want string
		state            func(k *fk)
	}{
		{"no lane: refused", "L-09", "FAIL no lane L-09", func(*fk) {}},
		{"a detached lane: refused", "L-01", "FAIL $BASE/proj-wt/L-01 is not on a branch", func(k *fk) { k.g.branch[k.lane] = "" }},
		{"a lane the target holds: refused", "L-02", "FAIL task/T-20 is already in the target", func(k *fk) {
			k.g.branch[k.lane2()], k.g.refs["refs/heads/task/T-20"] = "task/T-20", "main0"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			k := newFk(t)
			c.state(k)
			_, err := k.s.Submit(ctx, "T-05", "1", c.lane, "x", "w")
			var ref port.Refused
			if !errors.As(err, &ref) {
				t.Fatalf("submit: %v; want Refused", err)
			}
			k.eq(k.norm(ref.Line), c.want, "the refusal")
			k.eq(strings.Join(k.g.writeCalls(), "|"), "", "nothing written")
		})
	}
}

// patch is a one-file diff as the adapter returns git's (its last newline trimmed): path's hunk at line n (its
// header's line numbers), index line idx.
func patch(path, idx string, n int, body string) string {
	return fmt.Sprintf("diff --git a/%[1]s b/%[1]s\nindex %[2]s 100644\n--- a/%[1]s\n+++ b/%[1]s\n@@ -%[3]d,1 +%[3]d,1 @@ ctx\n%[4]s",
		path, idx, n, body)
}

// Gate: the decisions over the merge's answers (green, conflict, a replayed resolution clean, differing or unchecked,
// a merge error), the message (lane, moved, resolved, spec), the candidate (pins, own diff), the worktree reset first.
func TestGateDecisions(t *testing.T) {
	t.Parallel()
	t.Run("lane green: the submitted sha merged onto the target at the newer parent's date, pinned", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		k.g.dates[sha] = 300
		k.g.answers["Diff main0 "+sha], k.g.answers["Stat main0 "+sha] = "+feature\n", " feature.txt | 1 +"
		k.g.calls = nil
		o := k.gate(lane(sha))
		c := o.Candidate
		msg := "Merge T-05: feature\n\n" + trailer
		k.eq(strings.Join(k.g.writeCalls(), "|"), "AddWorktree "+k.serve()+" main0|Reset "+k.serve()+" main0|Clean "+k.serve()+
			"|Merge "+k.serve()+" "+sha+"|Commit "+k.serve()+" "+msg+" @300 +0000|UpdateRef "+candidateRefs+"T-05 "+c.SHA, "the writes")
		k.eq(o.Line, "GREEN T-05 (merged, no commands.compile)", "the line")
		k.eq(fmt.Sprintf("%s %s %s %s", c.Task, c.Lane, c.Target, c.Base), "T-05 L-01 main0 main0", "the candidate")
		k.eq(strings.Join(k.g.parents[c.SHA], " "), "main0 "+sha, "the merge's parents")
		k.eq(c.Patch, fmt.Sprintf("%x", sha256.Sum256([]byte("+feature\n"))), "the patch: the own diff's hash")
		k.eq(c.Diff+"|"+c.Stat, "+feature\n| feature.txt | 1 +", "the diff and stat kept")
		k.eq(k.green(lane(sha)).SHA, c.SHA, "a repeat makes the same commit")
	})
	t.Run("a diff over port.InlineDiff: not kept, its patch and stat are", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		big := strings.Repeat("+a line of the big file\n", port.InlineDiff/20)
		k.g.answers["Diff main0 "+sha], k.g.answers["Stat main0 "+sha] = big, " big.txt | 1000 +"
		c := k.green(lane(sha))
		k.eq(c.Diff, "", "the big diff not kept")
		k.eq(c.Patch, fmt.Sprintf("%x", sha256.Sum256([]byte(big))), "its patch")
		k.eq(c.Stat, " big.txt | 1000 +", "its stat")
	})
	t.Run("commands.compile: its counts in the line; red: nothing committed or pinned", func(t *testing.T) {
		t.Parallel()
		k := newFk(t, cmd("compile", "echo 1 pass"))
		o := k.gate(lane(k.submit()))
		k.eq(o.Candidate.Checks+"|"+o.Line, "compile 1 pass|GREEN T-05 (compile 1 pass)", "the checks and the line")
		r := newFk(t, cmd("compile", "echo 0 pass; exit 1"))
		sha := r.submit()
		r.g.calls = nil
		o = r.gate(lane(sha))
		if o.Kind != port.OutcomeRed || !strings.HasPrefix(o.Line, "RED compile") {
			t.Errorf("a failing compile: %s %q; want red", o.Kind, o.Line)
		}
		for _, w := range r.g.writeCalls() {
			r.lacks(w, "Commit", "a red gate commits nothing")
			r.lacks(w, "UpdateRef", "a red gate pins nothing")
		}
	})
	t.Run("lane conflict: the unmerged paths, the merge left open, no mismatch", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		k.g.conflicts[sha] = []string{"a/a.go", "shared.txt"}
		in := lane(sha)
		in.Resolved = "ignored" // only a replay is compared with it
		k.g.calls = nil
		o := k.gate(in)
		k.eq(fmt.Sprintf("%s %v %s", o.Kind, o.Files, k.norm(o.Dir)), "conflict [a/a.go shared.txt] $BASE/proj-wt/merge", "the outcome")
		k.eq(k.norm(o.Line), "CONFLICT in $BASE/proj-wt/merge: a/a.go shared.txt", "the line")
		if o.Mismatch != nil {
			t.Errorf("unmerged paths are no mismatch: %+v", o.Mismatch)
		}
		k.eq(k.g.merging[k.serve()], sha, "the merge open in serve's worktree")
		for _, w := range k.g.writeCalls() {
			k.lacks(w, "Commit", "a conflict commits nothing")
			k.lacks(w, "UpdateRef", "a conflict pins nothing")
		}
	})
	t.Run("a merge that stopped with no MERGE_HEAD: its error", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		k.g.failing["Merge"] = errors.New("git merge: fatal: refusing")
		if o, err := k.s.Gate(ctx, lane(sha)); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("gate: %s %q %v; want the merge's error", o.Kind, o.Line, err)
		}
	})

	// replay is a moved gate whose merge stopped with no unmerged path (rerere replayed every hunk): staged is its
	// index's patch, checked the attempt's checked resolution's ("": none).
	replay := func(t *testing.T, staged, checked string, compile bool) (*fk, port.GateOutcome, string) {
		var edits []func(map[string]any)
		if compile {
			edits = append(edits, cmd("compile", `echo run >> "$TMP_DIR/compile-runs"; echo 1 pass`))
		}
		k := newFk(t, edits...)
		sha := k.submit()
		k.move("main moved")
		k.g.conflicts[sha] = []string{}
		k.g.answers["Staged "+k.serve()+" "+k.main()] = staged
		in := moved(sha)
		if checked != "" {
			in.Resolved = "res1"
			k.g.parents["res1"] = []string{"main0", sha}
			k.g.answers["Patch "+k.serve()+" main0 res1"] = checked
		}
		return k, k.gate(in), sha
	}
	a := patch("a/a.go", "1111..2222", 3, "+func A() int { return 40 }")
	b := patch("b/b.go", "3333..4444", 3, "+func B() int { return 40 }")
	t.Run("a replay equal to the checked resolution: compiled, green, reused", func(t *testing.T) {
		t.Parallel()
		// The checked one's index line and hunk line numbers differ (the move shifted the file): the same patch.
		k, o, sha := replay(t, a, patch("a/a.go", "5555..6666", 9, "+func A() int { return 40 }"), true)
		if o.Kind != port.OutcomeGreen {
			t.Fatalf("gate: %s %q; want green", o.Kind, o.Line)
		}
		k.eq(k.Read(filepath.Join(k.Base, "compile-runs")), "run\n", "compiled")
		k.eq(k.g.msgs[o.Candidate.SHA], "Merge T-05: feature\n\nMerged onto the moved main at "+o.Candidate.Target+
			"\n\n(conflict resolution reused)\n\n"+trailer, "the message says the resolution was reused")
		k.eq(k.g.parents[o.Candidate.SHA][1], sha, "the merge of the submitted sha")
	})
	t.Run("a replay that differs: a conflict naming the files, the mismatch, nothing compiled", func(t *testing.T) {
		t.Parallel()
		k, o, sha := replay(t, a, a+"\n"+b, true)
		k.eq(fmt.Sprintf("%s %v", o.Kind, o.Files), "conflict [b/b.go]", "the outcome")
		k.eq(k.norm(o.Line), "CONFLICT in $BASE/proj-wt/merge: b/b.go (the replayed resolution differs from the checked one)", "the line")
		if o.Mismatch == nil {
			t.Fatal("no mismatch")
		}
		k.eq(fmt.Sprintf("%s %s %v %s", o.Mismatch.Checked, o.Mismatch.Tree, o.Mismatch.Files, k.norm(o.Mismatch.Dir)),
			"res1 tree-"+sha+" [b/b.go] $BASE/proj-wt/merge", "the mismatch names the checked resolution, the replay's tree, the files")
		k.gone(filepath.Join(k.Base, "compile-runs"), "nothing compiled")
		k.eq(k.g.merging[k.serve()], sha, "the merge stays open")
	})
	t.Run("a replay with no checked resolution: a conflict over its files, no mismatch", func(t *testing.T) {
		t.Parallel()
		k, o, _ := replay(t, a+"\n"+b, "", false)
		k.eq(fmt.Sprintf("%s %v", o.Kind, o.Files), "conflict [a/a.go b/b.go]", "the outcome")
		if o.Mismatch != nil {
			t.Errorf("no checked resolution, no mismatch: %+v", o.Mismatch)
		}
		for _, w := range k.g.writeCalls() {
			k.lacks(w, "WriteTree", "no tree written")
		}
	})
	t.Run("moved: the submitted sha onto the moved target, the subject rule kept, the move in the body", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		k.g.answers["Diff main0 "+sha] = "+feature\n"
		old := k.green(lane(sha))
		target := k.move("unrelated")
		c := k.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputMoved, Input: sha, Title: "Expected spec title",
			Summary: "worker words"})
		k.eq(c.Target, target, "the moved target")
		k.eq(strings.Join(k.g.parents[c.SHA], " "), target+" "+sha, "the submitted sha merged, not the old candidate")
		k.eq(k.g.msgs[c.SHA], "Merge T-05: Expected spec title\n\nMerged onto the moved main at "+target+
			"\n\nSummary: worker words\n\n"+trailer, "the message")
		k.eq(c.Patch+" "+c.Base, old.Patch+" main0", "the patch and base kept")
	})
	t.Run("the stored spec in a lane merge's message, none without one", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		in := lane(k.submit())
		in.Spec, in.SpecRev, in.Title = "# T-05 — Stored\n\nfeature.txt\n", 3, "Stored"
		k.eq(k.g.msgs[k.green(in).SHA], "Merge T-05: Stored\n\nSpec rev 3:\n\n# T-05 — Stored\n\nfeature.txt\n\nSummary: feature\n\n"+
			trailer, "with a stored spec (the trailer last)")
		in.Spec = ""
		k.eq(k.g.msgs[k.green(in).SHA], "Merge T-05: Stored\n\nSummary: feature\n\n"+trailer, "without")
	})
	t.Run("resolved: reset to the resolution, amended with the gate's message, the same commit on a repeat", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		input := k.conflicted()
		k.resolveServe("Merge T-05: resolved\n\nkept both sides")
		res, err := k.s.Resolved(ctx, "T-05", input, nil)
		if err != nil {
			t.Fatal(err)
		}
		k.g.head[k.serve()] = "elsewhere" // another task's gate since (a red resolved gate, then T-20's)
		in := port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res, Spec: "# T-05 — Stored\n\nx\n",
			SpecRev: 2, Title: "Stored", Summary: "feature"}
		k.g.calls = nil
		c := k.green(in)
		k.eq(strings.Join(k.g.writeCalls()[:2], "|"), "Reset "+k.serve()+" "+res+"|Clean "+k.serve(), "reset to the pin first")
		k.eq(c.Target+" "+strings.Join(k.g.parents[c.SHA], " "), k.g.parents[res][0]+" "+strings.Join(k.g.parents[res], " "),
			"its target the resolution's first parent, the resolution's parents")
		k.eq(c.Base, "main0", "its base: the submitted sha's fork point")
		k.eq(k.g.msgs[c.SHA], "Merge T-05: Stored\n\nSpec rev 2:\n\n# T-05 — Stored\n\nx\n\nkept both sides\n\nSummary: feature",
			"the amended message")
		k.eq(fmt.Sprint(k.g.dates[c.SHA]), fmt.Sprint(k.g.dates[res]), "the resolution's date")
		k.eq(k.green(in).SHA, c.SHA, "a repeat makes the same commit")
		in.Spec, in.Title = "", ""
		k.eq(k.g.msgs[k.green(in).SHA], "Merge T-05: feature\n\nkept both sides", "no stored spec or title: the summary is the subject")
	})
	t.Run("resolved with git's default message: the gate's subject, no comment lines", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		input := k.conflicted()
		k.resolveServe("Merge commit '" + input + "' into HEAD\n\n# Conflicts:\n#\tshared.txt\n")
		res, err := k.s.Resolved(ctx, "T-05", input, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := k.green(port.GateInput{Task: "T-05", Lane: "L-01", Kind: port.InputResolved, Input: res,
			Spec: "# T-05 — Stored\n\nx\n", SpecRev: 1, Title: "Stored", Summary: "feature"})
		k.eq(k.g.msgs[c.SHA], "Merge T-05: Stored\n\nSpec rev 1:\n\n# T-05 — Stored\n\nx\n\nSummary: feature", "the amended message")
	})
	t.Run("serve's worktree from any state: reset and cleaned first, the same candidate", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		sha := k.submit()
		want := k.green(lane(sha))
		k.g.merging[k.serve()], k.g.status[k.serve()], k.g.head[k.serve()] = "other", []string{" M shared.txt"}, "other"
		k.g.calls = nil
		k.eq(fmt.Sprintf("%+v", k.green(lane(sha))), fmt.Sprintf("%+v", want), "the same candidate")
		k.eq(strings.Join(k.g.writeCalls()[:2], "|"), "Reset "+k.serve()+" main0|Clean "+k.serve(), "reset and cleaned first")
	})
	t.Run("the target is branches.main, whatever the main checkout has checked out", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		k.g.branch[k.Root], k.g.refs["refs/heads/side"] = "side", "side1"
		k.eq(k.green(lane(k.submit())).Target, "main0", "the gate's target")
	})
	t.Run("an unknown input kind: an error", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		in := lane(k.submit())
		in.Kind = "sideways"
		if _, err := k.s.Gate(ctx, in); err == nil || !strings.Contains(err.Error(), `unknown input kind "sideways"`) {
			t.Errorf("gate: %v; want the unknown kind's error", err)
		}
	})
}

// Resolved: serve's HEAD is pinned only when it is the task's committed merge of its input onto the target; with a
// replay mismatch, only when the checked resolution's lost edits are back or the file was changed otherwise (T-917).
func TestResolvedDecisions(t *testing.T) {
	t.Parallel()
	t.Run("its own committed merge: pinned", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		input := k.conflicted()
		res := k.resolveServe("Merge T-05: resolved")
		k.g.calls = nil
		got, err := k.s.Resolved(ctx, "T-05", input, nil)
		k.eq(fmt.Sprintf("%s %v", got, err), res+" <nil>", "the resolution")
		k.eq(strings.Join(k.g.writeCalls(), "|"), "UpdateRef "+resolvedRefs+"T-05 "+res, "pinned")
	})
	for _, c := range []struct {
		name, want string
		state      func(k *fk, input string)
	}{
		{"no serve worktree", "FAIL no serve worktree $BASE/proj-wt/merge", nil},
		{"a merge still open", "FAIL merge in progress in $BASE/proj-wt/merge: commit the resolution first", func(*fk, string) {}},
		{"a HEAD the target holds", "FAIL no resolved merge in $BASE/proj-wt/merge (commit it there)", func(k *fk, _ string) {
			k.g.Reset(k.serve(), k.main())
		}},
		{"another task's merge", "FAIL HEAD of $BASE/proj-wt/merge is not T-05's merge of $INPUT (resolve that conflict there)",
			func(k *fk, _ string) {
				k.g.Reset(k.serve(), k.main())
				k.g.merging[k.serve()] = "t20"
				k.g.commitOn(k.serve(), "Merge T-20: other", 400)
			}},
		{"its input merged onto something else", "FAIL HEAD of $BASE/proj-wt/merge is not a merge onto $MAIN", func(k *fk, input string) {
			k.g.Reset(k.serve(), input)
			k.g.merging[k.serve()] = input
			k.g.parents["lane2"] = []string{"main0"}
			k.g.head[k.serve()] = "lane2"
			k.g.commitOn(k.serve(), "Merge T-05 onto T-20's lane", 400)
		}},
	} {
		t.Run(c.name+": refused", func(t *testing.T) {
			t.Parallel()
			k := newFk(t)
			input := "never"
			if c.state != nil {
				input = k.conflicted()
				c.state(k, input)
			}
			_, err := k.s.Resolved(ctx, "T-05", input, nil)
			var ref port.Refused
			if !errors.As(err, &ref) {
				t.Fatalf("resolved: %v; want Refused", err)
			}
			want := strings.NewReplacer("$INPUT", input, "$MAIN", k.main()).Replace(c.want)
			k.eq(k.norm(ref.Line), want, "the refusal")
			k.eq(k.g.refs[resolvedRefs+"T-05"], "", "nothing pinned")
		})
	}

	// mismatched is a committed resolution of a replay mismatch over b/b.go: head's patch of b/b.go is headB, its blob
	// headBlob; the replay's tree held replayBlob.
	checkedB := patch("b/b.go", "1..2", 3, "+func B() int { return 40 }")
	mismatched := func(t *testing.T, headB, headBlob string) (*fk, string, *port.Mismatch) {
		k := newFk(t)
		input := k.conflicted()
		res := k.resolveServe("as it stands")
		m := &port.Mismatch{Checked: "c0ffee1234", Tree: "tree0", Files: []string{"b/b.go"}, Dir: k.serve()}
		k.g.parents["c0ffee1234"] = []string{"main0", input}
		k.g.answers["Patch "+k.serve()+" main0 c0ffee1234"] = checkedB
		k.g.answers["Patch "+k.serve()+" "+k.g.parents[res][0]+" "+res] = headB
		k.g.answers["Verify "+res+":b/b.go"], k.g.answers["Verify tree0:b/b.go"] = headBlob, "blob-replay"
		return k, input, m
	}
	t.Run("a mismatch committed as it stands: refused naming the file and the checked resolution", func(t *testing.T) {
		t.Parallel()
		k, input, m := mismatched(t, "", "blob-replay")
		_, err := k.s.Resolved(ctx, "T-05", input, m)
		var ref port.Refused
		if !errors.As(err, &ref) {
			t.Fatalf("resolved: %v; want Refused", err)
		}
		k.eq(k.norm(ref.Line), "FAIL the merge in $BASE/proj-wt/merge still lacks edits of the checked resolution c0ffee1 in b/b.go "+
			"(unchanged from the replay): compare with git diff c0ffee1 -- b/b.go, restore them and commit", "the refusal")
		if _, err := k.s.Resolved(ctx, "T-05", input, nil); err != nil {
			t.Errorf("without the mismatch the same commit is pinned: %v", err)
		}
	})
	t.Run("a mismatch with the lost edit restored: pinned", func(t *testing.T) {
		t.Parallel()
		k, input, m := mismatched(t, patch("b/b.go", "7..8", 5, "+func B() int { return 40 }"), "blob-restored")
		if _, err := k.s.Resolved(ctx, "T-05", input, m); err != nil {
			t.Errorf("resolved: %v", err)
		}
	})
	t.Run("a mismatch file the integrator changed otherwise: pinned", func(t *testing.T) {
		t.Parallel()
		k, input, m := mismatched(t, patch("b/b.go", "7..9", 3, "+func B() int { return 41 }"), "blob-own")
		if _, err := k.s.Resolved(ctx, "T-05", input, m); err != nil {
			t.Errorf("resolved: %v", err)
		}
	})
}

// Publish: merged (fast-forward, unpin), already (a repeat), moved (the target moved: nothing written, the candidate
// pinned), hold (a dirty merged file: nothing written); off branches.main an error before any write; a refused
// fast-forward is its error, unless the target moved meanwhile (moved then).
func TestPublishDecisions(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*fk, port.Candidate) {
		k := newFk(t)
		c := k.green(lane(k.submit()))
		k.g.answers["Changed main0 "+c.SHA] = "feature.txt"
		k.g.calls = nil
		return k, c
	}
	publish := func(k *fk, c port.Candidate) string {
		o, err := k.s.Publish(ctx, c)
		return fmt.Sprintf("%s %s %v %v", o.Kind, o.Head, o.Files, err)
	}
	t.Run("merged, then already", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		k.g.status[k.Root] = []string{"?? other.txt"} // not a merged file
		k.eq(publish(k, c), "merged  [] <nil>", "merged")
		k.eq(k.main(), c.SHA, "fast-forwarded")
		k.eq(k.pins("T-05"), "submitted=- candidate=- resolved=-", "unpinned")
		k.g.refs[candidateRefs+"T-05"] = c.SHA
		k.g.calls = nil
		k.eq(publish(k, c), "already  [] <nil>", "a repeat")
		k.eq(k.pins("T-05"), "submitted=- candidate=- resolved=-", "unpinned again")
		for _, w := range k.g.writeCalls() {
			k.lacks(w, "FF", "already: no fast-forward")
		}
	})
	t.Run("moved: the head, nothing written", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		head := k.move("unrelated")
		k.eq(publish(k, c), "moved "+head+" [] <nil>", "moved")
		k.eq(strings.Join(k.g.writeCalls(), "|"), "", "nothing written")
		k.eq(k.g.refs[candidateRefs+"T-05"], c.SHA, "the candidate stays pinned")
	})
	t.Run("hold: a dirty merged file, nothing written", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		k.g.status[k.Root] = []string{" M feature.txt", "?? other.txt"}
		k.eq(publish(k, c), "hold  [feature.txt] <nil>", "hold")
		k.eq(strings.Join(k.g.writeCalls(), "|"), "", "nothing written")
	})
	t.Run("the main checkout off branches.main: an error, nothing written", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		k.g.branch[k.Root], k.g.refs["refs/heads/side"] = "side", "main0"
		k.eq(k.norm(publish(k, c)), "  [] publish T-05: the main checkout $BASE/proj is not on main", "the error")
		k.eq(strings.Join(k.g.writeCalls(), "|"), "", "nothing written")
		k.eq(k.main(), "main0", "main unmoved")
	})
	t.Run("a refused fast-forward: its error", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		k.g.failing["FF"] = errors.New("git merge: index.lock")
		k.eq(publish(k, c), "  [] git merge: index.lock", "the error")
	})
	t.Run("a fast-forward refused because the target moved meanwhile: moved", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t)
		head := ""
		k.g.before["FF"] = func() { head = k.move("raced") }
		k.eq(publish(k, c), "moved "+head+" [] <nil>", "moved")
		k.eq(k.g.refs[candidateRefs+"T-05"], c.SHA, "the candidate stays pinned")
	})
}

// after_merge runs commands.after_merge in the main checkout (the directory publish fast-forwards), never in serve's
// worktree; its when globs decide; its full output goes to .obor/state/logs/<task>-after_merge.log, a green run too
// (T-414: on 2026-10-25 a stale dist/obor after a merge had no trace). It keeps no record of its own (T-525: its intent
// in the store is the once), so a second call runs it again. The main checkout must be on a branch at the published
// merge, else a WARN line, again on a second call.
func TestAfterMergeDecisions(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, v any) (*fk, port.Candidate) {
		var edits []func(map[string]any)
		if v != nil {
			edits = append(edits, cmd("after_merge", v))
		}
		k := newFk(t, edits...)
		c := k.published()
		k.g.answers["Changed main0 "+c.SHA] = "feature.txt"
		k.g.calls = nil
		return k, c
	}
	log := func(c port.Candidate) string { return filepath.Join(".obor/state/logs", c.Task+"-after_merge.log") }
	t.Run("runs in the main checkout, its output in the log, again on a second call", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t, map[string]any{"cmd": `echo after-merge-ran; echo ran >> after.ran; touch after.marker`})
		for i := 1; i <= 2; i++ {
			line, err := k.s.AfterMerge(ctx, c)
			k.eq(fmt.Sprintf("%s %v", line, err), "ran <nil>", fmt.Sprintf("call %d", i))
		}
		k.eq(k.Read("after.ran"), "ran\nran\n", "both calls ran it")
		k.gone(filepath.Join(k.serve(), "after.marker"), "marker in serve's worktree")
		k.eq(k.Read(log(c)), "after-merge-ran\n", "the run's output in the log")
		k.gone(k.Path(".obor/state/merges.log"), "no merges.log")
	})
	t.Run("no commands.after_merge: skipped, no git", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t, nil)
		line, err := k.s.AfterMerge(ctx, c)
		k.eq(fmt.Sprintf("%s %v %d", line, err, len(k.g.calls)), "skipped <nil> 0", "skipped")
	})
	t.Run("a when glob that does not match skips it; one that does runs it", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t, map[string]any{"cmd": "touch after.marker", "when": []any{"cmd/**"}})
		line, err := k.s.AfterMerge(ctx, c)
		k.eq(fmt.Sprintf("%s %v", line, err), "skipped <nil>", "after_merge skipped")
		k.gone(k.Path("after.marker"), "not run")
		k.gone(k.Path(log(c)), "no log for a run that did not happen")
		k.g.answers["Changed main0 "+c.SHA] = "feature.txt\ncmd/obor/main.go"
		line, err = k.s.AfterMerge(ctx, c)
		k.eq(fmt.Sprintf("%s %v", line, err), "ran <nil>", "a matching path")
	})
	t.Run("a failed run's WARN line names its log", func(t *testing.T) {
		t.Parallel()
		k, c := setup(t, map[string]any{"cmd": "echo broken; exit 2"})
		line, err := k.s.AfterMerge(ctx, c)
		k.eq(fmt.Sprintf("%s %v", line, err), "WARN after_merge failed: broken; log "+k.Path(log(c))+" <nil>", "the WARN line")
		k.eq(k.Read(log(c)), "broken\n", "the failed run's output is in the log")
	})
	for _, m := range []struct {
		name  string
		moved func(k *fk, c port.Candidate)
	}{
		{"main moved past the merge", func(k *fk, _ port.Candidate) { k.move("later") }},
		{"main detached at the merge", func(k *fk, c port.Candidate) { k.g.Detach(k.Root, c.SHA) }},
	} {
		t.Run(m.name+": WARN, not run, again on a second call", func(t *testing.T) {
			t.Parallel()
			k, c := setup(t, map[string]any{"cmd": "touch after.marker"})
			m.moved(k, c)
			want := "WARN after_merge not run: the main checkout is not at the published merge " + c.SHA + " <nil>"
			for i := 1; i <= 2; i++ {
				line, err := k.s.AfterMerge(ctx, c)
				k.eq(fmt.Sprintf("%s %v", line, err), want, fmt.Sprintf("call %d", i))
			}
			k.gone(k.Path("after.marker"), "not run")
		})
	}
}

// Landed reads branches.main's log (HEAD in MergeEvidenceBy's call), whatever the main checkout has checked out; a
// merge subject is evidence, an old "<id>: spec" commit touching only the legacy spec dir is not (T-550, RV-57 #1); a
// log git cannot read is Landed's error (RV-38 #11).
func TestLandedDecisions(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		edit func(map[string]any)
		log  string
		want string
	}{
		{"nothing", nil, "", "false <nil>"},
		{"a merge subject", nil, "\x1eMerge T-05: feature\n\nfeature.txt\n", "true <nil>"},
		{"a worker commit outside the spec dir", nil, "\x1eT-05: feature\n\nfeature.txt\n", "true <nil>"},
		{"an old spec commit", nil, "\x1eT-05: spec\n\ndocs/tasks/T-05.md\n", "false <nil>"},
		{"an old spec commit under paths.tasks", testkit.Set("paths.tasks", "specs"), "\x1eT-05: spec\n\nspecs/T-05.md\n", "false <nil>"},
		{"an empty T-05 commit", nil, "\x1eT-05: empty\n", "true <nil>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var edits []func(map[string]any)
			if c.edit != nil {
				edits = append(edits, c.edit)
			}
			k := newFk(t, edits...)
			k.g.branch[k.Root], k.g.refs["refs/heads/side"] = "side", "main0"
			k.g.answers["Log refs/heads/main"] = c.log
			k.g.answers["Log HEAD"] = "\x1eMerge T-05: only on side\n" // read HEAD instead and it would land
			landed, err := k.s.Landed(ctx, "T-05")
			k.eq(fmt.Sprintf("%v %v", landed, err), c.want, "landed")
		})
	}
	t.Run("git's error", func(t *testing.T) {
		t.Parallel()
		k := newFk(t)
		k.g.failing["Log"] = errors.New("git log: broken")
		if landed, err := k.s.Landed(ctx, "T-05"); err == nil {
			t.Errorf("Landed with git failing: %v, nil; want the error", landed)
		}
	})
}
