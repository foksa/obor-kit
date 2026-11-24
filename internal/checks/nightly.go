package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/wire"
	"obor/internal/project"
)

// Nightly is `obor nightly`: the milestone check tier in
// its own worktree (<lanes.dir>/merge-nightly, added when missing) reset to the head of merge.target_branch (the
// branch tasks merge into, whatever the main checkout has checked out): lanes.setup, then check and check_milestone,
// each skipped when unset. Non-blocking: every command
// runs even after a failure (a failed lane_setup stops the rest). A red check reruns its named failing tests once by
// merge.retest inside the slot it holds; green then is the step's green and its line names the rerun.
// It then backs up the project's database (wire.Backup), sweeps.obor (project.Sweep: each removal it could not
// make is one detail line, never RED; its backup step only after a backup with a nonempty reply) and runs
// merge.nightly_commands in order (each a
// commands.<name>, non-gating — one report line, never RED; a nonzero exit a WARN line). Writes each step's outcome
// to .obor/state/full-check.json beside the previous check's (ReadFullChecks: serve's nightly reads it), and the
// summary derived from them to .obor/state/last-full-check "<sha> <epoch> GREEN|RED <summary>" (obor status reads
// it). A live nightly run of the project is waited for (max DefaultWindowWait).
func Nightly(o Options) Result {
	s, r := open(context.Background(), o, "nightly")
	if r != nil {
		return *r
	}
	defer s.close()
	r = s.nightly()
	if code := s.interrupted(); code != 0 {
		return Result{Code: code}
	}
	return *r
}

// nightly is prepare (the lock, the worktree reset to the target, its sha) and then the steps and the report.
func (s *session) nightly() *Result {
	if r := s.lock("obor nightly"); r != nil {
		return r
	}
	if err := (Scratch{Options: s.Options}).worktree(s.scratch, s.target); err != nil {
		return result(Fail, "FAIL %v", err)
	}
	if !s.reset(s.target) {
		return result(Fail, "FAIL cannot reset %s to %s", s.scratch, s.target)
	}
	sha, _ := s.git(s.scratch, "rev-parse", "HEAD")
	linkLaneLinks(s.Cfg, s.root, s.scratch)
	// The session's own reset, not gitIO.Clean: that one keeps obor's links, this `clean -fdq` does not, and steps
	// links them again after it.
	return s.steps(sha, func() { s.reset(sha) })
}

// steps runs the nightly's commands in s.dir (the checks, the backup, the sweep, merge.nightly_commands), writes
// full-check.json and last-full-check for sha and returns the result line. It reads no git itself: scrub is what puts the worktree back
// at sha between the gating checks and the rest, "no command artifacts left behind" (nil: nothing to scrub), so a
// test runs it on a plain dir. That reset's `clean -fdq` takes the lane links with the artifacts, so the nightly
// links them again before the commands that follow it.
func (s *session) steps(sha string, scrub func()) *Result {
	var outcomes []StepOutcome
	var details, evidence []string
	gatingFailed := false
	// step runs commands.<name>; retest is whether a red's failing tests are rerun once (the nightly's check and
	// check_milestone, as a lane test does; lane_setup and merge.nightly_commands never are).
	step := func(name string, retest bool) *Result {
		var o output
		var ok bool
		var h *halt
		var saved int
		if retest {
			o, ok, h, saved = s.retested(name)
		} else {
			o, ok, h = s.run(name)
		}
		if h != nil {
			return h.result()
		}
		if !ok {
			return nil
		}
		report := s.what(o)
		line := name + " " + report
		if saved > 0 {
			// A retest that saved the run: the GREEN line names the rerun, so a flaky night shows in obor status.
			line += fmt.Sprintf(", retest saved %d", saved)
		}
		outcomes = append(outcomes, StepOutcome{Name: name, Gating: true, OK: o.rc == 0, Fails: failCount(report),
			Report: report, Line: line, Log: o.log})
		if o.rc == 0 {
			return nil
		}
		gatingFailed = true
		details = append(details, fmt.Sprintf("--- %s (rc %d)", name, o.rc))
		if e := s.evidence(o); e != "" {
			evidence = append(evidence, name+": "+e)
		}
		details = append(details, s.details(o)...)
		return nil
	}
	if r := step("lane_setup", false); r != nil {
		return r
	}
	if !gatingFailed {
		s.retest = true // the nightly reruns a red check's failing tests once, inside the slot it holds
		for _, name := range []string{"check", "check_milestone"} {
			if r := step(name, true); r != nil {
				return r
			}
		}
	}
	s.releaseSlot()
	if scrub != nil {
		scrub() // no command artifacts left behind
		linkLaneLinks(s.Cfg, s.root, s.scratch)
	}
	sock := wire.SocketPath(s.Cfg.Root)
	ctx := s.callCtx
	if ctx == nil {
		ctx = s.ctx // an interrupted nightly does not wait out the call's deadline on a backup
	}
	line, failure, backedUp := backupOutcome(wire.Backup(ctx, sock, s.Cfg.Root, time.Now()))
	switch {
	case failure != "":
		outcomes = append(outcomes, StepOutcome{Name: "backup", Gating: true, Report: failure})
		evidence = append(evidence, failure)
	case line != "":
		outcomes = append(outcomes, StepOutcome{Name: "backup", Gating: true, OK: true, Report: line, Line: line})
	}
	// The nightly sweep: after the backup, both the serve path and the direct one; a removal it could not
	// make is a line, never a failure. Its backup step runs only when the backup above succeeded.
	details = append(details, project.Sweep(s.Cfg, time.Now(), backedUp)...)
	// The branch prune of tasks merged over a week ago: serve's (the fold says what merged), so none while it is down.
	if o, ok := pruneStep(ctx, sock, s.Cfg.Root); ok {
		outcomes = append(outcomes, o)
	}
	// merge.nightly_commands: after the gating checks and the backup, in order, each non-gating.
	for _, name := range s.Cfg.MergeNightlyCommands() {
		o, ok, h := s.run(name)
		if h != nil {
			return h.result()
		}
		if !ok { // config validate refuses a name that is no command; a config newer than this binary is no failure
			continue
		}
		outcomes = append(outcomes, s.nonGatingOutcome(name, o))
		if notes := noteLines(o.text); len(notes) > 0 {
			details = append(details, fmt.Sprintf("--- %s (non-gating)", name))
			details = append(details, notes...)
		}
	}

	c := FullCheck{SHA: sha, At: time.Now().Unix(), Seconds: secs(time.Since(s.start)), Steps: outcomes}
	c.Verdict = c.verdict()
	summary := c.Summary()
	if err := writeFullCheck(s.state, c); err != nil {
		return result(Fail, "FAIL %v", err)
	}
	file := filepath.Join(s.state, "last-full-check")
	if project.WriteAtomic(file, []byte(fmt.Sprintf("%s %d %s %s\n", sha, c.At, c.Verdict, summary))) != nil {
		return result(Fail, "FAIL cannot write %s", file)
	}
	s.printLines(details)
	if c.Verdict == "GREEN" {
		return result(OK, "NIGHTLY GREEN %s on %s (%s)", sha[:min(7, len(sha))], s.target, summary)
	}
	return result(Red, "NIGHTLY RED %s on %s (%s) — %s", sha[:min(7, len(sha))], s.target, summary,
		strings.Join(evidence, " | "))
}

// nonGatingOutcome is a merge.nightly_commands command's outcome: its own report (nonGatingReport) and the fail count
// in it; failed when it exited nonzero or reported failures. A zero-exit run that reports failures keeps its full
// output in its log, as a failed one does (the runner removed it: it passed).
func (s *session) nonGatingOutcome(name string, o output) StepOutcome {
	report := nonGatingReport(name, o.rc, o.text)
	fails := failCount(report)
	out := StepOutcome{Name: name, OK: o.rc == 0 && (fails == nil || *fails == 0), Fails: fails, Report: report,
		Line: nonGatingLine(name, o.rc, report), Log: o.log}
	if !out.OK && out.Log == "" && o.rc == 0 {
		out.Log = s.writeLog(name, []byte(o.text))
	}
	return out
}

// nonGatingReport is a merge.nightly_commands command's own summary: its last line that starts "<name>: " (prefix
// stripped), so a wrapper's trailer never stands in for it — make appends `make: *** [check-race] Error 1` to a failed
// recipe; with no such line, the command's last non-blank line is. A command that printed no line at all is rc=<rc>.
func nonGatingReport(name string, rc int, text string) string {
	ls := lines(text)
	for i := len(ls) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(ls[i]); strings.HasPrefix(l, name+": ") {
			return strings.TrimPrefix(l, name+": ")
		}
	}
	if l := lastLine(text); l != "" {
		return l
	}
	return "rc=" + strconv.Itoa(rc)
}

// nonGatingLine is a merge.nightly_commands command's part of the summary: its report, a nonzero exit a WARN line,
// never RED.
func nonGatingLine(name string, rc int, report string) string {
	if rc != 0 { // a warning line, never RED
		return fmt.Sprintf("WARN %s (rc %d, non-gating): %s", name, rc, report)
	}
	return name + " (non-gating) " + report
}

// failCountRE is a report's "<n> fail" count ("0 fail", "8 fail"), not "failed" or "failing".
var failCountRE = regexp.MustCompile(`\b(\d+) fail\b`)

// failCount is the fail count a step's report holds, the sum of its "<n> fail" counts (a chained command reports one
// per part); nil when it reports none.
func failCount(report string) *int {
	ms := failCountRE.FindAllStringSubmatch(report, -1)
	if len(ms) == 0 {
		return nil
	}
	n := 0
	for _, m := range ms {
		v, _ := strconv.Atoi(m[1])
		n += v
	}
	return &n
}

// backupOutcome is nightly's backup step's answer for wire.Backup's reply and error (a unit test, no nightly):
// line is the step's result line, failure the evidence line of a backup that could not be made, and backedUp whether
// the sweep may prune days: only a nonempty reply says a backup landed.
func backupOutcome(reply string, err error) (line, failure string, backedUp bool) {
	switch {
	case err != nil:
		return "", "backup: " + err.Error(), false
	case reply != "":
		return "backup " + filepath.Base(strings.TrimPrefix(reply, "BACKUP ")), "", true
	}
	return "", "", false
}

// pruneStep is the nightly's non-gating prune step: serve's line (`prune: N branches (oldest merge <date>)`); a call
// that failed is a failed step whose log is serve's (the prune's own lines are there); ok false when no serve is
// listening.
func pruneStep(ctx context.Context, sock, root string) (StepOutcome, bool) {
	if _, err := os.Stat(sock); err != nil {
		return StepOutcome{}, false
	}
	line, err := wire.Prune(ctx, sock, root)
	if err != nil {
		return StepOutcome{Name: "prune", Report: err.Error(), Line: "WARN prune (non-gating): " + err.Error(),
			Log: filepath.Join(project.StateDir(root), "logs", "serve.log")}, true
	}
	return StepOutcome{Name: "prune", OK: true, Report: strings.TrimPrefix(line, "prune: "), Line: line}, true
}

// lastLine is text's last non-blank line, trimmed ("" when it printed none).
func lastLine(text string) string {
	ls := lines(text)
	for i := len(ls) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(ls[i]); l != "" {
			return l
		}
	}
	return ""
}

// noteLines are text's lines that start WARN, SKIP or FAIL: a skip is why a check did not run; a warn
// or a fail, what may break.
func noteLines(text string) []string {
	var out []string
	for _, l := range lines(text) {
		if strings.HasPrefix(l, "WARN") || strings.HasPrefix(l, "SKIP") || strings.HasPrefix(l, "FAIL") {
			out = append(out, l)
		}
	}
	return out
}

// StepOutcome is one step of a full check as it ran: its name, whether it gates the verdict, whether it passed (a
// non-gating step that exits 0 but reports failures did not), the fail count its report holds (nil: none), its report,
// its part of the summary ("" none) and the log of its full output ("" when it passed or no log could be written; a
// failed prune: serve's log).
type StepOutcome struct {
	Name   string `json:"name"`
	Gating bool   `json:"gating,omitempty"`
	OK     bool   `json:"ok"`
	Fails  *int   `json:"fails,omitempty"`
	Report string `json:"report,omitempty"`
	Line   string `json:"line,omitempty"`
	Log    string `json:"log,omitempty"`
}

// FullCheck is one full check's outcome: the sha it checked, when it ended (unix seconds), its verdict, how long it
// took and its steps in the order they ran. Its summary is derived from them (Summary).
type FullCheck struct {
	SHA     string        `json:"sha"`
	At      int64         `json:"at"`
	Verdict string        `json:"verdict"`
	Seconds int           `json:"seconds"`
	Steps   []StepOutcome `json:"steps"`
}

// FullChecks is .obor/state/full-check.json: the last full check and the one before it (nil: none), so a restart or a
// hand run between two scheduled ones loses no outcome.
type FullChecks struct {
	Current  *FullCheck `json:"current,omitempty"`
	Previous *FullCheck `json:"previous,omitempty"`
}

// fullCheckFile is the full checks' state file under state.
func fullCheckFile(state string) string { return filepath.Join(state, "full-check.json") }

// verdict is RED when a gating step failed, else GREEN.
func (c FullCheck) verdict() string {
	for _, st := range c.Steps {
		if st.Gating && !st.OK {
			return "RED"
		}
	}
	return "GREEN"
}

// Summary is the check's one-line summary (obor status, the result line): its steps' lines and its time, "no
// commands" when no step has one, prefixed "failed: <gating steps that failed>; " when RED.
func (c FullCheck) Summary() string {
	var parts, failed []string
	for _, st := range c.Steps {
		if st.Line != "" {
			parts = append(parts, st.Line)
		}
		if st.Gating && !st.OK {
			failed = append(failed, st.Name)
		}
	}
	summary := strings.Join(parts, ", ")
	if summary == "" {
		summary = "no commands"
	}
	summary = fmt.Sprintf("%s, %ds", summary, c.Seconds)
	if len(failed) > 0 {
		summary = "failed: " + strings.Join(failed, ", ") + "; " + summary
	}
	return summary
}

// writeFullCheck makes c the current full check in state's full-check.json, the current one before it its previous.
// A file that cannot be read keeps no previous one.
func writeFullCheck(state string, c FullCheck) error {
	next := FullChecks{Current: &c}
	if b, err := os.ReadFile(fullCheckFile(state)); err == nil {
		var was FullChecks
		if json.Unmarshal(b, &was) == nil {
			next.Previous = was.Current
		}
	}
	b, err := json.Marshal(next)
	if err == nil {
		err = project.WriteAtomic(fullCheckFile(state), append(b, '\n'))
	}
	if err != nil {
		return fmt.Errorf("cannot write %s: %v", fullCheckFile(state), err)
	}
	return nil
}

// ReadFullChecks reads root's .obor/state/full-check.json (Nightly): the last full check and the one before it; none
// yet: both nil.
func ReadFullChecks(root string) (FullChecks, error) {
	file := fullCheckFile(project.StateDir(root))
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return FullChecks{}, nil
	}
	if err != nil {
		return FullChecks{}, err
	}
	var c FullChecks
	if err := json.Unmarshal(b, &c); err != nil {
		return FullChecks{}, fmt.Errorf("%s: %v", file, err)
	}
	return c, nil
}
