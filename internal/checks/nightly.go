package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/project"
)

// Nightly is `obor nightly` (label NIGHTLY) and `obor milestone-check` (label MILESTONE): the milestone check tier in
// its own worktree (<merge.scratch_dir>-nightly, added when missing) reset to the head of the main checkout's current
// branch: commands.lane_setup, then check,
// check_milestone, baseline (if its dir exists), gate (forced: OBOR_GATE=1, its paths and "at" ignored) and e2e, each
// skipped when unset. Non-blocking: every command runs even after a failure (a failed lane_setup stops the rest).
// NIGHTLY then backs up the project's database (app.Backup). Writes .obor/state/last-full-check
// "<sha> <epoch> GREEN|RED <summary>" (LastFullCheck reads it for NightlyDue and serve's nightly; obor status reads it too). A live nightly run of the project is waited for (max
// merge.window_wait s).
func Nightly(o Options, label string) Result {
	s, r := open(context.Background(), o, strings.ToLower(label))
	if r != nil {
		return *r
	}
	defer s.close()
	r = s.nightly(label)
	if code := s.interrupted(); code != 0 {
		return Result{Code: code}
	}
	return *r
}

func (s *session) nightly(label string) *Result {
	if r := s.lock("obor " + strings.ToLower(label)); r != nil {
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
	step := func(name string) *Result {
		o, ok, h := s.run(name)
		if h != nil {
			return h.result()
		}
		if !ok {
			return nil
		}
		results = append(results, name+" "+s.what(o))
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
	if r := step("lane_setup"); r != nil {
		return r
	}
	if len(failed) == 0 {
		for _, name := range []string{"check", "check_milestone", "baseline", "gate", "e2e"} {
			switch name {
			case "baseline":
				dir := s.baselineDir()
				if dir == "" {
					continue
				}
				s.env = append(s.env, "OBOR_BASELINE="+dir)
			case "gate":
				env := s.env
				s.env = append(env[:len(env):len(env)], "OBOR_GATE=1")
				r := step(name)
				s.env = env
				if r != nil {
					return r
				}
				continue
			}
			if r := step(name); r != nil {
				return r
			}
		}
	}
	s.releaseSlot()
	s.reset(sha) // no command artifacts left behind
	if label == "NIGHTLY" {
		sock := app.SocketPath(s.Cfg.Root)
		ctx := s.callCtx
		if ctx == nil {
			ctx = s.ctx // an interrupted nightly does not wait out the call's deadline on a backup
		}
		switch reply, err := app.Backup(ctx, sock, s.Cfg.Root, time.Now()); {
		case err != nil:
			failed = append(failed, "backup")
			evidence = append(evidence, "backup: "+err.Error())
		case reply != "":
			results = append(results, "backup "+filepath.Base(strings.TrimPrefix(reply, "BACKUP ")))
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
		return result(OK, "%s GREEN %s on %s (%s)", label, sha[:min(7, len(sha))], s.target, summary)
	}
	return result(Red, "%s RED %s on %s (%s) — %s", label, sha[:min(7, len(sha))], s.target, summary,
		strings.Join(evidence, " | "))
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

// NightlyDue: root's full check is due for obor serve's nightly run (merge.nightly_at, T-464): the last full check is
// RED, or merges (the log's fact merged, in seq order, T-525) has a partial merge it did not check; none known while
// fresh (no merge and no nightly run in the store: an older merges.log is not read) is due once. Checked is git's
// answer, not the time (T-468): a merge published while the check ran is not in the sha it checked, though it is older
// than its epoch. Only the partial merges after the checked sha's own merge are asked (the earlier ones are its
// ancestors; none: all), and a sha git no longer has is skipped (--ignore-missing).
func NightlyDue(root string, merges []event.Merge, fresh bool) (bool, error) {
	checked, _, verdict, err := LastFullCheck(root)
	if err != nil || verdict == "RED" {
		return verdict == "RED", err
	}
	if len(merges) == 0 {
		return fresh, nil
	}
	var partial []string
	for _, m := range merges {
		switch {
		case checked != "" && m.SHA == checked:
			partial = partial[:0] // merged before the checked merge: in the sha it checked
		case !m.Full:
			partial = append(partial, m.SHA)
		}
	}
	if len(partial) == 0 || checked == "" {
		return len(partial) > 0, nil
	}
	// One git call: a commit reachable from a partial merge but not from the checked sha.
	out, err := project.Git(root, append(append([]string{"rev-list", "--ignore-missing", "--max-count=1"}, partial...), "^"+checked, "--")...)
	if err != nil {
		return false, fmt.Errorf("merges against last-full-check %s: %v", checked[:min(7, len(checked))], err)
	}
	return strings.TrimSpace(out) != "", nil
}
