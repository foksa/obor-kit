package launch

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/project"
	"obor/internal/store"
)

// reportWait bounds the session request to serve (a CLI restart's: serve may be down).
const reportWait = 2 * time.Second

func (l *Launcher) rollout() time.Duration {
	if l.Rollout > 0 {
		return l.Rollout
	}
	return 10 * time.Second
}

// proc is a process as it was when pinned: its pid and its start time ("" when ps gave none), so a pid the system
// reuses is not taken for it.
type proc struct {
	pid   int
	start string
}

// pin is the agent in pane as its process info gives it now: the leader of the foreground group (stop's proofs read
// the same). It is read in the start's own path, before the pane can be reused (T-589). ok false: herdr names none.
func (l *Launcher) pin(ctx context.Context, pane string) (proc, bool) {
	pi, err := l.H.ProcessInfo(ctx, pane)
	if err != nil || pi.FgPGID == 0 || pi.AtShell() {
		return proc{}, false
	}
	out, _ := l.ps("-o", "lstart=", "-p", strconv.Itoa(pi.FgPGID))
	return proc{pi.FgPGID, strings.TrimSpace(string(out))}, true
}

// running: p is still the process pinned: its pid lists the same start time (a gone process lists none, a reused pid
// another).
func (l *Launcher) running(p proc) bool {
	out, _ := l.ps("-o", "lstart=", "-p", strconv.Itoa(p.pid))
	return strings.TrimSpace(string(out)) == p.start
}

// reportRollout: after a fresh start of a kind whose transcripts begin with their session (agents.Caps.Sessions:
// Codex's rollout, T-577), the transcript of the start's new session is reported as agent's session (Report); serve
// not told or refusing: said, nothing recorded (a restart resumes agent's last recorded session, else herdr's; T-607).
// The start's session is the one
// whose transcript the started agent's own process p holds open (T-588: a fact, not a guess by cwd and time; T-589: p
// pinned by the start, never read again from the pane, which another agent may hold by now): p or a process below it.
// p gone: nothing recorded. Nothing open within Rollout: said, nothing recorded (never a scan of the cwd; a fresh
// start with no prompt writes none, T-627: the line says so). say is where it says so.
func (l *Launcher) reportRollout(ctx context.Context, agent, pane, kind, lane string, p proc, prompted bool,
	say func(format string, args ...any)) {
	k, ok := agents.Lookup(kind)
	if !ok || !k.Caps().Sessions {
		return
	}
	glob := k.Transcript(l.Env.Get, "", "*")
	if glob == "" {
		return
	}
	var (
		path  string
		s     agents.Session
		found []string // the distinct sessions open: more than one is said, never a guess
		gone  bool
	)
	find := func() bool {
		found = nil
		for _, f := range l.openFiles(l.tree(p.pid)) {
			if !transcriptPath(glob, f) {
				continue
			}
			if fs, ok := firstSession(k, f); ok && (len(found) == 0 || fs.ID != s.ID) {
				path, s = f, fs
				found = append(found, fs.ID)
			}
		}
		// The pin is read last, right before the recording it guards: a pid the system reused while the files were read
		// is gone, and what another process opened meanwhile is not the agent's (T-589).
		if !l.running(p) {
			gone = true
			return false
		}
		return len(found) > 0
	}
	pid := p.pid
	l.until(ctx, l.rollout(), func() bool { return find() || gone })
	switch {
	case gone:
		say("warning: %s (pid %d) is gone: its %s session is not recorded", agent, pid, kind)
		return
	case len(found) == 0:
		why := ""
		if !prompted {
			why = ": expected after a fresh start with no prompt" // Codex writes its rollout at the first one (T-627)
		}
		say("warning: %s (pid %d) holds no %s transcript open within %s (its session is not recorded%s)", agent, pid, kind,
			l.rollout(), why)
		return
	case len(found) > 1:
		say("warning: %s (pid %d) holds several %s transcripts open (sessions %s): its session is not recorded", agent,
			pid, kind, strings.Join(found, ", "))
		return
	}
	rec := store.Session{Agent: agent, Kind: kind, ID: s.ID, Transcript: path, Pane: pane, Lane: lane,
		Started: s.Started.Unix(), Source: kind + " transcript"}
	if l.Cfg != nil && rec.Lane != "" {
		if row, ok, _ := store.ReadLaneAt(l.Cfg.Root, rec.Lane); ok {
			rec.Task = row.Task
		}
	}
	say("session %s (%s, open in pid %d)", rec.ID, path, pid)
	if err := l.report(ctx, rec); err != nil {
		say("warning: session %s of %s not recorded: %v (a restart resumes %s's last recorded session; obor restart %s --fresh starts a new one)",
			rec.ID, agent, err, agent, agent)
	}
}

// transcriptPath: f, a file a process holds open, matches glob (as given, or with the symlinks of its directories
// before the first wildcard resolved: lsof names the physical path).
func transcriptPath(glob, f string) bool {
	if ok, _ := filepath.Match(glob, f); ok {
		return true
	}
	i := strings.Index(glob, "*")
	if i < 0 {
		return false
	}
	dir := filepath.Dir(glob[:i+1])
	ok, _ := filepath.Match(filepath.Join(project.Physical(dir), strings.TrimPrefix(glob, dir)), f)
	return ok
}

// tree is pid and every process below it (ps; pid alone when ps fails): Codex's launcher runs the binary that writes
// the rollout as its child.
func (l *Launcher) tree(pid int) []int {
	out, _ := l.ps("-A", "-o", "pid=,ppid=")
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		c, err1 := strconv.Atoi(f[0])
		p, err2 := strconv.Atoi(f[1])
		if err1 == nil && err2 == nil && c != p {
			children[p] = append(children[p], c)
		}
	}
	pids := []int{pid}
	seen := map[int]bool{pid: true}
	for i := 0; i < len(pids); i++ {
		for _, c := range children[pids[i]] {
			if !seen[c] {
				seen[c] = true
				pids = append(pids, c)
			}
		}
	}
	return pids
}

// openFiles is the files processes pids hold open (Open; else /proc/<pid>/fd on linux, lsof elsewhere); none when they
// cannot be read.
func (l *Launcher) openFiles(pids []int) []string {
	if l.Open != nil {
		return l.Open(pids)
	}
	var files []string
	if runtime.GOOS == "linux" {
		for _, pid := range pids {
			dir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
			fds, _ := os.ReadDir(dir)
			for _, fd := range fds {
				if f, err := os.Readlink(filepath.Join(dir, fd.Name())); err == nil {
					files = append(files, f)
				}
			}
		}
		return files
	}
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	out, _ := exec.Command("lsof", "-n", "-P", "-F", "n", "-p", strings.Join(list, ",")).Output() // exit 1: some pid gone
	for _, line := range strings.Split(string(out), "\n") {
		if f, ok := strings.CutPrefix(line, "n"); ok && strings.HasPrefix(f, "/") {
			files = append(files, f)
		}
	}
	return files
}

// searchRollout runs reportRollout for a fresh start of agent (kind) in pane and lane: the agent's process is pinned
// here, in the start's own path (T-589), and searched in Background when set (its own bound, Rollout and the report's;
// the start returns at once; its lines to Log), else before the start returns.
func (l *Launcher) searchRollout(ctx context.Context, agent, pane, kind, lane string, prompted bool) {
	say := l.say
	if l.Background != nil {
		say = func(string, ...any) {}
		if l.Log != nil {
			say = func(format string, args ...any) { fmt.Fprintf(l.Log, format+"\n", args...) }
		}
	}
	p, ok := l.pin(ctx, pane)
	if !ok {
		say("warning: herdr names no process of %s in pane %s (its %s session is not recorded)", agent, pane, kind)
		return
	}
	if l.Background == nil {
		l.reportRollout(ctx, agent, pane, kind, lane, p, prompted, say)
		return
	}
	l.Background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), l.rollout()+reportWait)
		defer cancel()
		l.reportRollout(ctx, agent, pane, kind, lane, p, prompted, say)
	})
}

// report records s (Report, else the session request to Cfg.Root's serve).
func (l *Launcher) report(ctx context.Context, s store.Session) error {
	if l.Report != nil {
		return l.Report(ctx, s)
	}
	if l.Cfg == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, reportWait)
	defer cancel()
	_, err := app.Verdict(ctx, app.SocketPath(l.Cfg.Root), app.MethodSession, app.Call{Root: l.Cfg.Root,
		Agent: s.Agent, Kind: s.Kind, Session: s.ID, Transcript: s.Transcript, Pane: s.Pane, Lane: s.Lane, Task: s.Task,
		Started: s.Started, Source: s.Source})
	return err
}

// firstSession is the session the first line of the file at path names (k.Session).
func firstSession(k agents.Kind, path string) (agents.Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return agents.Session{}, false
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return agents.Session{}, false
	}
	return k.Session(line)
}
