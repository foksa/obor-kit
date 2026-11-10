package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/app/wire"
	"obor/internal/project"
)

// Nightly is `obor nightly`: the milestone check tier in
// its own worktree (<lanes.dir>/merge-nightly, added when missing) reset to the head of the main checkout's current
// branch: lanes.setup, then check and check_milestone, each skipped when unset. Non-blocking: every command
// runs even after a failure (a failed lane_setup stops the rest). A red check reruns its named failing tests once by
// merge.retest inside the slot it holds; green then is the step's green and its line names the rerun.
// It then backs up the project's database (wire.Backup), sweeps.obor (project.Sweep: each removal it could not
// make is one detail line, never RED; its backup step only after a backup with a nonempty reply) and runs
// merge.nightly_commands in order (each a
// commands.<name>, non-gating — one report line, never RED; a nonzero exit a WARN line). Writes
// .obor/state/last-full-check
// "<sha> <epoch> GREEN|RED <summary>" (LastFullCheck reads it for serve's nightly; obor status reads it too). A live nightly run of the project is waited for (max
// DefaultWindowWait).
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

	var results, failed, details, evidence []string
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
		results = append(results, name+" "+s.what(o))
		if saved > 0 {
			// A retest that saved the run: the GREEN line names the rerun, so a flaky night shows in obor status.
			results = append(results, fmt.Sprintf("retest saved %d", saved))
		}
		if o.rc == 0 {
			return nil
		}
		failed = append(failed, name)
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
	if len(failed) == 0 {
		s.retest = true // the nightly reruns a red check's failing tests once, inside the slot it holds
		for _, name := range []string{"check", "check_milestone"} {
			if r := step(name, true); r != nil {
				return r
			}
		}
	}
	s.releaseSlot()
	s.reset(sha) // no command artifacts left behind
	sock := wire.SocketPath(s.Cfg.Root)
	ctx := s.callCtx
	if ctx == nil {
		ctx = s.ctx // an interrupted nightly does not wait out the call's deadline on a backup
	}
	line, failure, backedUp := backupOutcome(wire.Backup(ctx, sock, s.Cfg.Root, time.Now()))
	if failure != "" {
		failed = append(failed, "backup")
		evidence = append(evidence, failure)
	}
	if line != "" {
		results = append(results, line)
	}
	// The nightly sweep: after the backup, both the serve path and the direct one; a removal it could not
	// make is a line, never a failure. Its backup step runs only when the backup above succeeded.
	details = append(details, project.Sweep(s.Cfg, time.Now(), backedUp)...)
	// merge.nightly_commands: after the gating checks and the backup, in order, each non-gating.
	for _, name := range s.Cfg.MergeNightlyCommands() {
		o, ok, h := s.run(name)
		if h != nil {
			return h.result()
		}
		if !ok { // config validate refuses a name that is no command; a config newer than this binary is no failure
			continue
		}
		results = append(results, nonGatingLine(name, o.rc, o.text))
		if notes := noteLines(o.text); len(notes) > 0 {
			details = append(details, fmt.Sprintf("--- %s (non-gating)", name))
			details = append(details, notes...)
		}
	}

	verdict, summary := "GREEN", strings.Join(results, ", ")
	if summary == "" {
		summary = "no commands"
	}
	summary = fmt.Sprintf("%s, %ds", summary, secs(time.Since(s.start)))
	if len(failed) > 0 {
		verdict = "RED"
		summary = "failed: " + strings.Join(failed, ", ") + "; " + summary
	}
	file := filepath.Join(s.state, "last-full-check")
	if project.WriteAtomic(file, []byte(fmt.Sprintf("%s %d %s %s\n", sha, time.Now().Unix(), verdict, summary))) != nil {
		return result(Fail, "FAIL cannot write %s", file)
	}
	s.printLines(details)
	if verdict == "GREEN" {
		return result(OK, "NIGHTLY GREEN %s on %s (%s)", sha[:min(7, len(sha))], s.target, summary)
	}
	return result(Red, "NIGHTLY RED %s on %s (%s) — %s", sha[:min(7, len(sha))], s.target, summary,
		strings.Join(evidence, " | "))
}

// nonGatingLine is a merge.nightly_commands command's result line: its own summary, a nonzero exit a WARN line,
// never RED. The summary is the command's last line that starts "<name>: " (prefix stripped), so a wrapper's trailer
// never stands in for it — make appends `make: *** [check-race] Error 1` to a failed recipe; with no
// such line, the command's last non-blank line is. A command that printed no line at all is rc=<rc>.
func nonGatingLine(name string, rc int, text string) string {
	summary, found := "", false
	ls := lines(text)
	for i := len(ls) - 1; i >= 0; i-- {
		l := strings.TrimSpace(ls[i])
		if strings.HasPrefix(l, name+": ") {
			summary, found = strings.TrimPrefix(l, name+": "), true
			break
		}
	}
	if !found {
		summary = lastLine(text)
	}
	if summary == "" {
		summary = "rc=" + strconv.Itoa(rc)
	}
	if rc != 0 { // a warning line, never RED
		return fmt.Sprintf("WARN %s (rc %d, non-gating): %s", name, rc, summary)
	}
	return name + " (non-gating) " + summary
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

// LastFullCheck reads root's .obor/state/last-full-check (Nightly): the sha, time and verdict (GREEN|RED) of the last
// full check; none yet: "", 0, "".
func LastFullCheck(root string) (sha string, epoch int64, verdict string, err error) {
	b, err := os.ReadFile(filepath.Join(project.StateDir(root), "last-full-check"))
	if os.IsNotExist(err) {
		return "", 0, "", nil
	}
	if err != nil {
		return "", 0, "", err
	}
	f := strings.Fields(project.FirstLine(string(b)))
	if len(f) < 3 {
		return "", 0, "", fmt.Errorf("last-full-check: no \"<sha> <epoch> GREEN|RED\" line")
	}
	epoch, _ = strconv.ParseInt(f[1], 10, 64)
	return f[0], epoch, f[2], nil
}
