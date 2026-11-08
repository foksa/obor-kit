package checks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"obor/internal/app/port"
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

// ltFixture (T-470: the lane table's tests, from package lanes): proj with a committed config whose lanes count is the
// given names' (L-01 … L-NN, T-765) under proj-wt; each lane a worktree on task/<id> when its id is set, else detached
// at main. Returns the project.
func ltFixture(t *testing.T, extra string, lanes map[string]string) *LaneTable {
	t.Helper()
	base := project.Physical(t.TempDir())
	root := filepath.Join(base, "proj")
	os.MkdirAll(filepath.Join(root, ".obor"), 0o755)
	cfg := `{"version":1,"prefix":"pj","lanes":` + strconv.Itoa(len(lanes)) + extra + `}`
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
	return NewLaneTable(context.Background(), c)
}

// ltSubmit puts task on lane's row and, done, its task_done age ago with the lane's HEAD as its submitted head
// (T-1015), and landed, its landed fact, as obor serve projects its log (T-439, T-1012).
func ltSubmit(t *testing.T, root, lane, task string, done, landed bool, age time.Duration) {
	t.Helper()
	head := ""
	if done {
		out, err := project.Git(filepath.Join(filepath.Dir(root), "proj-wt", lane), "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		head = strings.TrimSpace(out)
	}
	ltRow(t, root, lane, task, head, done, landed, age)
}

// ltRow puts task on lane's row with head as its submitted head ("": none recorded), as ltSubmit.
func ltRow(t *testing.T, root, lane, task, head string, done, landed bool, age time.Duration) {
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
	row := store.LaneRow{Lane: lane, Task: task, Branch: "task/" + task, Head: head, Landed: landed}
	if done {
		row.Finished = time.Now().Add(-age).Unix()
	}
	rows = append(slices.DeleteFunc(rows, func(r store.LaneRow) bool { return r.Lane == lane }), row)
	if err := store.WriteLanes(db, rows); err != nil {
		t.Fatal(err)
	}
}

// TestLaneStatesCanceled (T-873): a canceled ctx ends the lane table's git reads: a git that hangs is killed, States
// returns at once, and the lane is unknown (never free), with the cancellation as its reason.
func TestLaneStatesCanceled(t *testing.T) {
	p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"L-01": "T-01"})
	bin := t.TempDir()
	started := filepath.Join(bin, "started")
	script := "#!/bin/sh\ntouch " + strconv.Quote(started) + "\nsleep 3\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan []port.Lane, 1)
	go func() { done <- NewLaneTable(ctx, p.Cfg).States() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lane table never started git: the fixture's shim was not used")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	var states []port.Lane
	select {
	case states = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("States outlived the canceled ctx: its git was not killed")
	}
	if len(states) != 1 || states[0].State != "unknown" ||
		!slices.ContainsFunc([]string{"context canceled", "signal: terminated", "signal: killed"},
			func(why string) bool { return strings.Contains(states[0].Reason, why) }) {
		t.Errorf("states %+v, want the one lane unknown with the cancellation as its reason", states)
	}
}

func ltCommit(t *testing.T, dir string) {
	t.Helper()
	os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0o644)
	ltGit(t, dir, "add", "work.txt")
	ltGit(t, dir, "commit", "-q", "-m", "work")
}

// TestLaneStates (T-470, T-1012, T-1015): the lane table's states are the one free rule over the lane's row, its HEAD
// and its own dirty status, whatever the main checkout's history says (a landed task frees its lane only at the head
// its submission pinned; with no recorded head, only at a HEAD on main), and each lane's type (T-721, T-765: a working lane's type
// is the model its row's dispatch chose — a ref's model: sonnet:low is sonnet; a lane with no row has none, nor has
// a free lane whatever its row says: a lane's model is its task's only while it holds it). Its git reads run in the
// lane dirs only.
func TestLaneStates(t *testing.T) {
	p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"L-01": "T-01", "L-02": "T-02", "L-03": "T-03",
		"L-04": "T-04", "L-05": "T-05", "L-06": "T-06", "L-07": "T-07", "L-08": "T-08", "L-09": "T-09", "L-10": "T-10",
		"L-11": "T-11"})
	dir := func(l string) string { return filepath.Join(filepath.Dir(p.Root), "proj-wt", l) }
	os.WriteFile(filepath.Join(dir("L-02"), "scratch.txt"), []byte("x"), 0o644) // no row, dirty
	ltSubmit(t, p.Root, "L-03", "T-03", true, true, 0)                          // landed: free
	ltSetType(t, p.Root, "L-03", "sonnet")                                      // a free lane has no type, whatever its row says
	ltCommit(t, dir("L-04"))
	ltSubmit(t, p.Root, "L-04", "T-04", true, false, 0) // submitted, not landed
	ltSetType(t, p.Root, "L-04", "sonnet:high")
	ltCommit(t, dir("L-05"))
	ltSubmit(t, p.Root, "L-05", "T-05", false, false, 0) // dispatched, no task_done yet
	ltSetType(t, p.Root, "L-05", "sonnet:low")
	os.WriteFile(filepath.Join(dir("L-06"), "scratch.txt"), []byte("x"), 0o644)
	ltSubmit(t, p.Root, "L-06", "T-06", true, true, 0) // landed, dirty: never free (T-196)
	// The main checkout's history is no input: L-07's HEAD is main's (its "merge" by ancestry) but its task has not
	// landed; L-08's commit is not on main but its task landed (serve's fact).
	ltSubmit(t, p.Root, "L-07", "T-07", false, false, 0)
	ltCommit(t, dir("L-08"))
	ltSubmit(t, p.Root, "L-08", "T-08", true, true, 0)
	// T-1015: L-09's task landed, then a further clean commit (a driver after obor land): its HEAD is not what landed.
	ltCommit(t, dir("L-09"))
	ltSubmit(t, p.Root, "L-09", "T-09", true, true, 0)
	ltGit(t, dir("L-09"), "commit", "-q", "--allow-empty", "-m", "after the submit")
	// No recorded head (a hand merge with no submission): L-10's HEAD is on main (free), L-11's is not (working).
	ltRow(t, p.Root, "L-10", "T-10", "", true, true, 0)
	ltCommit(t, dir("L-11"))
	ltRow(t, p.Root, "L-11", "T-11", "", true, true, 0)
	if row := p.rowOf(t, "L-09"); row.Head == "" || row.Head == ltHead(t, dir("L-09")) {
		t.Fatalf("precondition: L-09's row has a submitted head %q that is not its HEAD", row.Head)
	}
	if _, err := project.Git(p.Root, "merge-base", "--is-ancestor", "task/T-07", "main"); err != nil {
		t.Fatalf("precondition: L-07's HEAD is in main's history: %v", err)
	}
	if _, err := project.Git(p.Root, "merge-base", "--is-ancestor", "task/T-08", "main"); err == nil {
		t.Fatal("precondition: L-08's commit is not on main")
	}
	var mu sync.Mutex
	var dirs []string
	p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		return project.GitCtx(ctx, dir, args...)
	}
	type laneRow struct{ state, task, typ, warning string }
	got := map[string]laneRow{}
	for _, s := range p.States() {
		got[s.Name] = laneRow{s.State, s.Task, s.Type, s.Warning}
	}
	want := map[string]laneRow{
		"L-01": {"free", "T-01", "", ""},
		"L-02": {"dirty-free", "T-02", "", ""}, // no row: no task holds it, no type
		"L-03": {"free", "T-03", "", ""},
		"L-04": {"working", "T-04", "sonnet", ""},
		"L-05": {"working", "T-05", "sonnet", ""},
		"L-06": {"dirty-free", "T-06", "", ""},
		"L-07": {"working", "T-07", "", ""},
		"L-08": {"free", "T-08", "", ""}, // its HEAD is the submitted head
		"L-09": {"working", "T-09", "", ""},
		"L-10": {"free", "T-10", "", ""},
		"L-11": {"working", "T-11", "", ""},
	}
	for l, w := range want {
		if got[l] != w {
			t.Errorf("%s: %+v, want %+v", l, got[l], w)
		}
	}
	if len(dirs) != 2*len(want)+2 {
		t.Errorf("the table ran %d git reads, want 2 per lane (rev-parse, status) and an ancestry read for each of "+
			"L-10, L-11 (landed, no recorded head): %v", len(dirs), dirs)
	}
	for _, d := range dirs {
		if d == p.Root || d == project.Physical(p.Root) {
			t.Errorf("the table read git in the main checkout %s", d)
		}
	}
	// T-788: no done state (the task's phase is serve's); a submitted lane keeps its task_done's age.
	for _, s := range p.States() {
		submittedLanes := []string{"L-03", "L-04", "L-06", "L-08", "L-09", "L-10", "L-11"}
		if submitted := s.DoneAge != nil; submitted != slices.Contains(submittedLanes, s.Name) {
			t.Errorf("%s: done age %v, want one only for the submitted %v", s.Name, s.DoneAge, submittedLanes)
		}
	}
	if evidence(p, "T-50") {
		t.Error("no Merge T-50: commit yet, but evidence")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-50: done")
	if !evidence(p, "T-50") {
		t.Error("a Merge T-50: commit is evidence")
	}
	// RV-38 #11: a log git cannot read is an error from MergeEvidenceBy, not "no evidence".
	if ok, err := p.MergeEvidenceBy("T-50", func(...string) (string, error) { return "", errors.New("git I/O") }); ok || err == nil {
		t.Errorf("MergeEvidenceBy with git failing = %v, %v; want false and the error", ok, err)
	}
}

// rowOf is lane's row in p's lanes table.
func (p *LaneTable) rowOf(t *testing.T, lane string) store.LaneRow {
	t.Helper()
	rows, err := store.ReadLanesAt(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	return rows[lane]
}

// ltHead is dir's HEAD.
func ltHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := project.Git(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// evidence is p.MergeEvidenceBy over the main checkout's HEAD (git: false).
func evidence(p *LaneTable, id string) bool {
	ok, _ := p.MergeEvidenceBy(id, func(args ...string) (string, error) { return project.Git(p.Root, args...) })
	return ok
}

// ltSetType sets lane's row's type to typ (the type reference its dispatch chose, T-721).
func ltSetType(t *testing.T, root, lane, typ string) {
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
	found := false
	for i := range rows {
		if rows[i].Lane == lane {
			rows[i].Type, found = typ, true
		}
	}
	if !found {
		t.Fatalf("no row for %s", lane)
	}
	if err := store.WriteLanes(db, rows); err != nil {
		t.Fatal(err)
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
	if evidence(p, "T-09") {
		t.Fatal("precondition: a fresh fixture has no evidence for T-09")
	}
	// The default legacy path: a "<id>:" commit touching only docs/tasks is an old spec, not evidence.
	add(p, "docs/tasks/T-09.md", "T-09: spec")
	if evidence(p, "T-09") {
		t.Error("a T-09: commit touching only docs/tasks (the default legacy path) is no evidence")
	}
	// A "<id>:" commit touching a file outside the legacy path is evidence (the squash case).
	add(p, "src/a.go", "T-09: fix")
	if !evidence(p, "T-09") {
		t.Error("a T-09: commit touching src/a.go is evidence (squash)")
	}
	// A configured paths.tasks names the excluded dir: only specs/T-09.md there is no evidence.
	r := ltFixture(t, `,"lanes_dir":"../proj-wt","paths":{"tasks":"specs"}`, nil)
	add(r, "specs/T-09.md", "T-09: spec")
	if evidence(r, "T-09") {
		t.Error("with paths.tasks=specs, a T-09: commit touching only specs/T-09.md is no evidence")
	}
	add(r, "docs/tasks/T-09.md", "T-09: extra spec")
	if !evidence(r, "T-09") {
		t.Error("with paths.tasks=specs, a T-09: commit touching docs/tasks is evidence (outside specs)")
	}
	// A subject without the "<id>:" prefix is no evidence, even when it names the task.
	q := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	add(q, "work.txt", "work for T-09: later")
	if evidence(q, "T-09") {
		t.Error("a subject that does not start with T-09: is no evidence")
	}
	// An empty "<id>:" commit is evidence too (the other packages' fixtures dispatch lanes with such a commit).
	e := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	ltGit(t, e.Root, "commit", "-q", "--allow-empty", "-m", "T-09: merged")
	if !evidence(e, "T-09") {
		t.Error("an empty T-09: commit is evidence")
	}
	// "Merge T-09:" is evidence (control: the gate's merge subject).
	m := ltFixture(t, `,"lanes_dir":"../proj-wt"`, nil)
	ltGit(t, m.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-09: done")
	if !evidence(m, "T-09") {
		t.Error(`a "Merge T-09:" commit is evidence`)
	}
}

// T-550 (RV-57 #1): an old spec commit on main is no merge evidence (the task's work lives only in its lane), for the
// default legacy path and a configured paths.tasks.
func TestLegacySpecOnMainIsNoEvidence(t *testing.T) {
	for _, c := range []struct {
		name, extra, file string
	}{
		{"default legacy path", `,"lanes_dir":"../proj-wt"`, "docs/tasks/T-01.md"},
		{"configured paths.tasks", `,"lanes_dir":"../proj-wt","paths":{"tasks":"specs"}`, "specs/T-01.md"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := ltFixture(t, c.extra, map[string]string{"L-01": "T-01"})
			dir := filepath.Join(filepath.Dir(p.Root), "proj-wt", "L-01")
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
			if evidence(p, "T-01") {
				t.Error("an old T-01: spec commit on main is no merge evidence")
			}
			// Control: the squash rule still finds the task's work on main.
			ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "T-01: work")
			if !evidence(p, "T-01") {
				t.Error("a T-01: work commit on main is evidence")
			}
		})
	}
}

func TestMissingLane(t *testing.T) {
	p := ltFixture(t, `,"lanes":1`, map[string]string{})
	s := p.States()
	if len(s) != 1 || s[0].Name != "L-01" || s[0].State != "missing" || s[0].Branch != "" || s[0].Type != "" {
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

// T-871: a lane whose git or store read failed is never free: its state is unknown, with the reason (git's stderr or
// the store's error), and dispatch's lane list (Lanes, what obor serve's lane choice reads) carries both. A project with
// no database yet is not a failed read: it has no rows and no error, and its lanes read as git says.
func TestLaneUnknownOnFailedRead(t *testing.T) {
	for _, c := range []struct {
		name    string
		breakIt func(t *testing.T, p *LaneTable, dir string)
		want    string // the reason's prefix
	}{
		{name: "status fails", want: "git status:", breakIt: func(t *testing.T, p *LaneTable, dir string) {
			out, err := project.Git(dir, "rev-parse", "--absolute-git-dir")
			if err != nil {
				t.Fatalf("precondition: the lane's git dir: %v", err)
			}
			os.WriteFile(filepath.Join(strings.TrimSpace(out), "index"), []byte("not an index"), 0o644)
		}},
		{name: "rev-parse fails", want: "git rev-parse:", breakIt: func(t *testing.T, p *LaneTable, dir string) {
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("not a gitdir file"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "store unreadable", want: "store:", breakIt: func(t *testing.T, p *LaneTable, dir string) {
			os.MkdirAll(filepath.Dir(store.File(p.Root)), 0o755)
			if err := os.WriteFile(store.File(p.Root), []byte("not a database"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReadLanesAt(p.Root); err == nil {
				t.Fatal("precondition: the store reads")
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"L-01": "T-01"})
			dir := filepath.Join(filepath.Dir(p.Root), "proj-wt", "L-01")
			c.breakIt(t, p, dir)
			s := stateOf(p, "L-01")
			if s.State != "unknown" || !strings.HasPrefix(s.Reason, c.want) || len(s.Reason) == len(c.want) {
				t.Fatalf("lane state %q, reason %q; want unknown and a %q reason with git's or the store's words",
					s.State, s.Reason, c.want)
			}
			if c.want != "store:" && !strings.Contains(s.Reason, "fatal:") {
				t.Errorf("reason %q does not carry git's stderr", s.Reason)
			}
			ls, err := (Lanes{}).Lanes(context.Background(), p.Root)
			if err != nil {
				t.Fatal(err)
			}
			if len(ls) != 1 || ls[0].State != "unknown" || ls[0].Reason != s.Reason {
				t.Errorf("dispatch's lanes %+v; want L-01 unknown with the same reason", ls)
			}
		})
	}
	t.Run("no store yet", func(t *testing.T) {
		p := ltFixture(t, `,"lanes_dir":"../proj-wt"`, map[string]string{"L-01": "T-01"})
		if _, err := store.ReadLanesAt(p.Root); err != nil {
			t.Fatal(err) // precondition: a fresh project has no store, not an unreadable one
		}
		s := stateOf(p, "L-01")
		if s.State != "free" || s.Reason != "" {
			t.Fatalf("lane state %q, reason %q; want free and no reason", s.State, s.Reason)
		}
		ls, err := (Lanes{}).Lanes(context.Background(), p.Root)
		if err != nil || len(ls) != 1 || ls[0].State != "free" || ls[0].Reason != "" {
			t.Errorf("dispatch's lanes %+v, %v; want L-01 free with no reason", ls, err)
		}
	})
}

// stateOf is lane's row of p's table (the zero row when it has none).
func stateOf(p *LaneTable, lane string) port.Lane {
	for _, s := range p.States() {
		if s.Name == lane {
			return s
		}
	}
	return port.Lane{}
}

// A lead commit whose subject contains the id is no merge evidence; "Merge <id>:" is. T-802: a resolved merge, as the
// gate amends it (title subject, spec and summary in the body), is evidence too — git's own "Merge commit '<sha>' into
// HEAD" is not. One repo, read between commits.
func TestMergeEvidenceSubjects(t *testing.T) {
	p := ltFixture(t, "", nil)
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "T-05 widened (lead: spec)")
	if evidence(NewLaneTable(context.Background(), p.Cfg), "T-05") {
		t.Fatal("a lead commit mentioning T-05 counts as merged")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m", "Merge T-05: work")
	if !evidence(NewLaneTable(context.Background(), p.Cfg), "T-05") {
		t.Error("Merge T-05: commit → merged")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m",
		"Merge commit 'abc' into HEAD\n\n# Conflicts:\n#\tshared.txt")
	if evidence(NewLaneTable(context.Background(), p.Cfg), "T-07") {
		t.Fatal("precondition: git's default merge subject counts as T-07's evidence")
	}
	ltGit(t, p.Root, "commit", "-q", "--allow-empty", "-m",
		"Merge T-07: Resolved title\n\nSpec rev 1:\n\n# T-07 — Resolved title\n\nSummary: did it")
	if !evidence(NewLaneTable(context.Background(), p.Cfg), "T-07") {
		t.Error("the resolved merge's subject is not evidence")
	}
}
