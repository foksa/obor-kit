package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"obor/internal/app"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/herdr"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/store"
)

// serveCmd is `obor serve` (T-323: the app; T-522: one per project, run in it; T-336: the merge loop), `obor serve
// install|uninstall|restart` (its launchd agent, T-596), `obor serve status` (an alias of `obor status --facts`) and
// `dump`.
func serveCmd(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cwd, _ := os.Getwd()
	return serve(ctx, args, project.OSEnv, cwd)
}

// serve runs the app for the project around cwd until ctx ends: it holds the project's serve.flock (one serve per
// project) and listens on <root>/.obor/state/serve.sock.
func serve(ctx context.Context, args []string, env project.Env, cwd string) int {
	switch {
	case len(args) == 1 && args[0] == "status":
		return status([]string{"--facts"})
	case len(args) == 1 && args[0] == "dump":
		return serveDump(out, cwd)
	case len(args) == 1 && (args[0] == "install" || args[0] == "uninstall" || args[0] == "restart"):
		return serveService(args[0], cwd, env)
	case len(args) != 0:
		errf("usage: obor serve [install | uninstall | restart | status (= obor status --facts, goes next release) | dump]")
		return 2
	}
	repo, err := project.Find(cwd)
	if err != nil || !configured(repo.Root) {
		errf("FAIL obor serve: %s is not in an obor project (no .obor/config.json): run it in the project", cwd)
		return 1
	}
	root := repo.Root
	sock := app.SocketPath(root)
	if err := herdr.CheckSocketPath(sock); err != nil {
		errf("FAIL obor serve: %v", err)
		return 1
	}
	lock := project.NewLock(app.FlockFile(root))
	ok, err := lock.Try()
	if err != nil {
		errf("FAIL obor serve: %v", err)
		return 1
	}
	if !ok {
		errf("FAIL obor serve already runs for %s (pid %s, %s)", root, lock.Holder(), lock.Path)
		return 1
	}
	defer lock.Unlock()
	_ = os.Remove(sock) // a leftover of a serve that died: the lock says none runs
	ln, err := net.Listen("unix", sock)
	if err != nil {
		errf("FAIL obor serve: %v", err)
		return 1
	}
	defer os.Remove(sock)
	defer ln.Close()
	panes := herdr.Panes{S: herdrSocket(env)}
	infoCtx, cancelInfo := context.WithTimeout(ctx, 2*time.Second)
	version, protocol, infoErr := panes.S.VersionProtocol(infoCtx)
	cancelInfo()
	if infoErr == nil {
		if protocol == herdr.Protocol {
			fmt.Fprintf(flushWriter{}, "herdr: %s, protocol %d\n", version, protocol)
		} else {
			fmt.Fprintf(flushWriter{}, "herdr %s speaks protocol %d, obor speaks %d: update obor or herdr so they match\n",
				version, protocol, herdr.Protocol)
		}
	}
	a := app.New(app.Options{Root: root, Out: flushWriter{}, Refresh: refreshed, Loop: mergeLoop(env, panes),
		Inboxed: inboxed(env), Sid: sid(env), Recorded: recorded(root, env)},
		panes, laneTable{})
	go a.Listen(ln)
	fmt.Fprintf(flushWriter{}, "obor serve: running (project %s, socket %s)\n", root, sock)
	_ = a.Serve(ctx)
	return 0
}

// mergeLoop is the merge loop's adapters for a project: serve's scratch on its config as it is now (a merged config
// change counts), launch for the integrator's start and the merged worker's stop, the herdr socket for the job
// prompts and the lines it types (T-466), the session inbox for the lead's lines and for the lines and job wakes of a
// kind with an inbox (T-485, T-499), and dispatch as state (dispatchLoop: obor serve is the only dispatcher, T-408).
func mergeLoop(env project.Env, panes herdr.Panes) func(root string) (*app.Loop, error) {
	var cache cacheDir // asked once: the Loop is built on every pass
	return func(root string) (*app.Loop, error) {
		repo, err := project.Find(root)
		if err != nil {
			return nil, err
		}
		cfg, err := config.LoadDaemon(repo.Root)
		if err != nil {
			return nil, err
		}
		o := checks.Options{Cfg: cfg, Env: env, Environ: os.Environ(), Out: flushWriter{}, PID: os.Getpid()}
		agents := &launch.Launcher{Cfg: cfg, Env: env, H: herdrSocket(env), Out: io.Discard,
			Background: func(search func()) { go search() }, Log: flushWriter{}}
		l := &app.Loop{Scratch: checks.Scratch{Options: o}, Agents: agents, Typer: panes, Nightly: nightly(o),
			Disk: diskOf(repo.Root, &cache), Inbox: poster{env}}
		dispatchLoop(l, env, cfg)
		return l, nil
	}
}

// herdrSocket is the herdr socket client every runtime caller shares: herdr's socket for env (herdr.SocketPath).
func herdrSocket(env project.Env) *herdr.Socket { return &herdr.Socket{Path: herdr.SocketPath(env)} }

// sid is app.Options.Sid: agent's session id as its SessionStart hook recorded it (<agent>.sid, else its .inbox's).
func sid(env project.Env) func(agent string) string {
	return func(agent string) string {
		if s := launch.RecordedSid(agent, env); s != "" {
			return s
		}
		in, _ := project.ReadInbox(agent, env)
		return in.Session
	}
}

// recorded is app.Options.Recorded: the session agent's <agent>.sid holds (the SessionStart hook's fallback while
// serve is down), started at the file's time, its transcript as the kinds keep them.
func recorded(root string, env project.Env) func(agent string) (store.Session, bool) {
	return func(agent string) (store.Session, bool) {
		id := launch.RecordedSid(agent, env)
		fi, err := os.Stat(project.SidFile(agent, env))
		if id == "" || err != nil {
			return store.Session{}, false
		}
		return store.Session{ID: id, Transcript: project.TranscriptOf(root, id, env), Started: fi.ModTime().Unix(),
			Source: "sid file"}, true
	}
}

// cacheDir is the Go build cache dir (`go env GOCACHE`), read on first use.
type cacheDir struct {
	once sync.Once
	dir  string
}

func (c *cacheDir) get() string {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, "go", "env", "GOCACHE").Output()
		c.dir = strings.TrimSpace(string(out))
	})
	return c.dir
}

// diskOf is the merge loop's low-disk watch (T-564) of the volume root is on.
func diskOf(root string, cache *cacheDir) *app.Disk {
	return &app.Disk{Volume: root, Cache: cache.get,
		Free: func() (uint64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(root, &st); err != nil {
				return 0, err
			}
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		}}
}

// nightly is the merge loop's nightly full check of o's project (merge.nightly_at; nil: unset): what obor nightly
// runs, cut off when ctx ends (as by a signal).
func nightly(o checks.Options) *app.Nightly {
	if o.Cfg.Data.Merge.NightlyAt == "" {
		return nil
	}
	root := o.Cfg.Root
	return &app.Nightly{At: o.Cfg.Data.Merge.NightlyAt,
		Due: func(merges []app.Merge, fresh bool) (bool, error) { return checks.NightlyDue(root, merges, fresh) },
		Last: func() (string, string, error) {
			sha, _, verdict, err := checks.LastFullCheck(root)
			return sha, verdict, err
		},
		Run: func(ctx context.Context) (app.NightlyOut, error) {
			cut, done := make(chan int, 1), make(chan struct{})
			defer close(done)
			go func() {
				select {
				case <-ctx.Done():
					cut <- 143
				case <-done:
				}
			}()
			o.Interrupted = cut
			r := checks.Nightly(o, "NIGHTLY")
			out := app.NightlyOut{Line: r.Line}
			switch r.Code {
			case checks.OK:
				out.Verdict = "GREEN"
			case checks.Red:
				out.Verdict = "RED"
			default:
				return out, errors.New(cmp.Or(r.Line, fmt.Sprintf("obor nightly exited %d", r.Code)))
			}
			sha, _, _, err := checks.LastFullCheck(root)
			out.SHA = sha
			return out, err
		}}
}

// refreshed: the herdr stream ended only so the app reads a new snapshot (a workspace or a pane moved).
func refreshed(err error) bool {
	var e *herdr.Error
	return errors.As(err, &e) && e.Code == "refresh"
}

// configured: root has an obor config.
func configured(root string) bool {
	_, err := config.LoadDaemon(root)
	return err == nil
}

// serveDump is `obor serve dump` (T-435): the event log of the project around cwd as the JSONL serve wrote before
// phase 3, read-only, so it works with serve down. For a rollback: stop serve, then
// `obor serve dump > .obor/state/events.jsonl` and run the old binary.
func serveDump(w io.Writer, cwd string) int {
	repo, err := project.Find(cwd)
	if err != nil {
		errf("FAIL obor serve dump: %v", err)
		return 1
	}
	root := repo.Root
	if err := app.Dump(w, root); err != nil {
		errf("FAIL obor serve dump %s: %v", root, err)
		return 1
	}
	return 0
}

// laneTable is app.Lanes from today's lane table: a lane's task is open while its branch is not merged; its agent is
// its row's (the lanes table, read-only).
type laneTable struct{}

func (t laneTable) Lanes(root string) ([]app.Lane, error) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return nil, err
	}
	var out []app.Lane
	lp := checks.NewLaneTable(cfg)
	for _, s := range lp.States() {
		l := app.Lane{Name: s.Lane, Dir: project.Physical(s.Dir), Type: s.Type, State: s.State,
			Agent: lp.Rows()[s.Lane].Agent}
		if s.State == "working" || s.State == "done" {
			l.Task = s.Task
		}
		out = append(out, l)
	}
	return out, nil
}

// flushWriter writes through out and flushes at once.
type flushWriter struct{}

func (flushWriter) Write(p []byte) (int, error) {
	n, err := out.Write(p)
	if err == nil {
		err = out.Flush()
	}
	return n, err
}
