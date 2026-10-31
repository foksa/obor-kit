package checks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/spec"
	"obor/internal/store"
)

func ltGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@x"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ltFixture (T-470: the lane table's tests, from package lanes): proj with a committed config whose lanes are the given names (type opus) under proj-wt; each lane a
// worktree on task/<id> when its id is set, else detached at main. Returns the project.
func ltFixture(t *testing.T, extra string, lanes map[string]string) *LaneTable {
	t.Helper()
	base := project.Physical(t.TempDir())
	root := filepath.Join(base, "proj")
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	var ls []string
	for l := range lanes {
		ls = append(ls, `"`+l+`":"opus"`)
	}
	cfg := `{"version":1,"prefix":"pj","lanes":{` + strings.Join(ls, ",") + `}` + extra + `}`
	os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".task-done.md\n"), 0o644)
	ltGit(t, base, "-c", "init.defaultBranch=main", "init", "-q", root)
	ltGit(t, root, "add", "-A")
	ltGit(t, root, "commit", "-q", "-m", "init")
	for l, id := range lanes {
		wt := filepath.Join(base, "proj-wt", l)
		if id == "" {
			ltGit(t, root, "worktree", "add", "-q", "--detach", wt, "main")
		} else {
			ltGit(t, root, "worktree", "add", "-q", "-b", "task/"+id, wt, "main")
		}
	}
	c, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return NewLaneTable(c)
}

// submit puts task on lane's row and, done, its task_done age ago, as obor serve projects its log (T-439); the row's
// head is a fork point the lane has moved from (a lane at it is in flight).
func ltSubmit(t *testing.T, root, lane, task string, done bool, age time.Duration) {
	t.Helper()
	db, err := store.OpenRW(store.File(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := store.ReadLanes(db)
	if err != nil {
		t.Fatal(err)
	}
	row := store.LaneRow{Lane: lane, Task: task, Branch: "task/" + task, Head: "fork-point"}
	if done {
		row.Finished = time.Now().Add(-age).Unix()
	}
	rows = append(slices.DeleteFunc(rows, func(r store.LaneRow) bool { return r.Lane == lane }), row)
	if err := store.WriteLanes(db, rows); err != nil {
		t.Fatal(err)
	}
}

func ltCommit(t *testing.T, dir string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o644)
	ltGit(t, dir, "add", "work.txt")
	ltGit(t, dir, "commit", "-q", "-m", "work")
}

func TestLaneStates(t *testing.T) {
	p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"O-01": "T-01", "O-02": "T-02", "O-03": "T-03",
		"O-04": "T-04", "O-05": "T-05", "O-06": "T-06"})
	dir := func(l string) string { return filepath.Join(filepath.Dir(p.Root), "proj-wt", l) }
	os.WriteFile(filepath.Join(dir("O-02"), "scratch.txt"), []byte("x"), 0o644)
	ltSubmit(t, p.Root, "O-03", "T-03", true, 0) // merged: free, whatever the log says
	ltCommit(t, dir("O-04"))
	ltSubmit(t, p.Root, "O-04", "T-04", true, 0)
	ltCommit(t, dir("O-05"))
	ltSubmit(t, p.Root, "O-05", "T-05", false, 0) // dispatched, no task_done yet
	// T-196: a dirty lane is free only when its task has merge evidence ("T-06:" commit reachable from HEAD).
	os.WriteFile(filepath.Join(dir("O-06"), "scratch.txt"), []byte("x"), 0o644)
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "T-06: merged work")
	got := map[string]string{}
	for _, s := range p.States() {
		got[s.Lane] = s.State + " " + s.Task + " " + s.Warning
	}
	want := map[string]string{"O-01": "free T-01 ", "O-02": "working T-02 ", "O-03": "free T-03 ",
		"O-04": "done T-04 ", "O-05": "working T-05 ", "O-06": "dirty-free T-06 "}
	for l, w := range want {
		if got[l] != w {
			t.Errorf("%s: %q, want %q", l, got[l], w)
		}
	}
	if p.MergeEvidence("T-50") {
		t.Error("no Merge T-50: commit yet, but evidence")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-50: done")
	if !p.MergeEvidence("T-50") {
		t.Error("a Merge T-50: commit is evidence")
	}
	// RV-38 #11: a log git cannot read is an error from MergeEvidenceBy, not "no evidence".
	if ok, err := p.MergeEvidenceBy("T-50", func(...string) (string, error) { return "", errors.New("git I/O") }); ok || err == nil {
		t.Errorf("MergeEvidenceBy with git failing = %v, %v; want false and the error", ok, err)
	}
}

// T-547, T-550: a "<id>:" subject is evidence whatever files it changes, except a commit that changes only files in the
// legacy spec dir (paths.tasks, else docs/tasks): an upgrade's old "<id>: spec" commit is no evidence (T-290's
// exclusion, kept for the deprecation period). "Merge <id>:" stays evidence.
func TestMergeEvidenceSubjectRule(t *testing.T) {
	add := func(p *LaneTable, file, subj string) {
		path := filepath.Join(p.Root, file)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("x\n"), 0o644)
		ltGit(t, p.Root, "add", file)
		ltGit(t, p.Root, "commit", "-q", "-m", subj)
	}
	p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	if p.MergeEvidence("T-09") {
		t.Fatal("precondition: a fresh fixture has no evidence for T-09")
	}
	// The default legacy path: a "<id>:" commit touching only docs/tasks is an old spec, not evidence.
	add(p, "docs/tasks/T-09.md", "T-09: spec")
	if p.MergeEvidence("T-09") {
		t.Error("a T-09: commit touching only docs/tasks (the default legacy path) is no evidence")
	}
	// A "<id>:" commit touching a file outside the legacy path is evidence (the squash case).
	add(p, "src/a.go", "T-09: fix")
	if !p.MergeEvidence("T-09") {
		t.Error("a T-09: commit touching src/a.go is evidence (squash)")
	}
	// A configured paths.tasks names the excluded dir: only specs/T-09.md there is no evidence.
	r := ltFixture(t, `,"lanes_dir":"../proj-wt","paths":{"tasks":"specs"}`, nil)
	add(r, "specs/T-09.md", "T-09: spec")
	if r.MergeEvidence("T-09") {
		t.Error("with paths.tasks=specs, a T-09: commit touching only specs/T-09.md is no evidence")
	}
	add(r, "docs/tasks/T-09.md", "T-09: extra spec")
	if !r.MergeEvidence("T-09") {
		t.Error("with paths.tasks=specs, a T-09: commit touching docs/tasks is evidence (outside specs)")
	}
	// A subject without the "<id>:" prefix is no evidence, even when it names the task.
	q := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	add(q, "work.txt", "work for T-09: later")
	if q.MergeEvidence("T-09") {
		t.Error("a subject that does not start with T-09: is no evidence")
	}
	// An empty "<id>:" commit is evidence too (the other packages' fixtures dispatch lanes with such a commit).
	e := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	ltGit(t, e.Root, "commit", "-q", "--allow-empty", "-m", "T-09: merged")
	if !e.MergeEvidence("T-09") {
		t.Error("an empty T-09: commit is evidence")
	}
	// "Merge T-09:" is evidence (control: the gate's merge subject).
	m := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	ltGit(t, m.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-09: done")
	if !m.MergeEvidence("T-09") {
		t.Error(`a "Merge T-09:" commit is evidence`)
	}
}

// T-550 (RV-57 #1): an old spec commit on main is no merge evidence: a lane whose work lives only there is not merged,
// for the default legacy path and a configured paths.tasks.
func TestLaneWithLegacySpecOnMainIsNotMerged(t *testing.T) {
	for _, c := range []struct {
		name, extra, file string
	}{
		{"default legacy path", `,"lanes_dir":"../proj-wt"`, "docs/tasks/T-01.md"},
		{"configured paths.tasks", `,"lanes_dir":"../proj-wt","paths":{"tasks":"specs"}`, "specs/T-01.md"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := ltFixture(t, c.extra, map[string]string{"O-01": "T-01"})
			dir := filepath.Join(filepath.Dir(p.Root), "proj-wt", "O-01")
			ltCommit(t, dir)
			path := filepath.Join(p.Root, c.file)
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, []byte("spec\n"), 0o644)
			ltGit(t, p.Root, "add", c.file)
			ltGit(t, p.Root, "commit", "-q", "-m", "T-01: spec")
			// Preconditions: the spec is main's newest commit and the lane's work is not on main.
			if subj, _ := project.Git(p.Root, "log", "-1", "--format=%s"); strings.TrimSpace(subj) != "T-01: spec" {
				t.Fatalf("precondition: main's newest subject %q", strings.TrimSpace(subj))
			}
			if _, err := project.Git(p.Root, "merge-base", "--is-ancestor", "task/T-01", "main"); err == nil {
				t.Fatal("precondition: the lane's work is not on main")
			}
			if p.MergeEvidence("T-01") {
				t.Error("an old T-01: spec commit on main is no merge evidence")
			}
			if s := stateOf(p, "O-01"); s.State != "working" {
				t.Errorf("lane state = %q, want working (its work lives only in the lane)", s.State)
			}
			// Control: the squash rule still finds the task's work on main.
			ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "T-01: work")
			if !p.MergeEvidence("T-01") {
				t.Error("a T-01: work commit on main is evidence")
			}
		})
	}
}

func TestMissingLane(t *testing.T) {
	p := ltFixture(t, `,"lanes":{"X-01":"sonnet"}`, map[string]string{})
	s := p.States()
	if len(s) != 1 || s[0].State != "missing" || s[0].Branch != "" || s[0].Type != "sonnet" {
		t.Errorf("States = %+v", s)
	}
}

// T-224: spec.Match with the patterns GlobMatch's tests used, with what it returned.
func TestSpecMatchGlobs(t *testing.T) {
	for _, c := range []struct {
		path, glob string
		want       bool // GlobMatch's answer
	}{
		{"src/ui/a/b.ts", "src/ui/**", true}, {"a/b", "a/**/b", true}, {"a/x/y/b", "a/**/b", true},
		{"x", "**/x", true}, {"d/x", "**/x", true}, {"src/net/x", "src/ui/**", false}, {"a.go", "?.go", true},
		{"b.go", "[ab].go", true}, {"c.go", "[!ab].go", true}, {"a.go", "[!ab].go", false},
		{"a/b/c", "a/*", true}, {"ab", "a/**/b", false}, {"src/ui", "src/ui/**", false}, {"axb", "a.b", false},
	} {
		if got := spec.Match(c.path, c.glob); got != c.want {
			t.Errorf("spec.Match(%q, %q) = %v, GlobMatch said %v", c.path, c.glob, got, c.want)
		}
	}
}

// A lead commit whose subject contains the id is no merge evidence; "Merge <id>:" is.
func TestMergeEvidenceSubject(t *testing.T) {
	p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"O-01": ""})
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "T-05 widened (lead: spec)")
	if NewLaneTable(p.Cfg).MergeEvidence("T-05") {
		t.Fatal("a lead commit mentioning T-05 counts as merged")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-05: work")
	if !NewLaneTable(p.Cfg).MergeEvidence("T-05") {
		t.Error("Merge T-05: commit → merged")
	}
}

// stateOf is lane's row of p's table (the zero row when it has none).
func stateOf(p *LaneTable, lane string) LaneStatus {
	for _, s := range p.States() {
		if s.Lane == lane {
			return s
		}
	}
	return LaneStatus{}
}
