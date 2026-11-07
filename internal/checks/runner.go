package checks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

// runner is the command side of a check run: commands in dir under their
// timeouts, every one but lane_setup holding the check slot, their logs, counts and failures, and the merge's class.
// It holds no lock and no scratch state: obor serve's Scratch makes one per gate and per lane test.
type runner struct {
	*Options
	ctx         context.Context
	sigCode     func() int // the exit code of the signal that ended ctx (nil: Fail)
	root, state string     // the main checkout and its .obor/state
	dir         string     // where the commands run: the scratch, or a lane (a lane test)
	logName     string     // log file prefix: the task, nightly, milestone
	changedFile string     // the OBOR_CHANGED_FILES file under state
	slot        string
	env         []string // extra command environment (OBOR_CHANGED_FILES, OBOR_BASELINE, OBOR_GATE, a lane test's CPU budget)
	prio        string   // the check slot's priority: "merge" (the merge marker; "" too) or "worker" (a pool slot: a lane test, T-961)
	loaded      string   // " (run under load <x>)" once a slot was claimed after the load wait ran out (T-720)
	pre         string   // the target side of the merge
	class       Class
	mode        string            // class.Mode() once classified
	check       string            // the checks' counts
	gate        string            // ", gate <counts>" when the gate ran
	reused      []string          // the trees the reused receipts named (receipts.go)
	noReuse     bool              // a reused tree went stale: every check runs
	pruned      bool              // the old logs were removed (once per run)
	onCommand   func(name string) // told each command as it starts and "" as it ends (serve's gate: obor status' running line, T-669, T-673)
}

func newRunner(ctx context.Context, o *Options, dir, logName, changedFile string) runner {
	return runner{Options: o, ctx: ctx, root: o.Cfg.Root, state: project.StateDir(o.Cfg.Root), dir: dir, logName: logName,
		changedFile: changedFile}
}

// interrupted is the signal's exit code once ctx ended, 0 while it runs.
func (r *runner) interrupted() int {
	switch {
	case r.ctx.Err() == nil:
		return 0
	case r.sigCode != nil:
		return r.sigCode()
	}
	return Fail
}

// halt is why a run stopped short of green: a failed check (red), a reason the checks cannot pass (fail: a missing command, lane_setup), an error the run may pass when tried again (err: git
// I/O, the check slot) or a signal (its exit code).
type halt struct {
	red    *output
	fail   string
	err    error
	signal bool
	code   int
}

func interrupt(code int) *halt { return &halt{signal: true, code: code} }

// result is h as a result line without an open merge (nightly): a signal's code, else FAIL.
func (h *halt) result() *Result {
	switch {
	case h.signal:
		return result(h.code, "")
	case h.err != nil:
		return result(Fail, "FAIL %v", h.err)
	}
	return result(Fail, "FAIL %s", h.fail)
}

// output is one command run: its output, exit code (124: timed out) and full log.
type output struct {
	name    string
	text    string
	rc      int
	timeout int
	log     string
}

// command is commands.<name> with obor run's defaults; ok false when unset.
func (opt *Options) command(name string) (config.Command, bool) { return run.Command(opt.Cfg, name) }

// run is exec plus the flake log (T-359, RV-39 #3): the one boundary every check run of the runner passes, so merge,
// serve, nightly and milestone runs all record a red run's failing tests. A run in another dir (after_merge) is no
// check run and records no flakes; its output goes to its log like any other run's.
func (r *runner) run(name string, dir ...string) (output, bool, *halt) {
	o, ok, h := r.exec(name, dir...)
	if h == nil && ok && o.rc != 0 && len(dir) == 0 {
		r.logFlakes(name, o)
	}
	return o, ok, h
}

// exec runs commands.<name> in dir (the runner's unless given) under its timeout: every command but lane_setup holds
// the check slot. ok false when not configured; a halt when the slot cannot be taken or a signal came. It keeps the
// run's full output in writeLog's log, in any dir (after_merge's included); must logs a red run's tests.
func (r *runner) exec(name string, dir ...string) (output, bool, *halt) {
	c, ok := r.command(name)
	if !ok {
		return output{}, false, nil
	}
	environ := append(append([]string(nil), r.Environ...), "OBOR_MERGE=1", run.CPUsEnv(r.Cfg, r.Env))
	if name != "lane_setup" && len(dir) == 0 {
		if h := r.takeSlot(); h != nil {
			return output{}, true, h
		}
		environ = append(environ, "CHECK_PRIORITY="+r.priority(), "CHECK_SLOT_HELD="+r.slot)
	}
	environ = append(environ, r.env...)
	wd := r.dir
	if len(dir) > 0 {
		wd = dir[0]
	}
	if r.onCommand != nil {
		r.onCommand(name)
	}
	var buf bytes.Buffer
	rc, timedOut := run.Execute(r.ctx, c, wd, nil, environ, &buf, &buf)
	if r.onCommand != nil {
		// The command ended, however it ended (a normal exit, a timeout, the signal that cut ctx): between two
		// commands the gate runs and no command does (T-673, RV-75 #4).
		r.onCommand("")
	}
	if code := r.interrupted(); code != 0 {
		return output{}, true, interrupt(code)
	}
	if timedOut {
		rc = 124
	}
	o := output{name: name, text: buf.String(), rc: rc, timeout: c.Timeout, log: r.writeLog(name, buf.Bytes())}
	return o, true, nil
}

// logMaxAge is how long a check log stays in .obor/state/logs.
const logMaxAge = 14 * 24 * time.Hour

// writeLog keeps a command's full output in .obor/state/logs/<logName>-<name>.log; "" when it cannot.
func (r *runner) writeLog(name string, b []byte) string {
	dir := filepath.Join(r.state, "logs")
	path := filepath.Join(dir, r.logName+"-"+name+".log")
	if os.MkdirAll(dir, 0o755) != nil || os.WriteFile(path, b, 0o644) != nil {
		return ""
	}
	if !r.pruned {
		r.pruned = true
		run.PruneOlder(dir, time.Now().Add(-logMaxAge))
	}
	return path
}

// priority is the check slot's priority the runner takes: merge unless set (a lane test's worker, T-961).
func (r *runner) priority() string {
	if r.prio == "" {
		return "merge"
	}
	return r.prio
}

// takeSlot takes the check slot of the runner's priority unless held: the merge-running-<project> marker (merge), or
// a pool slot (worker).
func (r *runner) takeSlot() *halt {
	if r.slot != "" {
		return nil
	}
	slots := r.Slots
	if slots == nil {
		sl := run.SlotsFor(r.Cfg, r.Env)
		slots = &sl
	}
	path, loaded, err := slots.Acquire(r.ctx, r.priority(), r.PID, r.Out)
	switch {
	case err == nil:
		r.slot = path
		if loaded > 0 {
			r.loaded = run.UnderLoad(loaded)
		}
		return nil
	case r.ctx.Err() != nil:
		return interrupt(r.interrupted())
	}
	return &halt{err: err}
}

func (r *runner) releaseSlot() {
	run.Release(r.slot, r.PID)
	r.slot = ""
}

// what is a run's parenthesis: "timed out after <n> s", else its counts.
func (opt *Options) what(o output) string {
	if o.rc == 124 {
		return fmt.Sprintf("timed out after %d s", o.timeout)
	}
	return opt.counts(o)
}

// counts sums the matches of each top-level alternative of merge.summary_regex (a chained command a && b prints a
// summary per part: "7 pass" and "30 pass" → "37 pass"), joined by ", "; rc=<n> without a match.
func (opt *Options) counts(o output) string {
	var res []string
	for _, alt := range alternatives(opt.Cfg.Data.Merge.SummaryRegex) {
		re, err := regexp.Compile(alt)
		if err != nil {
			continue
		}
		if m := re.FindAllString(o.text, -1); len(m) > 0 {
			res = append(res, sum(m))
		}
	}
	if len(res) == 0 {
		return "rc=" + strconv.Itoa(o.rc)
	}
	return strings.Join(res, ", ")
}

var number = regexp.MustCompile(`[0-9]+`)

// sum is the last match with its first number replaced by the sum of every match's first number.
func sum(matches []string) string {
	last := matches[len(matches)-1]
	if len(matches) == 1 || !number.MatchString(last) {
		return last
	}
	total := 0
	for _, m := range matches {
		n, _ := strconv.Atoi(number.FindString(m))
		total += n
	}
	loc := number.FindStringIndex(last)
	return last[:loc[0]] + strconv.Itoa(total) + last[loc[1]:]
}

// alternatives splits a regexp at its top-level |.
func alternatives(re string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(re); i++ {
		switch re[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			depth--
		case '|':
			if depth == 0 {
				out = append(out, re[start:i])
				start = i + 1
			}
		}
	}
	return nonEmpty(append(out, re[start:])...)
}

// failing is the output's merge.fail_grep matches (all of them).
func (opt *Options) failing(o output) []string {
	fg := opt.Cfg.Data.Merge.FailGrep
	if fg == "" {
		return nil
	}
	re, err := regexp.Compile(fg)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range lines(o.text) {
		if re.MatchString(l) {
			out = append(out, l)
		}
	}
	return out
}

// details is what a failure prints before its result line: the fail_grep matches (≤ 15), else the last 15 lines.
func (opt *Options) details(o output) []string {
	if f := opt.failing(o); len(f) > 0 {
		return f[:min(len(f), 15)]
	}
	l := lines(o.text)
	return l[max(0, len(l)-15):]
}

// evidence names a failed run's failing tests (fail_grep matches, ≤ 5) and its full log, for the result line.
func (opt *Options) evidence(o output) string {
	var parts []string
	if f := opt.failing(o); len(f) > 0 {
		names := make([]string, 0, 5)
		for _, l := range f[:min(len(f), 5)] {
			names = append(names, strings.TrimSpace(l))
		}
		more := ""
		if len(f) > 5 {
			more = fmt.Sprintf(" (+%d more)", len(f)-5)
		}
		parts = append(parts, "failing: "+strings.Join(names, ", ")+more)
	}
	if o.log != "" {
		parts = append(parts, "log "+o.log)
	}
	return strings.Join(parts, "; ")
}

func lines(text string) []string {
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func (opt *Options) printLines(ls []string) {
	for _, l := range ls {
		fmt.Fprintln(opt.Out, l)
	}
}

func (opt *Options) load() (float64, bool) {
	if opt.Load != nil {
		return opt.Load()
	}
	return run.SystemLoad()
}

// baselineDir is commands.baseline.dir made absolute, "" when unset or not a directory.
func (r *runner) baselineDir() string {
	c, ok := r.command("baseline")
	if !ok || c.Dir == "" {
		return ""
	}
	dir := config.AbsUnder(r.root, c.Dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// changed lists the files changed in rev (a range, or the merge base: against the worktree) into
// .obor/state/<changedFile> (exported as OBOR_CHANGED_FILES) and classifies the merge.
func (r *runner) changed(task, rev string) *halt {
	path := filepath.Join(r.state, r.changedFile)
	// --no-renames: a rename is a deletion plus an addition, so its source path is classified too (RV-03 #2).
	out, err := r.git(r.dir, "diff", "--no-renames", "--name-only", rev)
	files := lines(out)
	if err == nil {
		err = os.WriteFile(path, []byte(strings.Join(append(slices.Clone(files), ""), "\n")), 0o644)
	}
	if err != nil {
		return &halt{err: fmt.Errorf("cannot list the changed files into %s", path)}
	}
	r.class = Classify(r.Cfg.Data.Merge, files, r.Env.Get("OBOR_FULL") == "1")
	r.mode = r.class.Mode()
	r.env = append(r.env, "OBOR_CHANGED_FILES="+path)
	return nil
}

// laneSetup runs commands.lane_setup: a timeout is red, another failure a fail (its last 15 lines first).
func (r *runner) laneSetup() *halt {
	o, ok, h := r.run("lane_setup")
	switch {
	case h != nil:
		return h
	case !ok || o.rc == 0:
		return nil
	case o.rc == 124:
		return &halt{red: &o}
	}
	l := lines(o.text)
	r.printLines(l[max(0, len(l)-15):])
	return &halt{fail: fmt.Sprintf("lane_setup (rc %d)", o.rc)}
}

// compile runs commands.compile, the merge slot's one check (T-961: build, gofmt, vet, deadcode in obor-kit): a failure
// is red. r.check is its counts, "" when it is not configured (nothing runs).
func (r *runner) compile() *halt {
	o, ok, h := r.must("compile")
	if h != nil || !ok {
		return h
	}
	r.check = "compile " + r.counts(o)
	return nil
}

// runChecks runs the class's checks: partial → merge.guard, then the class's checks (a missing command → fail);
// full → check, then baseline and gate when their "at" is "merge" (default "milestone"), the gate only when the merge
// (pre..HEAD: the candidate checked out in a lane, T-961) touches its paths or OBOR_GATE=1.
func (r *runner) runChecks() *halt {
	r.noReuse = false
	class, envLen := r.class, len(r.env)
	if h := r.runClassChecks(); h != nil || len(r.reused) == 0 {
		return h
	}
	// The guard, baseline or a check may have changed the tree after a receipt was matched (RV-40 #1): the candidate
	// must be the tree the reused checks proved, else nothing is reused and every check runs on the final tree.
	final, err := r.checkedTree()
	if err != nil {
		return &halt{err: err}
	}
	if !slices.ContainsFunc(r.reused, func(t string) bool { return t != final }) {
		return nil
	}
	r.class, r.env, r.noReuse = class, r.env[:envLen], true
	return r.runClassChecks()
}

func (r *runner) runClassChecks() *halt {
	r.gate, r.reused = "", nil
	if r.class.Partial {
		var checks []string
		if r.class.Name != "neutral" {
			var ok bool
			if checks, ok = classChecks(r.Cfg.Data.Merge, r.class.Name); !ok {
				return &halt{fail: fmt.Sprintf("class %s has no unique checks entry in merge.classes (obor config validate) (%s)",
					r.class.Name, r.mode)}
			}
		}
		var counts []string
		for _, name := range append([]string{r.Cfg.Data.Merge.Guard}, checks...) {
			if name != r.Cfg.Data.Merge.Guard {
				if note, ok := r.reuse(name); ok {
					counts = append(counts, name+" "+note)
					continue
				}
			}
			o, ok, h := r.must(name)
			if h != nil {
				return h
			}
			if !ok {
				return &halt{fail: fmt.Sprintf("commands.%s not configured (%s)", name, r.mode)}
			}
			counts = append(counts, name+" "+r.counts(o))
		}
		r.check = strings.Join(counts, ", ")
		return nil
	}
	r.check = "no check"
	if note, ok := r.reuse("check"); ok {
		r.check = note
	} else if o, ok, h := r.must("check"); h != nil {
		return h
	} else if ok {
		r.check = r.counts(o)
	}
	if r.atMerge("baseline") {
		if dir := r.baselineDir(); dir != "" {
			r.env = append(r.env, "OBOR_BASELINE="+dir)
			if _, _, h := r.must("baseline"); h != nil {
				return h
			}
		}
	}
	if gate, ok := r.command("gate"); ok && r.atMerge("gate") {
		run := r.Env.Get("OBOR_GATE") == "1"
		if !run {
			out, err := r.git(r.dir, "diff", "--name-only", r.pre, "HEAD")
			if err != nil {
				return &halt{err: fmt.Errorf("cannot list the files the gate checks in %s", r.dir)}
			}
			for _, f := range lines(out) {
				run = run || matchAny(f, gate.Paths)
			}
		}
		if run {
			if note, ok := r.reuse("gate"); ok {
				r.gate = ", gate " + note
				return nil
			}
			o, _, h := r.must("gate")
			if h != nil {
				return h
			}
			r.gate = ", gate " + r.counts(o)
		}
	}
	return nil
}

func (r *runner) atMerge(name string) bool {
	c, ok := r.command(name)
	return ok && c.At == "merge"
}

// must runs commands.<name>; a failure is red. ok false: not configured. Every red run's failing tests go to the flake
// log (T-350). The load retry that used to save a red with only timeouts failing under load is gone (T-864).
func (r *runner) must(name string) (output, bool, *halt) {
	o, ok, h := r.exec(name)
	if h != nil || !ok {
		return o, ok, h
	}
	if o.rc == 0 {
		return o, ok, nil
	}
	r.logFlakes(name, o)
	return o, true, &halt{red: &o}
}

// redLine is a red run's result-line parts: what failed (counts or the timeout, and the class) and its evidence (" —
// failing tests; log"). Its details are printed first.
func (r *runner) redLine(o output) (what, evidence string) {
	r.printLines(r.details(o))
	what = r.what(o)
	if r.mode != "" {
		what += ", " + r.mode
	}
	if e := r.evidence(o); e != "" {
		evidence = " — " + e
	}
	return what, evidence
}
