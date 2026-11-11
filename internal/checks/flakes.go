package checks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/app/event"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// A Flake is one .obor/state/flakes.jsonl line: a check run's failing test. Retried and PassedOnRetry are written by
// scripts/release's own rerun and by a lane test's one retest (commands.retest).
type Flake struct {
	Time          int64  `json:"time"`            // epoch seconds
	Test          string `json:"test"`            // the fail_grep line's name: the test, else the package (merge.flake_regex)
	Check         string `json:"check"`           // commands.<name>
	Task          string `json:"task"`            // the run's task (nightly, milestone for those)
	SHA           string `json:"sha"`             // the commit the checks ran on
	Retried       bool   `json:"retried"`         // the run was rerun (scripts/release's)
	PassedOnRetry bool   `json:"passed_on_retry"` // and the test passed then
}

// flakeWindow is how far back obor flakes looks: the window the nightly sweep prunes flakes.jsonl with.
const flakeWindow = project.FlakeKeep

// raceCheck is the nightly's race command (nightly.also_run's race, commands.race): its failures are the race detector's
// reports, which have their own WARN line, not flakes, so a race run logs no flake lines and the reader ignores the ones
// older runs logged.
const raceCheck = "race"

// flakeRegexp compiles merge.flake_regex: the failed-test line and the failed-package line. A nil entry is
// no regex configured, or one that does not compile (validate refuses that): that line is not parsed.
func (opt *Options) flakeRegexp() (test, pkg *regexp.Regexp) {
	if pair := opt.Cfg.MergeFlakeRegex(); len(pair) == 2 {
		test, _ = regexp.Compile(pair[0])
		pkg, _ = regexp.Compile(pair[1])
	}
	return test, pkg
}

// flakeGroup is re's named group name in l; "" when re is nil, l does not match, or the group is empty.
func flakeGroup(re *regexp.Regexp, name, l string) string {
	if re == nil {
		return ""
	}
	m := re.FindStringSubmatch(l)
	if i := re.SubexpIndex(name); m != nil && i >= 0 {
		return m[i]
	}
	return ""
}

// flakeParse reads outs' fail_grep lines with merge.flake_regex. names is the flake log's list, in line order:
// a failed-test line's test, and a failed-package line's package unless a test line since the previous package line
// covers it (a build failure or a panic has no test line of its own). tests is every test line's name, pkgs every
// package line's package — the retest reruns the tests in all of them. One entry per name, in order; no regex: nothing,
// no error.
func (opt *Options) flakeParse(outs ...output) (names, tests, pkgs []string) {
	testRe, pkgRe := opt.flakeRegexp()
	if testRe == nil && pkgRe == nil {
		return nil, nil, nil
	}
	type list struct {
		items []string
		seen  map[string]bool
	}
	var logNames, testNames, pkgNames list
	logNames.seen, testNames.seen, pkgNames.seen = map[string]bool{}, map[string]bool{}, map[string]bool{}
	add := func(l *list, n string) {
		if n == "" || l.seen[n] {
			return
		}
		l.seen[n] = true
		l.items = append(l.items, n)
	}
	for _, o := range outs {
		covered := false // a test line since the last package line
		for _, l := range opt.failing(o) {
			if t := flakeGroup(testRe, "test", l); t != "" {
				add(&logNames, t)
				add(&testNames, t)
				covered = true
				continue
			}
			if p := flakeGroup(pkgRe, "package", l); p != "" {
				if !covered {
					add(&logNames, p)
				}
				add(&pkgNames, p)
				covered = false
			}
		}
	}
	return logNames.items, testNames.items, pkgNames.items
}

// logFlakes appends the failing tests of outs (one logical record: a red run) to the project's flake log; a write error
// is a warning line, never the check's result. retried: commands.retest ran the failures once more (a lane test);
// passed: and they passed there. The race check logs nothing: its failure is a race report, not flake evidence.
func (r *runner) logFlakes(name string, retried, passed bool, outs ...output) {
	if name == raceCheck {
		return
	}
	names, _, _ := r.flakeParse(outs...)
	if len(names) == 0 {
		return
	}
	at, sha := time.Now().Unix(), r.sha()
	entries := make([]Flake, 0, len(names))
	for _, test := range names {
		entries = append(entries, Flake{Time: at, Test: test, Check: name, Task: r.logName, SHA: sha,
			Retried: retried, PassedOnRetry: retried && passed})
	}
	if err := appendFlakes(r.state, entries); err != nil {
		fmt.Fprintf(r.Out, "warning: flake log: %v\n", err)
	}
}

// sha is the commit the checks ran on (a lane test's candidate; a nightly's head); "" when git cannot name
// one.
func (r *runner) sha() string {
	out, err := r.git(r.dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return out
}

// appendFlakes appends entries to state's flakes.jsonl, one JSON line each (the state dir is created when missing).
func appendFlakes(state string, entries []Flake) error {
	if err := os.MkdirAll(state, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(state, "flakes.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(append(b, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// Flakes reads the project's flake log; a missing log is empty. A line that does not parse (a crash left half of it)
// is skipped.
func Flakes(root string) ([]Flake, error) {
	b, err := os.ReadFile(filepath.Join(project.StateDir(root), "flakes.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Flake
	for _, l := range strings.Split(string(b), "\n") {
		var f Flake
		if strings.TrimSpace(l) == "" || json.Unmarshal([]byte(l), &f) != nil {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// Flaky is a test obor flakes prints: n passed-on-retry lines in the window, the tasks they name and
// the newest line's time.
type Flaky struct {
	Test  string
	N     int
	Tasks []string
	Last  int64
}

// flakeRule is the rule for a passed-on-retry line a fix ended: a merge after the line whose spec title names the test,
// or one whose diff touched a file that defines it and whose commit the line's sha does not contain. Its reads (git, the
// specs, the merges) are lazy and remembered per rule, so one `obor flakes` round pays for each task, test and sha once.
type flakeRule struct {
	cfg      *config.Config
	loaded   bool                // the specs and the merges are read
	specs    []flakeSpec         // the task specs' titles
	merged   map[string]flakeFix // task → its newest merge (fact merged)
	diffs    map[string][]string // "<task>\0<sha>" → the task diff's files (nil: git cannot tell)
	defs     map[string][]string // "<test>\0<sha>" → the files defining the test (empty: a package name)
	ancestry map[string]bool     // "<fix>\0<sha>" → sha's history holds fix
}

// flakeFix is a task's newest merge: its time and the merge's head, the sha its diff reads from.
type flakeFix struct {
	at  int64
	sha string
}

// flakeSpec is one task spec: its task and the title after the dash of its "# <task> — <title>" heading.
type flakeSpec struct{ task, title string }

func newFlakeRule(cfg *config.Config) *flakeRule {
	return &flakeRule{cfg: cfg, diffs: map[string][]string{}, defs: map[string][]string{},
		ancestry: map[string]bool{}}
}

// explained reports whether a merge ended the test's flakes: a task merged after the line whose spec title names the
// test, or one whose diff touched a file that defines it and whose commit the line's sha does not contain. A line git,
// the specs or the merges cannot place counts as no fix.
func (r *flakeRule) explained(e Flake) bool {
	if e.SHA == "" || e.Task == "" {
		return false
	}
	return r.beforeFix(e.Test, e.Time) || r.fixedSince(e.Test, e.SHA)
}

// fixedSince reports whether a task merged a change to a file that defines the test at sha, and that sha does not
// contain the merge: gates run on older bases, so a merge newer than the base can fix a test a line older in time
// still flaked on.
func (r *flakeRule) fixedSince(test, sha string) bool {
	r.load()
	defs := r.defines(test, sha)
	if len(defs) == 0 {
		return false
	}
	for task, m := range r.merged {
		if r.touches(task, m.sha, defs) && !r.contains(m.sha, sha) {
			return true
		}
	}
	return false
}

// contains reports whether sha's history holds fix (git merge-base --is-ancestor), cached per pair. A pair git cannot
// answer counts as holding it: a fix this rule cannot place never ends a flake.
func (r *flakeRule) contains(fix, sha string) bool {
	key := fix + "\x00" + sha
	if ok, seen := r.ancestry[key]; seen {
		return ok
	}
	_, err := project.Git(r.cfg.Root, "merge-base", "--is-ancestor", fix, sha)
	yes, unknown := exit1(err)
	ok := yes || unknown != nil
	r.ancestry[key] = ok
	return ok
}

// touches reports whether the task's merged diff changed one of files.
func (r *flakeRule) touches(task, sha string, files []string) bool {
	changed := r.diff(task, sha)
	for _, f := range files {
		if slices.Contains(changed, f) {
			return true
		}
	}
	return false
}

// beforeFix reports whether a line at at came before the merge of a task whose spec title names test.
func (r *flakeRule) beforeFix(test string, at int64) bool {
	r.load()
	for _, s := range r.specs {
		if m, ok := r.merged[s.task]; ok && at < m.at && mentions(s.title, test) {
			return true
		}
	}
	return false
}

// mentions reports whether a spec title names the test: the name with a non-identifier character (or the end of the
// title) on both sides, so a TestA title does not name TestAB.
func mentions(title, test string) bool {
	for i := 0; i+len(test) <= len(title); i++ {
		if title[i:i+len(test)] != test {
			continue
		}
		left := i == 0 || !ident(title[i-1])
		right := i+len(test) == len(title) || !ident(title[i+len(test)])
		if left && right {
			return true
		}
	}
	return false
}

// ident reports whether c can be part of a Go identifier.
func ident(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// diff is the files of the task's change: from the merge-base of the target branch and sha to sha, so a lane's commit
// and the gate's merge of it read as the task's work. When sha is already in the branch (a merge's head: the old merge
// runs the checks before its merge commit exists), it is the task's merge commit on the branch, first parent to it, as
// "Merge <task>: …". Nil: git cannot tell, so no fix is seen.
func (r *flakeRule) diff(task, sha string) []string {
	key := task + "\x00" + sha
	if files, ok := r.diffs[key]; ok {
		return files
	}
	files := r.taskDiff(task, sha)
	r.diffs[key] = files
	return files
}

func (r *flakeRule) taskDiff(task, sha string) []string {
	branch := r.cfg.MainBranch() // the target the tasks merge into
	base, err := project.Git(r.cfg.Root, "merge-base", branch, sha)
	if err != nil {
		return nil
	}
	if base = strings.TrimSpace(base); base != sha {
		return r.changed(base, sha)
	}
	out, err := project.Git(r.cfg.Root, "log", "-1", "--format=%H", "--merges", "--fixed-strings",
		"--grep=Merge "+task+":", branch)
	if err != nil {
		return nil
	}
	merge := strings.TrimSpace(out)
	if merge == "" {
		return nil
	}
	return r.changed(merge+"^1", merge)
}

// changed is the files git diff --name-only names for a..b (nil: git cannot tell).
func (r *flakeRule) changed(a, b string) []string {
	out, err := project.Git(r.cfg.Root, "diff", "--name-only", a, b)
	if err != nil {
		return nil
	}
	return nonBlank(out)
}

// defines is the files at sha that hold "func <test>(" (empty: a package-level name, or git cannot tell).
func (r *flakeRule) defines(test, sha string) []string {
	key := test + "\x00" + sha
	if files, ok := r.defs[key]; ok {
		return files
	}
	var files []string
	if out, err := project.Git(r.cfg.Root, "grep", "-l", "-F", "func "+test+"(", sha); err == nil {
		for _, l := range nonBlank(out) {
			files = append(files, strings.TrimPrefix(l, sha+":"))
		}
	}
	r.defs[key] = files
	return files
}

// nonBlank is s's lines, trimmed, without the empty ones.
func nonBlank(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// load reads the task specs' titles and the tasks' newest merge (fact merged), both the store's, read-only; no
// store leaves them empty, so no fix is seen.
func (r *flakeRule) load() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.merged = map[string]flakeFix{}
	if merges, err := event.ReadMerges(r.cfg.Root); err == nil {
		for _, m := range merges {
			if at := m.At.Unix(); at > r.merged[m.Task].at {
				r.merged[m.Task] = flakeFix{at: at, sha: m.SHA}
			}
		}
	}
	// The store's specs (the only source).
	if rows, err := store.ReadSpecsAt(r.cfg.Root); err == nil {
		for _, row := range rows {
			r.specs = append(r.specs, flakeSpec{task: row.Task, title: row.Spec.Title})
		}
	}
}

// FlakyTests is the flaky tests among entries as of now, sorted by name: a test with a passed_on_retry line in the
// window that no fix ended (flakeRule). Red lines are no flake evidence on their own, and the race check's lines are
// none at all.
func FlakyTests(cfg *config.Config, entries []Flake, now time.Time) []Flaky {
	type acc struct {
		n     int
		tasks []string
		last  int64
	}
	cutoff := now.Add(-flakeWindow).Unix()
	rule := newFlakeRule(cfg)
	byTest := map[string]*acc{}
	for _, e := range entries {
		if e.Test == "" || e.Check == raceCheck || !e.PassedOnRetry || e.Time < cutoff || rule.explained(e) {
			continue
		}
		a := byTest[e.Test]
		if a == nil {
			a = &acc{}
			byTest[e.Test] = a
		}
		a.n++
		a.last = max(a.last, e.Time)
		if e.Task != "" && !slices.Contains(a.tasks, e.Task) {
			a.tasks = append(a.tasks, e.Task)
		}
	}
	var out []Flaky
	for test, a := range byTest {
		out = append(out, Flaky{Test: test, N: a.n, Tasks: a.tasks, Last: a.last})
	}
	slices.SortFunc(out, func(a, b Flaky) int { return strings.Compare(a.Test, b.Test) })
	return out
}

// Line is obor flakes' line: FLAKY <test> ×<n> (7d) tasks: T-a,T-b last: <time>.
func (f Flaky) Line() string {
	return fmt.Sprintf("FLAKY %s ×%d (7d) tasks: %s last: %s", f.Test, f.N, strings.Join(f.Tasks, ","),
		time.Unix(f.Last, 0).Format(time.RFC3339))
}
