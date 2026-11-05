package checks

import (
	"context"
	"errors"
	"os/exec"
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

// NewLaneTable is the lane table of a loaded config: its git reads run under ctx (T-873: a canceled request or serve
// pass ends them).
func NewLaneTable(ctx context.Context, cfg *config.Config) *LaneTable {
	return &LaneTable{ctx: ctx, Cfg: cfg, Root: cfg.Root, Now: time.Now().Unix()}
}

// LaneTable is the project's lanes as their worktrees, git and the store's lanes table say, computed once per command.
type LaneTable struct {
	ctx  context.Context // the git reads' (NewLaneTable)
	Cfg  *config.Config
	Root string
	Now  int64

	once     sync.Once
	states   []port.Lane
	rowsOnce sync.Once
	rows     map[string]store.LaneRow // the lanes table, by lane, read once
	rowsErr  error                    // why it cannot be read (no database yet: nil, no rows, T-871)
}

// States is every configured lane's row, in config order (computed once, lanes in parallel).
func (p *LaneTable) States() []port.Lane {
	p.once.Do(func() {
		ls := p.Cfg.Lanes()
		p.states = make([]port.Lane, len(ls))
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
//	working     anything else (what serve does for its task is the task's phase, T-788: git facts only here)
//	unknown     git or the store could not tell (Reason says why, T-871): never free
//
// A lane whose row holds its task at its current HEAD is not merged until the task has merge evidence.
func (p *LaneTable) LaneState(l config.Lane) port.Lane {
	s := port.Lane{Name: l.Name, Dir: l.Dir, State: port.LaneMissing}
	if !project.IsDir(l.Dir) {
		return s
	}
	out, err := project.GitCtx(p.ctx, l.Dir, "rev-parse", "--show-toplevel", "HEAD", "--symbolic-full-name", "HEAD")
	if err != nil {
		return unknown(s, "git rev-parse: "+err.Error())
	}
	f := strings.Split(strings.TrimSpace(out), "\n")
	if len(f) != 3 || (f[0] != l.Dir && f[0] != project.Physical(l.Dir)) {
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
	var mergedErr, statusErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := project.GitCtx(p.ctx, p.Root, "merge-base", "--is-ancestor", head, "HEAD")
		var exit *exec.ExitError
		switch {
		case err == nil:
			merged = !(s.Task != "" && p.inFlight(l.Name, s.Task, head))
		case errors.As(err, &exit) && exit.ExitCode() == 1: // not an ancestor: a reading, not a failed read
		default:
			mergedErr = err
		}
	}()
	status, statusErr := project.GitCtx(p.ctx, l.Dir, "status", "--porcelain")
	wg.Wait()
	switch {
	case mergedErr != nil:
		return unknown(s, "git merge-base: "+mergedErr.Error())
	case statusErr != nil:
		return unknown(s, "git status: "+statusErr.Error())
	}
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
	// A lane that holds a task or looks merged needs its row (in flight at this head, its task's model and agent):
	// an unreadable store cannot tell, so it is unknown, never free (T-871). A fresh project has no store, not an
	// unreadable one: store.ReadLanesAt says so with no rows and no error.
	if _, err := p.Rows(); err != nil && (s.Task != "" || merged) {
		return unknown(s, "store: "+err.Error())
	}

	if fin := p.finished(s.Task); fin != 0 {
		age := max(p.Now-fin, 0)
		s.DoneAge = &age
	}

	switch {
	case merged && s.Dirty:
		s.State = port.LaneDirtyFree
	case merged:
		s.State = port.LaneFree
	default:
		s.State = port.LaneWorking
	}
	s.Type = p.taskType(l, s)
	return s
}

// unknown is s as a lane whose read failed: state unknown, why a git or store error (T-871). Its branch and task, when
// the git read that failed came after them, still say which task the lane holds.
func unknown(s port.Lane, why string) port.Lane {
	s.State, s.Reason = port.LaneUnknown, why
	return s
}

// taskType is the model lane l runs in state s (T-721, T-765), without its effort: project.RowModel's answer for
// the lane's row, kept only while git says the lane works on the row's task (a free lane whose row keeps its last
// task runs none).
func (p *LaneTable) taskType(l config.Lane, s port.Lane) string {
	rows, _ := p.Rows()
	row := rows[l.Name]
	if s.State != port.LaneWorking || s.Task == "" || row.Task != s.Task {
		return ""
	}
	ref, _ := project.RowModel(row)
	typ, _ := config.ParseModelRef(ref)
	return typ
}

// Lanes is port.Lanes from today's lane table (obor serve's app.Lanes): a lane's task is open while its branch is not
// merged; its agent is its row's (the lanes table, read-only). A lane whose git or store read failed is unknown, with
// its reason (T-871): dispatch never picks it.
type Lanes struct{}

func (Lanes) Lanes(ctx context.Context, root string) ([]port.Lane, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return nil, err
	}
	lp := NewLaneTable(ctx, cfg)
	rows, _ := lp.Rows() // an unknown lane carries its store error in Reason
	out := lp.States()   // this call's own table: its rows are changed in place
	for i := range out {
		l := &out[i]
		l.Dir, l.Agent = project.Physical(l.Dir), rows[l.Name].Agent
		if l.State != port.LaneWorking {
			l.Task = ""
		}
	}
	return out, nil
}

// Rows is the store's lanes table by lane, read once (read-only); an error when the database or its lanes table
// cannot be read, with no rows. A project with no database yet has no rows and no error (T-871).
func (p *LaneTable) Rows() (map[string]store.LaneRow, error) {
	p.rowsOnce.Do(func() {
		p.rows, p.rowsErr = store.ReadLanesAt(p.Root)
	})
	return p.rows, p.rowsErr
}

// finished is when task's task_done was recorded (its lane row's finished; 0: none, or "" task).
func (p *LaneTable) finished(task string) int64 {
	rows, _ := p.Rows()
	for _, row := range rows {
		if task != "" && row.Task == task {
			return row.Finished
		}
	}
	return 0
}

// inFlight: the lane's row says it works on task — at head (the HEAD recorded at dispatch), or, without a recorded
// head (a lane dispatched before heads were recorded), until the task has merge evidence.
func (p *LaneTable) inFlight(lane, task, head string) bool {
	rows, _ := p.Rows()
	row, ok := rows[lane]
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
	ok, _ := p.MergeEvidenceBy(id, func(args ...string) (string, error) { return project.GitCtx(p.ctx, p.Root, args...) })
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
