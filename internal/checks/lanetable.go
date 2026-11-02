package checks

import (
	"strings"
	"sync"
	"time"

	"obor/internal/app/port"
	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/store"
)

// The lane table (T-470, from package lanes): each configured lane's worktree as git and the store's lanes table
// (read-only) say; obor status and obor serve's app.Lanes read it.

// NewLaneTable is the lane table of a loaded config.
func NewLaneTable(cfg *config.Config) *LaneTable {
	return &LaneTable{Cfg: cfg, Root: cfg.Root, Now: time.Now().Unix()}
}

// LaneStatus is one lane's row of the lane table. Type is its lane type: the dispatch's while it holds a task
// (taskType), else the config's.
type LaneStatus struct {
	Lane, Type, Dir string
	Branch, Task    string // "" when detached / no task id in the branch
	State           string // free, dirty-free, working, done, missing (live: also stalled, waiting-bg, blocked)
	DoneAge         *int64 // seconds since the task's task_done was recorded; nil without one
	Dirty           bool
	Warning         string // a live warning (agent-blocked …), or ""
}

// LaneTable is the project's lanes as their worktrees, git and the store's lanes table say, computed once per command.
type LaneTable struct {
	Cfg  *config.Config
	Root string
	Now  int64

	once     sync.Once
	states   []LaneStatus
	rowsOnce sync.Once
	rows     map[string]store.LaneRow // the lanes table, by lane, read once (unreadable: none)
}

// States is every configured lane's row, in config order (computed once, lanes in parallel).
func (p *LaneTable) States() []LaneStatus {
	p.once.Do(func() {
		ls := p.Cfg.Lanes()
		p.states = make([]LaneStatus, len(ls))
		var wg sync.WaitGroup
		for i, l := range ls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.states[i] = p.LaneState(l)
			}()
		}
		wg.Wait()
	})
	return p.states
}

// LaneState is one lane's row:
//
//	missing     the lane dir is no worktree of its own
//	free        its HEAD is in the main checkout's HEAD (merged) and nothing is uncommitted
//	dirty-free  merged, with uncommitted changes (never free, T-196)
//	done        not merged, its task submitted by task_done (its row's finished) and not sent back since
//	working     anything else
//
// A lane whose row holds its task at its current HEAD is not merged until the task has merge evidence.
func (p *LaneTable) LaneState(l config.Lane) LaneStatus {
	s := LaneStatus{Lane: l.Name, Type: l.Type, Dir: l.Dir, State: "missing"}
	if !project.IsDir(l.Dir) {
		return s
	}
	out, err := project.Git(l.Dir, "rev-parse", "--show-toplevel", "HEAD", "--symbolic-full-name", "HEAD")
	f := strings.Split(strings.TrimSpace(out), "\n")
	if err != nil || len(f) != 3 || (f[0] != l.Dir && f[0] != project.Physical(l.Dir)) {
		return s
	}
	head := f[1]
	s.Branch, _ = strings.CutPrefix(f[2], "refs/heads/")
	if s.Branch == f[2] {
		s.Branch = ""
	}
	s.Task = p.Cfg.TaskFromBranch(s.Branch)

	var merged bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := project.Git(p.Root, "merge-base", "--is-ancestor", head, "HEAD")
		merged = err == nil && !(s.Task != "" && p.inFlight(l.Name, s.Task, head))
	}()
	status, _ := project.Git(l.Dir, "status", "--porcelain")
	wg.Wait()
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 {
			s.Dirty = true
			break
		}
	}
	// A lane with uncommitted changes is never free (T-196): its HEAD inside the base proves nothing when its task has
	// no merge evidence (a task branch fast-forwarded to the base by hand, the work still uncommitted): in flight.
	if merged && s.Dirty && s.Task != "" && !p.MergeEvidence(s.Task) {
		merged = false
	}

	fin := p.finished(s.Task)
	submitted := fin != 0
	if submitted {
		age := max(p.Now-fin, 0)
		s.DoneAge = &age
	}

	switch {
	case merged:
		s.State = "free"
		if s.Dirty {
			s.State = "dirty-free"
		}
	case submitted:
		s.State = "done"
	default:
		s.State = "working"
	}
	s.Type = p.taskType(l, s)
	return s
}

// taskType is the lane type of lane l in state s (T-721): a working or done lane's is the type its row's dispatch
// runs (the row holds its task; store.LaneTypeRef: a row from before T-721 has none, the config's), any other lane's
// the config's ("": an untyped lane).
func (p *LaneTable) taskType(l config.Lane, s LaneStatus) string {
	if s.State != "working" && s.State != "done" {
		return l.Type
	}
	if row, ok := p.Rows()[l.Name]; ok && s.Task != "" && row.Task == s.Task {
		typ, _ := config.ParseTypeRef(store.LaneTypeRef(row.Type, l.Type))
		return typ
	}
	return l.Type
}

// Lanes is port.Lanes from today's lane table (obor serve's app.Lanes): a lane's task is open while its branch is not
// merged; its agent is its row's (the lanes table, read-only). Its type is the config's, the lane's reservation ("":
// a pool lane), never the type its dispatch runs (T-738: that is the dispatch's, store.LaneTypeRef).
type Lanes struct{}

func (Lanes) Lanes(root string) ([]port.Lane, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return nil, err
	}
	var out []port.Lane
	lp := NewLaneTable(cfg)
	for i, s := range lp.States() {
		l := port.Lane{Name: s.Lane, Dir: project.Physical(s.Dir), Type: cfg.Lanes()[i].Type, State: s.State,
			Agent: lp.Rows()[s.Lane].Agent}
		if s.State == "working" || s.State == "done" {
			l.Task = s.Task
		}
		out = append(out, l)
	}
	return out, nil
}

// Rows is the store's lanes table by lane, read once (read-only; no database or an unreadable one: none).
func (p *LaneTable) Rows() map[string]store.LaneRow {
	p.rowsOnce.Do(func() {
		var err error
		if p.rows, err = store.ReadLanesAt(p.Root); err != nil {
			p.rows = map[string]store.LaneRow{}
		}
	})
	return p.rows
}

// finished is when task's task_done was recorded (its lane row's finished; 0: none, or "" task).
func (p *LaneTable) finished(task string) int64 {
	for _, row := range p.Rows() {
		if task != "" && row.Task == task {
			return row.Finished
		}
	}
	return 0
}

// inFlight: the lane's row says it works on task — at head (the HEAD recorded at dispatch), or, without a recorded
// head (a lane dispatched before heads were recorded), until the task has merge evidence.
func (p *LaneTable) inFlight(lane, task, head string) bool {
	row, ok := p.Rows()[lane]
	if !ok || row.Task != task {
		return false
	}
	if row.Head == "" {
		return !p.MergeEvidence(task)
	}
	return row.Head == head
}

// MergeEvidence (T-196, T-290, T-547, T-550): a commit reachable from HEAD whose subject is "Merge <id>: …" (the gate's
// merge of the task) or starts with "<id>:" (a lane's squashed work), except a "<id>:" commit that changes only files in
// the legacy spec dir (paths.tasks, else docs/tasks): an old "<id>: spec" commit is no evidence (T-290's exclusion,
// kept while adopters' history still holds specs there, T-521 stopped writing them). One log with file names reads
// both rules. Task branches are not looked at. A log git cannot read is no evidence.
func (p *LaneTable) MergeEvidence(id string) bool {
	ok, _ := p.MergeEvidenceBy(id, func(args ...string) (string, error) { return project.Git(p.Root, args...) })
	return ok
}

// MergeEvidenceBy is MergeEvidence with git run by git (git -C <root> args…): an error when git cannot read the log.
func (p *LaneTable) MergeEvidenceBy(id string, git func(args ...string) (string, error)) (bool, error) {
	out, err := git("log", "-F", "--grep="+id+":", "--format=\x1e%s", "--name-only", "HEAD")
	if err != nil {
		return false, err
	}
	spec := legacySpecDir(p.Cfg)
	for _, rec := range strings.Split(out, "\x1e") {
		lines := strings.Split(strings.TrimRight(rec, "\n"), "\n")
		if len(lines) == 0 {
			continue
		}
		if strings.HasPrefix(lines[0], "Merge "+id+":") ||
			(strings.HasPrefix(lines[0], id+":") && !specOnly(lines[1:], spec)) {
			return true, nil
		}
	}
	return false, nil
}

// legacySpecDir is the spec dir before T-521 wrote none: the configured paths.tasks, else its former default docs/tasks.
func legacySpecDir(cfg *config.Config) string {
	if dir := strings.Trim(cfg.Data.Paths["tasks"], "/"); dir != "" {
		return dir
	}
	return "docs/tasks"
}

// specOnly: files (a commit's changed paths) hold at least one, and every one is inside dir. A commit that changes
// nothing (an empty commit) is not spec-only.
func specOnly(files []string, dir string) bool {
	n := 0
	for _, f := range files {
		if f == "" {
			continue
		}
		n++
		if f != dir && !strings.HasPrefix(f, dir+"/") {
			return false
		}
	}
	return n > 0
}
