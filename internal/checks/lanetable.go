package checks

import (
	"context"
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
	// Git is the git command the table's reads run (nil: project.GitCtx): a test counts them (T-880).
	Git func(ctx context.Context, dir string, args ...string) (string, error)

	once     sync.Once
	states   []port.Lane
	rowsOnce sync.Once
	rows     map[string]store.LaneRow // the lanes table, by lane, read once
	rowsErr  error                    // why it cannot be read (no database yet: nil, no rows, T-871)
}

// git is the table's git: project.GitCtx unless a test injected Git.
func (p *LaneTable) git(dir string, args ...string) (string, error) {
	if p.Git != nil {
		return p.Git(p.ctx, dir, args...)
	}
	return project.GitCtx(p.ctx, dir, args...)
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

// LaneState is one lane's row: missing when the lane dir is no worktree of its own, else the one free rule
// (port.FreeState: free, dirty-free, working, unknown) over the store's row of the lane (its task, whether that task
// landed: serve's projection, T-1012; the head its submission pinned, T-1015), the lane's HEAD and its own uncommitted
// changes. Its git reads run in the lane dir only (rev-parse, status; headMerged's only for a landed row with no
// recorded head), never in the main checkout; what serve does for its task is the task's phase (T-788). Unknown: git
// or the store could not tell (Reason says why, T-871), never free.
func (p *LaneTable) LaneState(l config.Lane) port.Lane {
	// The agent is derived from the lane (T-1031: no row stores it); a keep lane says its mode (a fresh one none).
	s := port.Lane{Name: l.Name, Dir: l.Dir, State: port.LaneMissing, Agent: p.Cfg.AgentName("worker", l.Name)}
	if l.Mode == config.LaneKeep {
		s.Mode = l.Mode
	}
	if !project.IsDir(l.Dir) {
		return s
	}
	// --symbolic-full-name applies to the names after it: HEAD's sha first, then its ref.
	out, err := p.git(l.Dir, "rev-parse", "--show-toplevel", "HEAD", "--symbolic-full-name", "HEAD")
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
	status, err := p.git(l.Dir, "status", "--porcelain")
	if err != nil {
		return unknown(s, "git status: "+err.Error())
	}
	for _, line := range strings.Split(status, "\n") {
		if len(line) > 3 {
			s.Dirty = true
			break
		}
	}
	// An unreadable store cannot tell whether a task holds the lane: unknown, never free (T-871). A fresh project has
	// no store, not an unreadable one: store.ReadLanesAt says so with no rows and no error.
	rows, err := p.Rows()
	if err != nil {
		return unknown(s, "store: "+err.Error())
	}
	row := rows[l.Name]
	s.State = port.FreeState(&row, head, s.Dirty, func() bool { return p.headMerged(l.Dir) })
	if fin := p.finished(s.Task); fin != 0 {
		age := max(p.Now-fin, 0)
		s.DoneAge = &age
	}
	s.Type = p.taskType(l, s)
	return s
}

// headMerged: the lane's HEAD is in the target branch's history (branches.main), read in the lane dir (T-1015: the
// merge evidence of a landed row with no recorded head, a hand merge with no submission); git failing proves nothing.
func (p *LaneTable) headMerged(dir string) bool {
	_, err := p.git(dir, "merge-base", "--is-ancestor", "HEAD", "refs/heads/"+p.Cfg.Data.Branches.Main)
	return err == nil
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

// Lanes is port.Lanes from today's lane table (obor serve's app.Lanes): a lane's task is open while the lane works
// (its row's task has not landed, T-1012); its agent is derived from the lane (LaneState). A lane whose git or store read failed is unknown, with
// its reason (T-871): dispatch never picks it.
type Lanes struct{}

func (Lanes) Lanes(ctx context.Context, root string) ([]port.Lane, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	lp := NewLaneTable(ctx, cfg)
	out := lp.States() // this call's own table: its rows are changed in place; an unknown lane carries its error
	for i := range out {
		l := &out[i]
		l.Dir = project.Physical(l.Dir)
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

// MergeEvidenceBy (T-196, T-290, T-547, T-550) reads, with git run by git (git -C <root> args…), whether HEAD's log
// holds a commit whose subject is "Merge <id>: …" (the gate's merge of the task) or starts with "<id>:" (a lane's
// squashed work). Task branches are not looked at. An error when git cannot read the log. Serve's Scratch.Landed reads
// it for a task merged by hand (the merged fact that sets the lane row's landed, T-1012); the lane table itself never
// reads the main checkout.
func (p *LaneTable) MergeEvidenceBy(id string, git func(args ...string) (string, error)) (bool, error) {
	out, err := git("log", "-F", "--grep="+id+":", "--format=\x1e%s", "HEAD")
	if err != nil {
		return false, err
	}
	for _, rec := range strings.Split(out, "\x1e") {
		lines := strings.Split(strings.TrimRight(rec, "\n"), "\n")
		if len(lines) == 0 {
			continue
		}
		if strings.HasPrefix(lines[0], "Merge "+id+":") || strings.HasPrefix(lines[0], id+":") {
			return true, nil
		}
	}
	return false, nil
}
