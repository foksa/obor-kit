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
	changedFile string     // the OBOR_CHANGED_FILES file under state (a lane test's)
	slot        string
	env         []string // extra command environment (OBOR_CHANGED_FILES, a lane test's CPU budget)
	prio        string   // the check slot's priority: "merge" (the merge marker; "" too) or "worker" (a pool slot: a lane test)
	retest      bool     // a lane test or the nightly: a red run's failing tests are rerun once (commands.retest)
	pre         string   // the target side of the merge
	class       Class
	mode        string            // class.Mode() once classified
	check       string            // the checks' counts
	onCommand   func(name string) // told each command as it starts and "" as it ends (serve's gate: obor status' running line)
}

func newRunner(ctx context.Context, o *Options, dir, logName string) runner {
	return runner{Options: o, ctx: ctx, root: o.Cfg.Root, state: project.StateDir(o.Cfg.Root), dir: dir, logName: logName}
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

// run is exec plus the flake log: the one boundary every check run of the runner passes, so merge,
// serve, nightly and milestone runs all record a red run's failing tests. A run in another dir (after_merge) is no
// check run and records no flakes; its output goes to its log like any other run's.
func (r *runner) run(name string, dir ...string) (output, bool, *halt) {
	o, ok, h := r.exec(name, dir...)
	if h == nil && ok && o.rc != 0 && len(dir) == 0 {
		r.logFlakes(name, false, false, o)
	}
	return o, ok, h
}

// exec runs commands.<name> in dir (the runner's unless given) under its timeout: every command but lane_setup holds
// the check slot. ok false when not configured; a halt when the slot cannot be taken or a signal came. A failed run's
// full output goes to writeLog's log, in any dir (after_merge's included); must logs a red run's tests.
func (r *runner) exec(name string, dir ...string) (output, bool, *halt) {
	c, ok := r.command(name)
	if !ok {
		return output{}, false, nil
	}
	o, h := r.execCommand(c, dir...)
	return o, true, h
}

// execCommand runs c in dir (the runner's unless given) under its timeout, under the name c.Name: every command but
// lane_setup holds the check slot. A halt when the slot cannot be taken or a signal came. A failed command's full
// output goes to writeLog's log, in any dir (after_merge's included); a command that passes writes none and removes an
// earlier log of the same name. exec is this for commands.<name>; retestOnRed is this for commands.retest with its
// {tests} filled.
func (r *runner) execCommand(c config.Command, dir ...string) (output, *halt) {
	environ := append(append([]string(nil), r.Environ...), "OBOR_MERGE=1", run.CPUsEnv(r.Cfg, r.Env))
	if c.Name != "lane_setup" && len(dir) == 0 {
		if h := r.takeSlot(); h != nil {
			return output{}, h
		}
		environ = append(environ, "CHECK_PRIORITY="+r.priority(), "CHECK_SLOT_HELD="+r.slot)
	}
	environ = append(environ, r.env...)
	wd := r.dir
	if len(dir) > 0 {
		wd = dir[0]
	}
	if r.onCommand != nil {
		r.onCommand(c.Name)
	}
	var buf bytes.Buffer
	rc, timedOut := run.Execute(r.ctx, c, wd, nil, environ, &buf, &buf)
	if r.onCommand != nil {
		// The command ended, however it ended (a normal exit, a timeout, the signal that cut ctx): between two
		// commands the gate runs and no command does.
		r.onCommand("")
	}
	if code := r.interrupted(); code != 0 {
		return output{}, interrupt(code)
	}
	if timedOut {
		rc = 124
	}
	o := output{name: c.Name, text: buf.String(), rc: rc, timeout: c.Timeout}
	if o.rc == 0 {
		os.Remove(r.logPath(c.Name))
	} else {
		o.log = r.writeLog(c.Name, buf.Bytes())
	}
	return o, nil
}

// logPath is where a failed command's full output is kept: .obor/state/logs/<logName>-<name>.log.
func (r *runner) logPath(name string) string {
	return filepath.Join(r.state, "logs", r.logName+"-"+name+".log")
}

// writeLog keeps a failed command's full output in logPath; "" when it cannot.
func (r *runner) writeLog(name string, b []byte) string {
	path := r.logPath(name)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil || os.WriteFile(path, b, 0o644) != nil {
		return ""
	}
	return path
}

// priority is the check slot's priority the runner takes: merge unless set (a lane test's worker).
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
	path, err := slots.Acquire(r.ctx, r.priority(), r.PID, r.Out)
	switch {
	case err == nil:
		r.slot = path
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
	for _, alt := range alternatives(opt.Cfg.MergeSummaryRegex()) {
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
	fg := opt.Cfg.MergeFailGrep()
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

// changed lists the files changed in rev (a range, or the merge base: against the worktree) into
// .obor/state/<changedFile> (exported as OBOR_CHANGED_FILES) and classifies the merge.
func (r *runner) changed(task, rev string) *halt {
	path := filepath.Join(r.state, r.changedFile)
	// --no-renames: a rename is a deletion plus an addition, so its source path is classified too.
	out, err := r.git(r.dir, "diff", "--no-renames", "--name-only", rev)
	files := lines(out)
	if err == nil {
		err = os.WriteFile(path, []byte(strings.Join(append(slices.Clone(files), ""), "\n")), 0o644)
	}
	if err != nil {
		return &halt{err: fmt.Errorf("cannot list the changed files into %s", path)}
	}
	r.class = Classify(r.Cfg.ByPath(), files, r.Env.Get("OBOR_FULL") == "1")
	r.mode = r.class.Mode()
	r.env = append(r.env, "OBOR_CHANGED_FILES="+path)
	return nil
}

// laneSetup runs lanes.setup: a timeout is red, another failure a fail (its last 15 lines first).
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

// compile runs commands.compile, the merge slot's one check (build, gofmt, vet, deadcode in obor-kit): a failure
// is red. r.check is its counts, "" when it is not configured (nothing runs).
func (r *runner) compile() *halt {
	o, ok, h := r.must("compile")
	if h != nil || !ok {
		return h
	}
	r.check = "compile " + r.counts(o)
	return nil
}

// movedCheck runs merge.check, a moved gate's check when commands.compile is not configured: a failure is red.
// r.check is "check <counts>".
func (r *runner) movedCheck() *halt {
	o, ok, h := r.must("check")
	if h != nil || !ok {
		return h
	}
	r.check = "check " + r.counts(o)
	return nil
}

// runChecks runs the classes' checks: partial → merge.guard, then each class's checks (a missing command → fail; a
// command two classes name runs once); full → check, then each matched class marked with_full's checks.
// Every check runs: a run in a lane has nothing to reuse.
func (r *runner) runChecks() *halt {
	if r.class.Partial {
		var checks []string
		if !(len(r.class.Names) == 1 && r.class.Names[0] == "neutral") { // all files neutral: the guard only
			for _, name := range r.class.Names {
				cs, ok := classChecks(r.Cfg.ByPath(), name)
				if !ok {
					return &halt{fail: fmt.Sprintf("class %s has no unique run entry in merge.by_path.rules (obor config validate) (%s)",
						name, r.mode)}
				}
				checks = append(checks, cs...)
			}
		}
		var counts []string
		seen := map[string]bool{}
		for _, name := range append([]string{r.Cfg.MergeGuard()}, checks...) {
			if seen[name] {
				continue
			}
			seen[name] = true
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
	var counts []string
	if o, ok, h := r.must("check"); h != nil {
		return h
	} else if ok {
		counts = append(counts, r.counts(o))
	}
	// A matched class marked with_full runs its checks after check, config order, a command named twice once (a
	// full lane with release or web files still covers them).
	seen := map[string]bool{"check": true}
	for _, name := range r.class.Names {
		for _, check := range withFullChecks(r.Cfg.ByPath(), name) {
			if seen[check] {
				continue
			}
			seen[check] = true
			o, ok, h := r.must(check)
			if h != nil {
				return h
			}
			if !ok {
				return &halt{fail: fmt.Sprintf("commands.%s not configured (%s)", check, r.mode)}
			}
			counts = append(counts, check+" "+r.counts(o))
		}
	}
	if len(counts) > 0 {
		r.check = strings.Join(counts, ", ")
	}
	return nil
}

// must runs commands.<name>; a failure is red. ok false: not configured. Every red run's failing tests go to the flake
// log. A lane test or the nightly reruns a red's failing tests once (retested, retestOnRed). A red whose only
// failures are timeouts is red: a retry does not save it.
func (r *runner) must(name string) (output, bool, *halt) {
	o, ok, h, _ := r.retested(name)
	if h != nil || !ok {
		return o, ok, h
	}
	if o.rc == 0 {
		return o, ok, nil
	}
	return o, ok, &halt{red: &o}
}

// retested is must before its red halt: commands.<name> run once, and a red whose failing tests and packages it names
// rerun once (retestOnRed) when the runner retests — green then is the run's, saved the failing tests it reran, 0 when
// none was saved (a red that names no test, no commands.retest, a retest that failed). ok false: not configured. Every
// red run's failing tests go to the flake log, retried and passed when the retest saved it (the
// nightly's GREEN line names the rerun).
func (r *runner) retested(name string) (o output, ok bool, h *halt, saved int) {
	o, ok, h = r.exec(name)
	if h != nil || !ok || o.rc == 0 {
		return o, ok, h, 0
	}
	if r.retest {
		if out, green, ran, tests := r.retestOnRed(o); ran {
			r.logFlakes(name, true, green, o)
			if green {
				return out, true, nil, tests
			}
			return o, true, nil, 0
		}
	}
	r.logFlakes(name, false, false, o)
	return o, true, nil, 0
}

// retestOnRed runs a red run's failures once more (a lane test and the nightly, one retest, never
// more): the tests and packages the run named, commands.retest in the same dir at the same commit — its {tests} the
// failing tests joined by merge.retest_join, OBOR_FAILED_PKGS the failing packages — the runner's CPU budget, inside
// the slot it already holds. green: the retest passed and ran at least one test (a run that tested nothing, "0 pass,
// 0 fail", is not green); ran false: none ran (no name or package, no commands.retest), so the red stands as
// before; n: how many failing tests it reran.
func (r *runner) retestOnRed(red output) (out output, green, ran bool, n int) {
	_, tests, pkgs := r.flakeParse(red)
	if len(tests) == 0 || len(pkgs) == 0 {
		return output{}, false, false, 0
	}
	c, ok := r.command("retest")
	if !ok {
		return output{}, false, false, 0
	}
	c = r.retestCommand(c, tests)
	env := len(r.env)
	r.env = append(r.env, "OBOR_FAILED_PKGS="+strings.Join(pkgs, " "))
	o, h := r.execCommand(c)
	r.env = r.env[:env]
	if h != nil {
		return output{}, false, false, 0
	}
	return o, o.rc == 0 && ranTests(r.counts(o)), true, len(tests)
}

// ranTests is whether a retest's counts (merge.summary_regex) show at least one test: the retry's own "go test: 0
// pass, 0 fail" is a run that tested nothing (an empty package list, a -run that matched nothing) and cannot turn the
// original red green. counts' "rc=<n>" (no summary line) says nothing about tests, so rc alone decides.
func ranTests(counts string) bool {
	if strings.HasPrefix(counts, "rc=") {
		return true
	}
	nums := number.FindAllString(counts, -1)
	for _, n := range nums {
		if v, _ := strconv.Atoi(n); v != 0 {
			return true
		}
	}
	return len(nums) == 0
}

// retestPlaceholder is commands.retest's placeholder for the failing tests, joined by merge.retest_join.
const retestPlaceholder = "{tests}"

// retestCommand is c with retestPlaceholder filled: the failing tests joined by merge.retest_join (default "|"), in
// the order the run named them. A command with no placeholder runs as it is.
func (r *runner) retestCommand(c config.Command, tests []string) config.Command {
	c.Cmd = strings.ReplaceAll(c.Cmd, retestPlaceholder, strings.Join(tests, r.Cfg.RetestJoin()))
	return c
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
