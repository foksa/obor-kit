package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/app/event"
	"obor/internal/config"
)

// loadCfg is the config of the project at root as its files read now: the config a test's pass or call runs on.
func loadCfg(t testing.TB, root string) *config.Config {
	t.Helper()
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatalf("config of %s: %v", root, err)
	}
	return cfg
}

// cfgAt is loadCfg for a helper with no *testing.T: nil when the config does not load, as a status read has it.
func cfgAt(root string) *config.Config {
	cfg, _ := config.Load(root)
	return cfg
}

// withCfg is l on the config of the project at root as its files read now: the Loop a test's adapters build, as serve
// builds its own.
func withCfg(root string, l *Loop) *Loop {
	l.Cfg = cfgAt(root)
	return l
}

// withAppCfg is l on the config of the project a serves, for a pass a test runs on a itself.
func withAppCfg(a *App, l *Loop) *Loop {
	a.mu.Lock()
	root := a.opt.Root
	if a.p != nil {
		root = a.p.root
	}
	a.mu.Unlock()
	return withCfg(root, l)
}

// The core reads the project's config where a pass or a call starts and nowhere else: config.Load is called by the
// entries' one reader (proj.cfgNow) and by the project's load; the entries below are the only ones that read it, each
// once, and everything else takes the config it is handed (Loop.Cfg for a pass or a verb that takes a Loop).
func TestConfigReadAtEntries(t *testing.T) {
	t.Parallel()
	loaders := map[string]bool{"Load": true, "LoadWith": true, "LoadReader": true, "LoadForValidate": true, "LoadHook": true}
	loadSites := map[string]bool{ // the functions that call the config package's loaders
		"proj.cfgNow":   true, // the one read of a call that takes no Loop
		"App.load":      true, // the project's load at serve's start
		"App.configSet": true, // the candidate document of a config change, not a read of the files
	}
	entries := map[string]bool{ // the functions that read the config for their call, through proj.cfgNow
		"App.servedCfg": true, "App.taskDone": true, "App.sendCall": true, "App.watchCall": true, "App.pauseCall": true,
		"App.idleCall": true, "App.reviewCall": true, "App.backup": true, "App.specSet": true, "App.resumeLine": true,
		"App.reconcile": true,
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil {
				switch r := fn.Recv.List[0].Type.(type) {
				case *ast.StarExpr:
					name = r.X.(*ast.Ident).Name + "." + name
				case *ast.Ident:
					name = r.Name + "." + name
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch x, isPkg := sel.X.(*ast.Ident); {
				case isPkg && x.Name == "config" && loaders[sel.Sel.Name]:
					reads++
					if !loadSites[name] {
						t.Errorf("%s: %s calls config.%s: the core reads the config at its entries (proj.cfgNow) or takes it from Loop.Cfg",
							file, name, sel.Sel.Name)
					}
				case sel.Sel.Name == "cfgNow" && name != "proj.cfgNow" && !entries[name]:
					t.Errorf("%s: %s reads the config (cfgNow) but is no entry of a call or a pass", file, name)
				}
				return true
			})
		}
	}
	if reads < len(loadSites) {
		t.Errorf("found %d config reads, want at least one in each of %d sites: the scan finds nothing", reads, len(loadSites))
	}
}

// A start records the config its pass ran on (Loop.Cfg), not the file as it reads when the step runs: the config
// file changes after the pass's adapters were built and the start's row keeps the revision of the pass's config.
func TestAStartRecordsTheConfigItsPassRanOn(t *testing.T) {
	t.Parallel()
	r := newConcRig(t)
	p := r.proj("pj")
	r.add("pj", "T-2", event.StepDispatched, event.KindFact, dispatchData{Lane: "L-02", Dir: r.a.obs["w1:p2"].pane.Cwd, Agent: "pj-l-02"})
	r.add("pj", "T-2", event.StepPrepare, event.KindResult, prepareOut{Base: "b"})
	f := &startEffects{concFakes: r.fakes["pj"]}
	fx := withAppCfg(r.a, &Loop{Agents: f, Prompter: f, Kit: r.kit})
	if fx.Cfg == nil {
		t.Fatal("precondition: the pass's adapters carry no config")
	}
	passRev := fx.Cfg.Rev()
	// A merge lands between the pass's read of the config and the start: the model sonnet gains an id.
	next := `{"version":2,"project":{"name":"pj","agent_prefix":"pj"},"agents":{"roles":{"integrator":""},` +
		`"models":{"sonnet":{"kind":"claude","id":"sonnet-4-6"}}}}`
	if err := os.WriteFile(filepath.Join(r.roots["pj"], ".obor", "config.json"), []byte(next), 0o644); err != nil {
		t.Fatal(err)
	}
	if now := loadCfg(t, r.roots["pj"]).Rev(); now == passRev {
		t.Fatalf("precondition: the file changed but its revision did not (both %s)", now)
	}
	mt := *p.folded.loop["T-2"]
	start := event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepStartWork}
	work := event.Key{Task: mt.task, Attempt: mt.attempt, Step: event.StepWork}
	if !r.a.startWorker(context.Background(), p, fx, mt, start, startData{Agent: mt.agent, Pane: "w1:p2"}, work) {
		t.Fatal("the start failed")
	}
	r.a.mu.Lock()
	row, ok := p.reg.Start("pj-l-02")
	r.a.mu.Unlock()
	if !ok || row.Config != passRev {
		t.Errorf("the start row's config revision %q (row %+v, ok %v), want the pass's %s", row.Config, row, ok, passRev)
	}
}
