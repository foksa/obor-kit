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
// scripts/release's own rerun (T-717) and by a lane test's one retest (commands.retest, T-964).
type Flake struct {
	Time          int64  `json:"time"`            // epoch seconds
	Test          string `json:"test"`            // the fail_grep line's name: the test, else the package (merge.flake_regex)
	Check         string `json:"check"`           // commands.<name>
	Task          string `json:"task"`            // the run's task (nightly, milestone for those)
	SHA           string `json:"sha"`             // the commit the checks ran on
	Retried       bool   `json:"retried"`         // the run was rerun (scripts/release's)
	PassedOnRetry bool   `json:"passed_on_retry"` // and the test passed then
}

// flakeWindow is how far back obor flakes looks.
const flakeWindow = 7 * 24 * time.Hour

// flakeRegexp compiles merge.flake_regex: the failed-test line and the failed-package line (T-1047). A nil entry is
// no regex configured, or one that does not compile (validate refuses that): that line is not parsed.
func (opt *Options) flakeRegexp() (test, pkg *regexp.Regexp) {
	if pair := opt.Cfg.Data.Merge.FlakeRegex; len(pair) == 2 {
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

// flakeParse reads outs' fail_grep lines with merge.flake_regex (T-1047). names is the flake log's list, in line order:
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
// is a warning line, never the check's result. retried: commands.retest ran the failures once more (a lane test, T-964);
// passed: and they passed there.
func (r *runner) logFlakes(name string, retried, passed bool, outs ...output) {
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

// sha is the commit the checks ran on (a lane test's candidate, T-961; a nightly's head); "" when git cannot name
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

// Flaky is a test obor flakes prints: n log lines in the window, the tasks they name and
// the newest line's time.
type Flaky struct {
	Test  string
	N     int
	Tasks []string
	Last  int64
}

// flakeRule is T-399's rule for the reds that are not flake evidence: the red task's own diff touched the file that
// defines the test, or the red came before the merge of a task whose spec title names the test (a fix ended it). Its
// reads (git, the specs, the merges) are lazy and remembered per rule, so one `obor flakes` round
// pays for each test and sha once.
type flakeRule struct {
	cfg    *config.Config
	loaded bool                // the specs and the merges are read
	specs  []flakeSpec         // the task specs' titles
	merged map[string]int64    // task → its newest merge's time (fact merged)
	diffs  map[string][]string // "<task>\0<sha>" → the task diff's files (nil: git cannot tell)
	defs   map[string][]string // "<test>\0<sha>" → the files defining the test (empty: a package name)
}

// flakeSpec is one task spec: its task and the title after the dash of its "# <task> — <title>" heading.
type flakeSpec struct{ task, title string }

func newFlakeRule(cfg *config.Config) *flakeRule {
	return &flakeRule{cfg: cfg, diffs: map[string][]string{}, defs: map[string][]string{}}
}

// explained reports whether e is a red the project explains, so it is no flake evidence. A passed_on_retry line is
// evidence on its own; a line git, the specs or the merges cannot place counts as before.
func (r *flakeRule) explained(e Flake) bool {
	if e.PassedOnRetry || e.SHA == "" || e.Task == "" {
		return false
	}
	return r.beforeFix(e.Test, e.Time) || r.touched(e.Task, e.SHA, e.Test)
}

// touched reports whether the red task's diff touched a file that defines the test at the red's sha.
func (r *flakeRule) touched(task, sha, test string) bool {
	files := r.diff(task, sha)
	if files == nil {
		return false
	}
	for _, f := range r.defines(test, sha) {
		if slices.Contains(files, f) {
			return true
		}
	}
	return false
}

// beforeFix reports whether a red at at came before the merge of a task whose spec title names test.
func (r *flakeRule) beforeFix(test string, at int64) bool {
	r.load()
	for _, s := range r.specs {
		if m, ok := r.merged[s.task]; ok && at < m && mentions(s.title, test) {
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

// diff is the files of the red task's change: from the merge-base of the target branch and sha to sha, so a lane's
// commit and the gate's merge of it read as the task's work. When sha is already in the branch (a merge's target: the
// old merge runs the checks before its merge commit exists), it is the task's merge commit on the branch, first parent
// to it, as "Merge <task>: …". Nil: git cannot tell, so the red counts.
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
	branch := r.cfg.Data.Branches.Main // the target the tasks merge into
	if branch == "" {
		return nil // no target: git cannot tell, the red counts
	}
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

// load reads the task specs' titles and the tasks' merge times (fact merged, T-525), both the store's, read-only; no
// store leaves them empty, so only the diff rule applies.
func (r *flakeRule) load() {
	if r.loaded {
		return
	}
	r.loaded = true
	r.merged = map[string]int64{}
	if merges, err := event.ReadMerges(r.cfg.Root); err == nil {
		for _, m := range merges {
			if at := m.At.Unix(); at > r.merged[m.Task] {
				r.merged[m.Task] = at
			}
		}
	}
	// The store's specs (T-440, the only source since T-521).
	if rows, err := store.ReadSpecsAt(r.cfg.Root); err == nil {
		for _, row := range rows {
			r.specs = append(r.specs, flakeSpec{task: row.Task, title: row.Spec.Title})
		}
	}
}

// FlakyTests is the flaky tests among entries as of now, sorted by name: a test with a passed_on_retry line in the
// window, or red lines on two or more tasks in it — a red the project explains does not count (T-399, flakeRule).
func FlakyTests(cfg *config.Config, entries []Flake, now time.Time) []Flaky {
	type acc struct {
		n     int
		tasks []string
		last  int64
		saved bool
	}
	cutoff := now.Add(-flakeWindow).Unix()
	rule := newFlakeRule(cfg)
	byTest := map[string]*acc{}
	for _, e := range entries {
		if e.Test == "" || e.Time < cutoff || rule.explained(e) {
			continue
		}
		a := byTest[e.Test]
		if a == nil {
			a = &acc{}
			byTest[e.Test] = a
		}
		a.n++
		a.last = max(a.last, e.Time)
		a.saved = a.saved || e.PassedOnRetry
		if e.Task != "" && !slices.Contains(a.tasks, e.Task) {
			a.tasks = append(a.tasks, e.Task)
		}
	}
	var out []Flaky
	for test, a := range byTest {
		if !a.saved && len(a.tasks) < 2 {
			continue
		}
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
