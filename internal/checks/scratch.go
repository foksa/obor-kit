package checks

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"obor/internal/app/port"
	"obor/internal/project"
	"obor/internal/spec"
)

// Scratch is obor serve's merge gate over git (port.Scratch). It works in
// serve's own worktree, <lanes.dir>/merge, which only obor serve writes: every gate starts by resetting it, so there
// is no merge window, no lock and no ownership proof. Each step is a function of its inputs (shas the
// app records, the pins under refs/obor/), never of the worktree's state. An outcome is final for its step; an error
// is "not decided": the app runs the step again. Every git call ends with the method's ctx (killed with its process
// group).
//
// each method reads git (gitReader), decides over the answers, then writes (gitWriter); realGit (gitio.go) is
// the one adapter that runs git, so an outcome is a function of git's answers and the decision tests run on a fake.
type Scratch struct {
	Options
	fake gitIO // the tests' git (fakegit_test.go); nil: real git
}

var _ port.Scratch = Scratch{}

// submittedRefs pins a submitted lane head, the gate's input: refs/obor/submitted/<task>/<submit id>, one per submit
// step. candidateRefs a green gate's merge, one per task; resolvedRefs the merge the integrator resolved in serve's
// worktree, the resolved gate's input: one per task.
const (
	submittedRefs = "refs/obor/submitted/"
	candidateRefs = "refs/obor/candidate/"
	resolvedRefs  = "refs/obor/resolved/"
)

// rerere is git's options for the scratch's merge: recording and replay of conflict resolutions, for that call
// only (-c, never the repo's config). The preimage lands in the repository's common rr-cache, which serve's worktree
// shares with the main checkout; the integrator's resolve commit records the resolution there (git turns rerere on by
// itself once that directory exists), and a later merge of the same sha onto a moved target replays it, autoupdate
// staging the resolved paths.
var rerere = []string{"-c", "rerere.enabled=true", "-c", "rerere.autoupdate=true"}

// Submit commits the lane's uncommitted work ("<task>: <summary>") and pins the lane's HEAD at
// refs/obor/submitted/<task>/<id>. A pin there is this submit's result already: it is returned, nothing touched (a
// later worker commit stays out). A cut between the commit and the pin reads HEAD again (a residual race). The commit
// passes --no-verify: the project's checking step is commands.check, not its commit hooks (prepare-commit-msg
// still runs).
func (s Scratch) Submit(ctx context.Context, task, id, lane, summary string) (string, error) {
	s.gitCtx = ctx
	g := s.gio()
	pin := submittedRefs + task + "/" + id
	if sha, ok, err := g.Verify(s.Cfg.Root, pin); err != nil || ok {
		return sha, err
	}
	dir := s.laneDir(lane)
	if dir == "" {
		return "", port.Refused{Line: fmt.Sprintf("FAIL no lane %s", lane)}
	}
	branch, err := g.Branch(dir)
	if err != nil {
		return "", err
	}
	if branch == "" {
		return "", port.Refused{Line: fmt.Sprintf("FAIL %s is not on a branch", dir)}
	}
	st, err := g.Status(dir)
	if err != nil {
		return "", err
	}
	if len(st) > 0 {
		if err := g.AddAll(dir); err != nil {
			return "", err
		}
		if err := g.Commit(dir, submitMessage(task, summary), ""); err != nil {
			return "", err
		}
	}
	_, target, err := s.target(g)
	if err != nil {
		return "", err
	}
	head, err := g.Rev(dir, "HEAD")
	if err != nil {
		return "", err
	}
	if in, err := g.Ancestor(dir, head, target); err != nil {
		return "", err
	} else if in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL %s is already in the target", branch)}
	}
	if err := s.dropFlatPin(g, task); err != nil {
		return "", err
	}
	if err := g.UpdateRef(pin, head); err != nil {
		return "", err
	}
	return head, nil
}

// submitMessage is the lane commit's message: "<task>: <summary>" and nothing else (no agent or model names
// in commits).
func submitMessage(task, summary string) string {
	return task + ": " + summary
}

// Gate resets serve's worktree (created when missing) and merges in.Input there: a lane or moved input (the submitted
// sha, never the old candidate) is merged onto the target (merge --no-ff --no-commit; a conflict stays open), a
// resolved one is taken as committed. Then commands.compile runs on it (the slot compiles, it runs no test; red
// when it fails), after lane_setup when compile is configured. Green: the merge committed and pinned at
// refs/obor/candidate/<task>, its Patch, Stat and Diff the task's own diff Base..S (Base: merge-base(S, target); S the
// submitted sha), which a move keeps. The merge commit's date is its newer parent's, so a repeat makes the same commit.
//
// the merge runs with git's rerere, so a hunk the integrator resolved once is replayed by the next merge of the
// same sha onto a moved target instead of being asked for again. A merge that stopped with MERGE_HEAD but has no
// unmerged path left after the replay is clean when its patch is the checked resolution's: it compiles as any
// merge, and its message says the resolution was reused.
func (s Scratch) Gate(ctx context.Context, in port.GateInput) (port.GateOutcome, error) {
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	g := s.gio()
	branch, target, err := s.target(g)
	if err != nil {
		return port.GateOutcome{}, err
	}
	dir := s.dir()
	if err := s.worktree(dir, target); err != nil {
		return port.GateOutcome{}, err
	}
	base, err := gateBase(in, target)
	if err != nil {
		return port.GateOutcome{}, err
	}
	if err := g.Reset(dir, base); err != nil {
		return port.GateOutcome{}, err
	}
	if err := g.Clean(dir); err != nil {
		return port.GateOutcome{}, err
	}
	linkLaneLinks(s.Cfg, s.Cfg.Root, dir)

	c := port.Candidate{Task: in.Task, Lane: in.Lane, SHA: in.Input}
	submitted := in.Input // S: the lane's commit the merge takes in
	reused := false       // the merge got its conflicting hunks from a recorded resolution: clean, not a conflict
	if in.Kind == port.InputResolved {
		if c.Target, err = g.Rev(dir, in.Input+"^1"); err != nil {
			return port.GateOutcome{}, err
		}
		if submitted, err = g.Rev(dir, in.Input+"^2"); err != nil {
			return port.GateOutcome{}, err
		}
		if c.SHA, err = s.specResolved(g, dir, in); err != nil {
			return port.GateOutcome{}, err
		}
	} else {
		c.Target = target
		if mergeErr := g.Merge(dir, in.Input); mergeErr != nil {
			o, err := s.stopped(g, dir, target, in, mergeErr)
			if o != nil || err != nil {
				return deref(o), err
			}
			reused = true
		}
	}

	r := newRunner(ctx, &s.Options, dir, in.Task)
	r.onCommand = in.Command // obor status' running line names the command while it runs, none between two
	defer r.releaseSlot()
	if _, ok := s.command("compile"); ok {
		if o, err := gateHalt(&r, r.laneSetup()); o != nil || err != nil {
			return *o, err
		}
		if o, err := gateHalt(&r, r.compile()); o != nil || err != nil {
			return *o, err
		}
	}
	r.releaseSlot()

	if in.Kind != port.InputResolved {
		msg := mergeMessage(in, movedBody(in, branch, target, reused), "")
		if c.SHA, err = s.commit(g, dir, msg, target, in.Input); err != nil {
			return port.GateOutcome{}, err
		}
	}
	if err := g.UpdateRef(candidateRefs+in.Task, c.SHA); err != nil {
		return port.GateOutcome{}, err
	}
	if err := s.ownDiff(g, &c, submitted); err != nil {
		return port.GateOutcome{}, err
	}
	return greenGate(c, r.check), nil
}

// gateBase is what a gate resets serve's worktree to: the target for a lane or moved input, the resolved merge itself
// for a resolved one.
func gateBase(in port.GateInput, target string) (string, error) {
	switch in.Kind {
	case port.InputLane, port.InputMoved:
		return target, nil
	case port.InputResolved:
		return in.Input, nil
	}
	return "", fmt.Errorf("gate %s: unknown input kind %q", in.Task, in.Kind)
}

// movedBody is the body line of a gate's own merge: a moved target's "Merged onto the moved <branch> at <target>"
// (the subject keeps the task's rule) and, when the merge replayed a recorded resolution, "(conflict resolution
// reused)"; "" for a plain lane merge.
func movedBody(in port.GateInput, branch, target string, reused bool) string {
	moved := ""
	if in.Kind == port.InputMoved {
		moved = "Merged onto the moved " + branch + " at " + target
	}
	if reused {
		moved = strings.TrimSpace(moved + "\n\n(conflict resolution reused)")
	}
	return moved
}

// greenGate is a green gate's outcome for its candidate c and checks summary ("": no commands.compile).
func greenGate(c port.Candidate, checks string) port.GateOutcome {
	c.Checks = checks
	what := checks
	if what == "" {
		what = "merged, no commands.compile"
	}
	return port.GateOutcome{Kind: port.OutcomeGreen, Candidate: c, Line: fmt.Sprintf("GREEN %s (%s)", c.Task, what)}
}

func deref(o *port.GateOutcome) port.GateOutcome {
	if o == nil {
		return port.GateOutcome{}
	}
	return *o
}

// stopped decides a merge that stopped with mergeErr from what git answers after it: no MERGE_HEAD (or no answer) is
// the merge's error, not decided; unmerged paths a conflict. With none left, every hunk came from a recorded
// resolution: clean (nil, nil) only when the merge's patch is the attempt's checked resolution's, else a
// conflict naming the files, carrying the mismatch when the attempt has a checked resolution.
func (s Scratch) stopped(g gitIO, dir, target string, in port.GateInput, mergeErr error) (*port.GateOutcome, error) {
	if _, open, err := g.Verify(dir, "MERGE_HEAD"); err != nil || !open {
		return nil, mergeErr
	}
	files, err := g.Unmerged(dir)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		o := conflictOutcome(dir, files, "")
		return &o, nil
	}
	got, err := g.Staged(dir, target)
	if err != nil {
		return nil, err
	}
	want := ""
	if in.Resolved != "" {
		if want, err = g.Patch(dir, in.Resolved+"^1", in.Resolved); err != nil {
			return nil, err
		}
	}
	files = replayDiffers(got, want, in.Resolved != "")
	if len(files) == 0 {
		return nil, nil
	}
	o := conflictOutcome(dir, files, " (the replayed resolution differs from the checked one)")
	if in.Resolved != "" { // the resolve job names the checked resolution and the files
		tree, err := g.WriteTree(dir)
		if err != nil {
			return nil, err
		}
		o.Mismatch = &port.Mismatch{Checked: in.Resolved, Tree: tree, Files: files, Dir: dir}
	}
	return &o, nil
}

// conflictOutcome is a gate's conflict in dir over files, why appended to its line.
func conflictOutcome(dir string, files []string, why string) port.GateOutcome {
	return port.GateOutcome{Kind: port.OutcomeConflict, Files: files, Dir: dir,
		Line: fmt.Sprintf("CONFLICT in %s: %s%s", dir, strings.Join(files, " "), why)}
}

// ownDiff fills c's Base (merge-base(submitted, c.Target)) and the task's own diff Base..submitted: its Patch (an
// accept binds to it, row 20), Stat and Diff (at most InlineDiff bytes: the review job carries it). Base is
// fixed by the submitted sha's history, so a move onto a newer target keeps the same three.
func (s Scratch) ownDiff(g gitReader, c *port.Candidate, submitted string) error {
	base, err := g.MergeBase(submitted, c.Target)
	if err != nil {
		return err
	}
	c.Base = base
	diff, err := g.Diff(base, submitted)
	if err != nil {
		return err
	}
	c.Patch, c.Diff = patchOf(diff)
	c.Stat, err = g.Stat(base, submitted)
	return err
}

// patchOf is a candidate's Patch (the sha256 of its own diff) and the Diff it keeps: the whole diff when it is at most
// port.InlineDiff bytes, else "".
func patchOf(diff string) (patch, kept string) {
	patch = fmt.Sprintf("%x", sha256.Sum256([]byte(diff)))
	if len(diff) <= port.InlineDiff {
		kept = diff
	}
	return patch, kept
}

// replayDiffers is the files whose patch differs between the merge open in serve's worktree (staged: its index
// against the target) and the checked resolution (checked: resolved^1..resolved; have false: none, every file
// differs), each file's diff compared without its index line and hunk line numbers, as git patch-id does.
// Conservative: a moved context line differs too.
func replayDiffers(staged, checked string, have bool) []string {
	got, want := filePatches(staged), map[string]string{}
	if have {
		want = filePatches(checked)
	}
	var files []string
	for f, p := range got {
		if want[f] != p {
			files = append(files, f)
		}
	}
	for f := range want {
		if _, ok := got[f]; !ok {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files
}

// filePatches is a diff split per file ("diff --git a/<f> b/<f>": f), each without its index line and the line numbers
// of its hunk headers.
func filePatches(diff string) map[string]string {
	m, file := map[string]string{}, ""
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			file = strings.TrimPrefix(l, "diff --git ")
			if i := strings.Index(file, " b/"); strings.HasPrefix(file, "a/") && i > 0 {
				file = file[2:i]
			}
			continue
		case strings.HasPrefix(l, "index "):
			continue
		case strings.HasPrefix(l, "@@ "):
			if _, rest, ok := strings.Cut(l[3:], " @@"); ok {
				l = "@@" + rest
			}
		}
		m[file] += l + "\n"
	}
	return m
}

// specBody is the spec part of a merge's message: a blank line, "Spec rev N:", a blank line and in.Spec (the
// store's rev at gate time) without its `Model:` line (that metadata line is dropped, the content is not
// redacted); "" when none is stored (a task dispatched without one).
func specBody(in port.GateInput) string {
	if strings.TrimSpace(in.Spec) == "" {
		return ""
	}
	return fmt.Sprintf("\n\nSpec rev %d:\n\n%s", in.SpecRev, strings.TrimRight(spec.WithoutModelLine(in.Spec), "\n"))
}

// mergeMessage is every merge's message, the gate's own and the integrator's resolved one alike. Subject:
// "Merge T-xx: <in.Title>" (the spec's title), else the worker's summary's first line. Body, each part after a
// blank line: the spec (specBody), moved (a moved target's "Merged onto ..." line), resolver (the integrator's own
// message body without git's "#" comment lines) and "Summary: <in.Body>" (the report's whole Summary section;
// no Body: the first line, in.Summary; not repeated when it is the subject).
func mergeMessage(in port.GateInput, moved, resolver string) string {
	summary, _, _ := strings.Cut(strings.TrimSpace(in.Summary), "\n")
	body := strings.TrimSpace(in.Body)
	if body == "" {
		body = summary
	}
	subject := strings.TrimSpace(in.Title)
	if subject == "" {
		subject = summary
	}
	msg := strings.TrimSpace("Merge " + in.Task + ": " + subject)
	msg += specBody(in)
	if moved != "" {
		msg += "\n\n" + moved
	}
	var kept []string
	for _, l := range strings.Split(resolver, "\n") {
		if !strings.HasPrefix(l, "#") {
			kept = append(kept, strings.TrimRight(l, " \t"))
		}
	}
	if r := strings.TrimSpace(strings.Join(kept, "\n")); r != "" {
		msg += "\n\n" + r
	}
	if body != "" && body != subject {
		msg += "\n\nSummary: " + body
	}
	return msg
}

// specResolved gives the resolved merge checked out in dir the gate's message (resolvedMessage): the merge is amended
// (same parents, tree and author; the committer date the merge's, so a repeat makes the same commit). The candidate's
// sha.
func (s Scratch) specResolved(g gitIO, dir string, in port.GateInput) (string, error) {
	out, err := g.Message(dir, in.Input)
	if err != nil {
		return "", err
	}
	msg, date := resolvedMessage(in, out)
	if err := g.Amend(dir, msg, date); err != nil {
		return "", err
	}
	return g.Rev(dir, "HEAD")
}

// resolvedMessage is a resolved merge's amended message and committer date, from its date and message as git shows
// them (Message): mergeMessage's subject "Merge T-xx: <title>" in place of the resolver's own, the spec, the
// resolver's body.
func resolvedMessage(in port.GateInput, shown string) (msg, date string) {
	ct, msg, _ := strings.Cut(shown, "\n")
	_, rest, _ := strings.Cut(strings.TrimSpace(msg), "\n")
	return mergeMessage(in, "", rest), "@" + ct + " +0000"
}

// gateHalt is h, how r's checks stopped, as a gate outcome: red or fail are final (nothing to clean: the next gate resets), git I/O, the check
// slot and a signal are errors; nil, nil when h is.
func gateHalt(r *runner, h *halt) (*port.GateOutcome, error) {
	o := &port.GateOutcome{}
	switch {
	case h == nil:
		return nil, nil
	case h.signal:
		return o, fmt.Errorf("interrupted (exit %d)", h.code)
	case h.err != nil:
		return o, h.err
	case h.red != nil:
		what, evidence := r.redLine(*h.red)
		o.Kind, o.Line = port.OutcomeRed, fmt.Sprintf("RED %s (%s)%s", h.red.name, what, evidence)
	default:
		o.Kind, o.Line = port.OutcomeFail, "FAIL "+h.fail
	}
	return o, nil
}

// commit commits the open merge of the target and input in dir with the newer parent's date (the same commit on every
// repeat): its sha. --no-verify as submit's; prepare-commit-msg still runs.
func (s Scratch) commit(g gitIO, dir, msg, target, input string) (string, error) {
	out, err := g.Dates(dir, target, input)
	if err != nil {
		return "", err
	}
	date, err := newestDate(out)
	if err != nil {
		return "", fmt.Errorf("commit dates of %s and %s: %q", target, input, out)
	}
	if err := g.Commit(dir, msg, date); err != nil {
		return "", err
	}
	return g.Rev(dir, "HEAD")
}

// newestDate is the newest of dates (unix seconds, one per line) as git's date "@<s> +0000".
func newestDate(dates string) (string, error) {
	var newest int64
	for _, l := range lines(dates) {
		ct, err := strconv.ParseInt(l, 10, 64)
		if err != nil {
			return "", err
		}
		newest = max(newest, ct)
	}
	return fmt.Sprintf("@%d +0000", newest), nil
}

// Resolved pins serve's worktree HEAD, the merge the integrator resolved and committed there, at
// refs/obor/resolved/<task>, only when it is the merge of input (the conflicting gate's: submit's sha, a lane or moved
// gate alike) on top of the target: a merge whose second parent is input and whose first is in the target.
// Refused: no worktree, a merge still open, another HEAD (another task's gate, say), or one the target holds.
func (s Scratch) Resolved(ctx context.Context, task, input string, m *port.Mismatch) (string, error) {
	s.gitCtx = ctx
	g := s.gio()
	dir := s.dir()
	if _, err := os.Stat(dir); err != nil {
		return "", port.Refused{Line: "FAIL no serve worktree " + dir}
	}
	if _, open, err := g.Verify(dir, "MERGE_HEAD"); err != nil {
		return "", err
	} else if open {
		return "", port.Refused{Line: fmt.Sprintf("FAIL merge in progress in %s: commit the resolution first", dir)}
	}
	head, err := g.Rev(dir, "HEAD")
	if err != nil {
		return "", err
	}
	_, target, err := s.target(g)
	if err != nil {
		return "", err
	}
	if in, err := g.Ancestor(dir, head, target); err != nil {
		return "", err
	} else if in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL no resolved merge in %s (commit it there)", dir)}
	}
	parents, err := g.Parents(dir, head)
	if err != nil {
		return "", err
	}
	if len(parents) != 3 || parents[2] != input {
		return "", port.Refused{Line: fmt.Sprintf("FAIL HEAD of %s is not %s's merge of %s (resolve that conflict there)",
			dir, task, input)}
	} else if in, err := g.Ancestor(dir, parents[1], target); err != nil {
		return "", err
	} else if !in {
		return "", port.Refused{Line: fmt.Sprintf("FAIL HEAD of %s is not a merge onto %s", dir, target)}
	}
	if m != nil {
		if lost, err := s.lostEdits(g, dir, head, m); err != nil {
			return "", err
		} else if len(lost) > 0 {
			return "", lostRefusal(dir, m.Checked, lost)
		}
	}
	if err := g.UpdateRef(resolvedRefs+task, head); err != nil {
		return "", err
	}
	return head, nil
}

// lostRefusal is Resolved's refusal of a merge in dir that still lacks the checked resolution's edits in lost.
func lostRefusal(dir, checked string, lost []string) port.Refused {
	c, f := checked[:min(7, len(checked))], strings.Join(lost, " ")
	return port.Refused{Line: fmt.Sprintf("FAIL the merge in %s still lacks edits of the checked resolution %s in %s "+
		"(unchanged from the replay): compare with git diff %s -- %s, restore them and commit", dir, c, f, c, f)}
}

// lostEdits is the files of m whose patch in the committed merge head still differs from the checked resolution's and
// whose content is the replay's: the integrator committed nothing new there.
func (s Scratch) lostEdits(g gitReader, dir, head string, m *port.Mismatch) ([]string, error) {
	out, err := g.Patch(dir, head+"^1", head)
	if err != nil {
		return nil, err
	}
	got := filePatches(out)
	if out, err = g.Patch(dir, m.Checked+"^1", m.Checked); err != nil {
		return nil, err
	}
	want := filePatches(out)
	var lost []string
	for _, f := range m.Files {
		if got[f] == want[f] {
			continue
		}
		now, _, _ := g.Verify(dir, head+":"+f) // "": absent
		was, _, _ := g.Verify(dir, m.Tree+":"+f)
		if now == was {
			lost = append(lost, f)
		}
	}
	return lost, nil
}

// Publish fast-forwards the target to c.SHA when it is still c.Target and deletes the task's pins. Already: the
// target holds c (a repeat after the fast-forward). Hold: uncommitted changes in the main checkout touch merged files;
// decided before any write, so a lock in the main checkout can only be an error.
func (s Scratch) Publish(ctx context.Context, c port.Candidate) (port.PublishOutcome, error) {
	s.gitCtx = ctx
	g := s.gio()
	root := s.Cfg.Root
	// A fast-forward refused while the target moved goes round once more: Already or Moved then.
	for range 2 {
		_, head, err := s.target(g)
		if err != nil {
			return port.PublishOutcome{}, err
		}
		if in, err := g.Ancestor(root, c.SHA, head); err != nil {
			return port.PublishOutcome{}, err
		} else if in {
			return port.PublishOutcome{Kind: port.PublishAlready}, s.Unpin(ctx, c.Task)
		}
		if head != c.Target {
			return port.PublishOutcome{Kind: port.PublishMoved, Head: head}, nil
		}
		paths, err := g.Changed(c.Target, c.SHA)
		if err != nil {
			return port.PublishOutcome{}, err
		}
		if len(paths) > 0 {
			st, err := g.Status(root, paths...)
			if err != nil {
				return port.PublishOutcome{}, err
			}
			if len(st) > 0 {
				return port.PublishOutcome{Kind: port.PublishHold, Files: statusPaths(st)}, nil
			}
		}
		// ff-only moves the checked-out branch: off the target it would publish elsewhere (the loop pauses).
		if b, err := g.Branch(root); err != nil {
			return port.PublishOutcome{}, err
		} else if b != s.Cfg.MainBranch() {
			return port.PublishOutcome{}, fmt.Errorf("publish %s: the main checkout %s is not on %s", c.Task, root, s.Cfg.MainBranch())
		}
		ffErr := g.FF(c.SHA)
		if ffErr == nil {
			return port.PublishOutcome{Kind: port.PublishMerged}, s.Unpin(ctx, c.Task)
		}
		if _, now, err := s.target(g); err != nil {
			return port.PublishOutcome{}, err
		} else if now == c.Target {
			return port.PublishOutcome{}, ffErr
		}
	}
	return port.PublishOutcome{}, fmt.Errorf("publish %s: the target keeps moving", c.Task)
}

// statusPaths is the paths of status --porcelain lines (each past its two status letters and the space).
func statusPaths(st []string) []string {
	var files []string
	for _, l := range st {
		files = append(files, l[min(3, len(l)):])
	}
	return files
}

// AfterMerge runs commands.after_merge in the main checkout for the published c when its "when" globs match the
// merged files (none: always). A failed run's full output goes to .obor/state/logs/<task>-after_merge.log; a passing
// one writes none (the log is a failure's trace). A failing command is the WARN line. Its intent (G) is the
// once: a cut before its result runs it again (after_merge is idempotent).
func (s Scratch) AfterMerge(ctx context.Context, c port.Candidate) (string, error) {
	s.gitCtx = ctx
	if s.Out == nil {
		s.Out = io.Discard
	}
	g := s.gio()
	cmd, ok := s.command("after_merge")
	if !ok {
		return "skipped", nil
	}
	if len(cmd.When) > 0 {
		files, err := g.Changed(c.Target, c.SHA)
		if err != nil {
			return "", err
		}
		if !anyMatch(files, cmd.When) {
			return "skipped", nil
		}
	}
	return s.afterMergeRun(ctx, g, c)
}

// anyMatch reports whether one of files matches one of globs.
func anyMatch(files, globs []string) bool {
	for _, f := range files {
		if matchAny(f, globs) {
			return true
		}
	}
	return false
}

// afterMergeRun runs commands.after_merge in the main checkout for the published c and returns its result line. The
// checkout must be at c.SHA on a branch (publish fast-forwards it; a later merge or a checkout moved it away): else a
// WARN line and no run.
func (s Scratch) afterMergeRun(ctx context.Context, g gitReader, c port.Candidate) (string, error) {
	branch, err := g.Branch(s.Cfg.Root)
	if err != nil {
		return "", err
	}
	at := ""
	if branch != "" {
		if at, err = g.Rev(s.Cfg.Root, "refs/heads/"+branch); err != nil {
			return "", err
		}
	}
	if at != c.SHA {
		line := "WARN after_merge not run: the main checkout is not at the published merge " + c.SHA
		fmt.Fprintln(s.Out, line)
		return line, nil
	}
	r := newRunner(ctx, &s.Options, s.Cfg.Root, c.Task)
	o, _, h := r.run("after_merge", s.Cfg.Root)
	if h != nil {
		return "", fmt.Errorf("after_merge interrupted (exit %d)", h.code)
	}
	if o.rc != 0 {
		line := afterMergeWarn(o)
		fmt.Fprintln(s.Out, line)
		return line, nil
	}
	return "ran", nil
}

// afterMergeWarn is a failed after_merge's WARN line: its last non-blank output line, else its exit code, and its log
// (named as a failing check's line names its own).
func afterMergeWarn(o output) string {
	last := fmt.Sprintf("rc %d", o.rc)
	for _, l := range lines(o.text) {
		if strings.TrimSpace(l) != "" {
			last = l
		}
	}
	line := "WARN after_merge failed: " + last
	if o.log != "" {
		line += "; log " + o.log
	}
	return line
}

// Landed: the target holds merge evidence of task (a merge outside the app); an error: git could not read its log.
// MergeEvidenceBy reads HEAD's log: here it reads merge.target_branch's, whatever the main checkout is on.
func (s Scratch) Landed(ctx context.Context, task string) (bool, error) {
	s.gitCtx = ctx
	g := s.gio()
	branch, _, err := s.target(g)
	if err != nil {
		return false, err
	}
	return NewLaneTable(ctx, s.Cfg).MergeEvidenceBy(task, func(args ...string) (string, error) {
		if n := len(args); n > 0 && args[n-1] == "HEAD" {
			args = append(args[:n-1:n-1], "refs/heads/"+branch)
		}
		return g.Log(args...)
	})
}

// Unpin deletes the task's pins: every submitted one, candidate, resolved (an absent one is not an error).
func (s Scratch) Unpin(ctx context.Context, task string) error {
	s.gitCtx = ctx
	g := s.gio()
	refs, err := g.Refs(submittedRefs + task + "/")
	if err != nil {
		return err
	}
	for _, ref := range append(refs, candidateRefs+task, resolvedRefs+task) {
		if err := g.DeleteRef(ref); err != nil {
			return err
		}
	}
	return s.dropFlatPin(g, task)
}

// dropFlatPin deletes refs/obor/submitted/<task>, the pin before submits had an id: it would stand in the way of the
// task's directory of pins.
func (s Scratch) dropFlatPin(g gitIO, task string) error {
	if _, flat, err := g.Verify(s.Cfg.Root, submittedRefs+task); err != nil || !flat {
		return err
	}
	return g.DeleteRef(submittedRefs + task)
}

// dir is serve's worktree, <lanes.dir>/merge.
func (s Scratch) dir() string {
	return s.Cfg.ScratchDir()
}

// worktree makes sure dir is a worktree of the project, added detached at target when missing (-f: a worktree whose
// directory was removed is still registered).
func (s Scratch) worktree(dir, target string) error {
	g := s.gio()
	if _, err := os.Stat(dir); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		return g.AddWorktree(dir, target)
	}
	top, err := g.Toplevel(dir)
	if err != nil {
		return err
	}
	if project.Physical(top) != project.Physical(dir) {
		return fmt.Errorf("%s is not a git worktree", dir)
	}
	return nil
}

// target is merge.target_branch and the sha it is at, whatever the main checkout has checked out.
func (s Scratch) target(g gitReader) (branch, head string, err error) {
	branch = s.Cfg.MainBranch()
	head, err = g.Rev(s.Cfg.Root, "refs/heads/"+branch)
	return branch, head, err
}
