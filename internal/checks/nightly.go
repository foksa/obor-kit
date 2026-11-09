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
// its own worktree (<merge.scratch_dir>-nightly, added when missing) reset to the head of the main checkout's current
// branch: commands.lane_setup, then check and check_milestone, each skipped when unset. Non-blocking: every command
// runs even after a failure (a failed lane_setup stops the rest).
// It then backs up the project's database (wire.Backup) and runs merge.nightly_commands in order (T-817: each a
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
		for _, name := range []string{"check", "check_milestone"} {
			if r := step(name); r != nil {
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
	switch reply, err := wire.Backup(ctx, sock, s.Cfg.Root, time.Now()); {
	case err != nil:
		failed = append(failed, "backup")
		evidence = append(evidence, "backup: "+err.Error())
	case reply != "":
		results = append(results, "backup "+filepath.Base(strings.TrimPrefix(reply, "BACKUP ")))
	}
	// merge.nightly_commands (T-817): after the gating checks and the backup, in order, each non-gating.
	for _, name := range s.Cfg.Data.Merge.NightlyCommands {
		o, ok, h := s.run(name)
		if h != nil {
			return h.result()
		}
		if !ok { // config validate refuses a name that is no command; a config newer than this binary is no failure
			continue
		}
		last := strings.TrimPrefix(lastLine(o.text), name+": ")
		if last == "" {
			last = "rc=" + strconv.Itoa(o.rc)
		}
		line := name + " (non-gating) " + last
		if o.rc != 0 { // a warning line, never RED
			line = fmt.Sprintf("WARN %s (rc %d, non-gating): %s", name, o.rc, last)
		}
		results = append(results, line)
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

// noteLines are text's lines that start WARN, SKIP or FAIL (T-805, T-817): a skip is why a check did not run; a warn
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
