package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"obor/internal/agents/plugin"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/launch"
	"obor/internal/project"
	"obor/internal/selfexec"
	"obor/internal/service"
)

// serveCmd is `obor serve` (the app; one per project, run in it; the merge loop), `obor serve
// install|uninstall|restart` (its launchd agent) and `dump`.
func serveCmd(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cwd, _ := os.Getwd()
	return serve(ctx, args, project.OSEnv, cwd)
}

// serve runs the app for the project around cwd until ctx ends: it holds the project's serve.flock (one serve per
// project) and listens on <root>/.obor/state/serve.sock, and it writes the project into the machine's list of obor
// projects by name.
func serve(ctx context.Context, args []string, env project.Env, cwd string) int {
	switch {
	case len(args) == 1 && args[0] == "dump":
		return serveDump(out, cwd)
	case len(args) == 1 && (args[0] == "install" || args[0] == "uninstall" || args[0] == "restart"):
		return serveService(args[0], cwd, env)
	case len(args) != 0:
		errf("%s", help.Usage("serve"))
		return 2
	}
	repo, err := project.Find(cwd)
	if err != nil || !configured(repo.Root) {
		errf("FAIL obor serve: %s is not in an obor project (no .obor/config.json): run it in the project", cwd)
		return 1
	}
	if project.Stopped(repo.Root) { // obor stop: nothing serves a sleeping project
		errf("FAIL obor serve: %s is stopped (obor stop): obor start wakes it", repo.Root)
		return 1
	}
	// stop ends serve the way a signal does: as a graceful shutdown, exit 0. The self-restart uses it, and so
	// does the socket's exit request (obor stop).
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	end := &serveEnd{stop: stop}
	root := repo.Root
	sock := wire.SocketPath(root)
	if err := wire.CheckSocketPath(sock); err != nil {
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
	// serve's own rotation, so a new build's re-exec counts as much as an install or restart. launchd's fd
	// is opened with O_APPEND: the truncate keeps it on this file and its next write lands at 0. A failure is one
	// line, not the end of serve: serving the project is not the rotation's to refuse.
	if err := service.RotateServeLog(root); err != nil {
		fmt.Fprintf(flushWriter{}, "obor serve: serve.log not rotated (%v)\n", err)
	}
	_ = os.Remove(sock) // a leftover of a serve that died: the lock says none runs
	// The plugin kinds' manifests, before anything reads them: the merge loop keeps them current.
	kinds := &kindCache{}
	cfg, cfgErr := config.Load(root)
	if cfgErr == nil {
		kinds.refresh(cfg, flushWriter{})
	}
	// The machine's list of obor projects by name: obor send and obor projects read it. A failure is one line,
	// not the end of serve: naming a project is a convenience, serving it is not.
	if _, err := project.Register(root, env); err != nil {
		fmt.Fprintf(flushWriter{}, "obor serve: the machine's project list is not updated (%v)\n", err)
	}
	term, err := terminal(cfg, env)
	if err != nil {
		errf("FAIL obor serve: %v", err)
		return 1
	}
	a := app.New(app.Options{Root: root, Out: flushWriter{}, Refresh: port.Refreshed, Loop: mergeLoop(env, term, end, kinds),
		Inboxed: inboxed(root), Exit: stop, Binary: binaryWatch(env), Tick: serveTick, Follow: serveTick,
		Info: term.Control.ProcessInfo, Screen: term.Control.Screen, Restore: restorer(term, paneless(env))},
		term.Panes, checks.Lanes{})
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = a.Serve(ctx)
	}()
	// The socket is bound only once the project is loaded: a call made during the replay finds no socket and
	// its client retries inside its own deadline; nothing is admitted to wait for the load.
	select {
	case <-served:
		return 0
	case <-a.Loaded():
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		errf("FAIL obor serve: %v", err)
		stop()
		<-served
		return 1
	}
	defer os.Remove(sock)
	defer ln.Close()
	go a.Listen(ln)
	fmt.Fprintf(flushWriter{}, "obor serve: running (project %s, socket %s)\n", root, sock)
	<-served
	if !end.exec.Load() {
		return 0
	}
	// The self-restart of a serve launchd does not run (a pane, Linux): it has ended as at a signal, and now
	// the same pid execs the kit's shim, which runs the binary it picks now. The flock goes with the exec (Go's files
	// are close-on-exec); the socket is removed here, and a new serve removes a leftover once it holds the lock.
	ln.Close()
	_ = os.Remove(sock)
	kit := project.KitRoot(env)
	fmt.Fprintf(flushWriter{}, "obor serve: restarting in place (exec %s serve)\n", selfexec.Shim(kit))
	if err := selfExec(kit, []string{"serve"}); err != nil {
		errf("FAIL obor serve: the restart's exec of %s: %v", selfexec.Shim(kit), err)
		return 1
	}
	return 0 // only a test's selfExec returns nil
}

// serveTick is the app's tick and its binary watch's (0: their default, 30 s); a test shortens it.
var serveTick time.Duration

// serveEnd is how serve ends for its self-restart: stop ends it as a signal does; exec, set by the
// restart, has serve exec the kit's shim once it has ended (a serve launchd does not restart).
type serveEnd struct {
	stop context.CancelFunc
	exec atomic.Bool
}

// mergeLoop is the merge loop's adapters for a project: serve's scratch on its config as it is now (a merged config
// change counts), launch for the integrator's start and the merged worker's stop, the terminal for the job
// prompts and the lines it types, the session inbox for the lead's lines and for the lines and job wakes of a
// kind with an inbox, and dispatch as state (dispatchLoop: obor serve is the only dispatcher).
// end ends serve for its self-restart. A plugin kind whose command changed is described again first
// (kindCache).
func mergeLoop(env project.Env, term port.Terminal, end *serveEnd, kinds *kindCache) func(root string) (*app.Loop, error) {
	return func(root string) (*app.Loop, error) {
		repo, err := project.Find(root)
		if err != nil {
			return nil, err
		}
		cfg, err := config.Load(repo.Root)
		if err != nil {
			return nil, err
		}
		if kinds.refresh(cfg, flushWriter{}) { // the registry's plugin kinds carry the new manifest
			if cfg, err = config.Load(repo.Root); err != nil {
				return nil, err
			}
		}
		o := checks.Options{Cfg: cfg, Env: env, Environ: os.Environ(), Out: flushWriter{}, PID: os.Getpid()}
		agents := &launch.Launcher{Cfg: cfg, Env: env, H: term.Control, Out: io.Discard, Diag: flushWriter{},
			Background: func(search func()) { go search() }, Log: flushWriter{}}
		wf, err := workflowOf(o)
		if err != nil {
			return nil, err
		}
		if k := cfg.TerminalKind(); k != config.DefaultTerminal { // herdr is the one terminal adapter
			return nil, fmt.Errorf("terminal.kind %q: serve knows %s", k, strings.Join(config.TerminalKinds, ", "))
		}
		l := &app.Loop{Workflow: wf, Agents: agents, Prompter: term.Control, Nightly: nightly(o), Disk: diskOf(repo.Root),
			Inbox: poster{repo.Root}}
		dispatchLoop(l, env, cfg)
		l.Config = configFile{root: repo.Root, main: cfg.MainBranch()}
		l.Restart = serveRestarter(cfg, env, end)
		l.Versions = func(ctx context.Context) map[string]string { return launch.CLIVersions(ctx, env, watchKinds(cfg)) }
		return l, nil
	}
}

// kindCache is serve's cache step: each plugin kind of the project's config gets its manifest cached
// (plugin.Ensure) at serve's start and again once its command changed (another path, a rebuilt binary), so the guard,
// the starts, obor setup mcp and obor config validate read it with no hand step; hooks still never run a plugin. A
// failure is said once per kind until it changes or heals: the guard denies that kind's program meanwhile.
type kindCache struct {
	mu   sync.Mutex
	said map[string]string // kind → the failure last said
}

// refresh caches what cfg's plugin kinds lack, saying each cache written and each new failure on log; wrote says one
// was written (a config load then registers the kinds with it).
func (kc *kindCache) refresh(cfg *config.Config, log io.Writer) (wrote bool) {
	kc.mu.Lock()
	defer kc.mu.Unlock()
	if kc.said == nil {
		kc.said = map[string]string{}
	}
	for _, name := range cfg.PluginKinds() {
		cmd := cfg.KindCommand(name)
		w, err := plugin.Ensure(name, cmd, cfg.StateDir())
		switch {
		case err != nil && kc.said[name] != err.Error():
			fmt.Fprintf(log, "obor serve: FAIL %v (the guard denies %s until its manifest is cached)\n", err, name)
			kc.said[name] = err.Error()
		case err != nil:
		case w:
			fmt.Fprintf(log, "obor serve: kind %s: manifest of %s cached (%s)\n", name, cmd,
				plugin.CacheFile(cfg.StateDir(), name))
			wrote = true
			fallthrough
		default:
			delete(kc.said, name)
		}
	}
	return wrote
}

// workflowOf is the git workflow workflow.kind selects (obor config validate refuses another kind): scratch-ff,
// serve's scratch on o's config, its lanes, and the review git on the main checkout.
func workflowOf(o checks.Options) (port.Workflow, error) {
	switch k := o.Cfg.WorkflowKind(); k {
	case config.DefaultWorkflow:
		scratch := checks.Scratch{Options: o}
		return checks.NewScratchFF(scratch, reviewGit{root: o.Cfg.Root, locked: scratch.Locked}), nil
	default:
		return nil, fmt.Errorf("workflow.kind %q: serve knows %s", k, strings.Join(config.WorkflowKinds, ", "))
	}
}

// diskOf is the merge loop's low-disk watch of the volume root is on.
func diskOf(root string) *app.Disk {
	return &app.Disk{Volume: root,
		Free: func() (uint64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(root, &st); err != nil {
				return 0, err
			}
			return uint64(st.Bavail) * uint64(st.Bsize), nil
		}}
}

// watchKinds are the agent kinds of every model cfg knows (its catalog: built-in and configured, the one dispatch
// validates a model ref against), unique and sorted: the CLIs this project may start, so the ones whose version serve
// reads. A kind whose program is not installed gives no reading, as a failed --version does.
func watchKinds(cfg *config.Config) []string {
	var kinds []string
	for _, name := range cfg.ModelNames() {
		if m, ok := cfg.Model(name); ok && m.Kind != "" {
			kinds = append(kinds, m.Kind)
		}
	}
	slices.Sort(kinds)
	return slices.Compact(kinds)
}

// nightly is the merge loop's nightly full check of o's project: what obor nightly runs, cut off when ctx ends (as by
// a signal). At "" (merge.nightly_at unset) is off: the pass starts no run, but still settles one.
func nightly(o checks.Options) *app.Nightly {
	root := o.Cfg.Root
	return &app.Nightly{At: o.Cfg.NightlyAt(),
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
			r := checks.Nightly(o)
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

// configured: root has an obor config.
func configured(root string) bool {
	_, err := config.Load(root)
	return err == nil
}

// serveDump is `obor serve dump`: the project's event log around cwd, one JSON record a line, read-only, so it works
// with serve down.
func serveDump(w io.Writer, cwd string) int {
	repo, err := project.Find(cwd)
	if err != nil {
		errf("FAIL obor serve dump: %v", err)
		return 1
	}
	root := repo.Root
	if err := event.Dump(w, root); err != nil {
		errf("FAIL obor serve dump %s: %v", root, err)
		return 1
	}
	return 0
}

// flushWriter writes through out and flushes at once; outMu holds the pair together, since out's buffer is written
// by serve's own goroutine and by the app's (Options.Out).
type flushWriter struct{}

func (flushWriter) Write(p []byte) (int, error) {
	outMu.Lock()
	defer outMu.Unlock()
	n, err := out.Write(p)
	if err == nil {
		err = out.Flush()
	}
	return n, err
}
