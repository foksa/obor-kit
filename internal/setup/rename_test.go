package setup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// `obor setup rename`: a project with guards naming its agents (and one off-prefix), local.json, a dispatched
// store, files of the old agents in the global state home (left alone: the inbox records are the project's, by slot),
// and a stub herdr (on PATH) holding the workspace. Agent names follow the
// prefix (no configured role or lane agents).

func renameProject(t *testing.T) *fx {
	t.Helper()
	x := newFx(t, testkit.Set("rules", `{"may_push": ["lead", "integrator"]}`))
	x.PlainLane("L-01", "task/T-1")
	x.PlainLane("L-02", "task/T-2")
	x.Config(testkit.Set("rules", `{"may_push": ["lead", "integrator"]}`))
	x.Write(".obor/local.json", `{"rules": {"may_push": ["reviewer", "pj-lead"]}}`+"\n") // shape 2, as config.json
	x.Dispatched("L-01")
	x.Dispatched("L-02")
	agents := filepath.Join(x.Env().Get("OBOR_STATE_HOME"), "agents")
	for _, f := range []string{"pj-l-01.sid", "pj-l-01.inbox", "pj-lead.inbox", "bob.inbox"} {
		x.Write(filepath.Join(agents, f), "old "+f+"\n")
	}
	x.running()
	return x
}

// stubHerdr puts a `herdr` first on the runs' PATH: one workspace w9 whose label is <dir>/label; `workspace rename`
// writes it; `pane list` answers the one line of <dir>/panes (none: no panes). Every call is logged to <dir>/herdr.log. Only shell builtins. Returns the dir.
func (x *fx) stubHerdr(t *testing.T, label string) string {
	t.Helper()
	dir := filepath.Join(x.Base, "stubbin")
	script := `#!/bin/bash
d=${0%/*}
echo "$*" >> "$d/herdr.log"
case "$1 $2" in
"workspace list") read -r label < "$d/label"; echo '{"result":{"workspaces":[{"workspace_id":"w9","label":"'$label'"}]}}' ;;
"workspace rename") echo "$4" > "$d/label" ;;
"pane list") if [ -f "$d/panes" ]; then read -r panes < "$d/panes"; echo "$panes"; else echo '{"result":{"panes":[]}}'; fi ;;
esac
`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, c := range map[string]string{"herdr": script, "label": label + "\n"} {
		mode := os.FileMode(0o644)
		if f == "herdr" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), mode); err != nil {
			t.Fatal(err)
		}
	}
	x.env = append(x.env, "PATH="+dir)
	return dir
}

func (x *fx) rename(t *testing.T) string {
	t.Helper()
	return x.ok(t, "rename", "--name", "newp", "--prefix", "ob")
}

func TestRenameMapsConfigStoreFilesAndTerminal(t *testing.T) {
	x := renameProject(t)
	dir := x.stubHerdr(t, "proj")
	if row, ok := x.Row("L-01"); !ok || row.Task != "T-1" {
		t.Fatalf("precondition: L-01's row = %+v, want T-1's", row)
	}
	out := x.rename(t)

	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Project.Name != "newp" || cfg.Data.Project.AgentPrefix != "ob" {
		t.Errorf("name/prefix = %s/%s, want newp/ob", cfg.Data.Project.Name, cfg.Data.Project.AgentPrefix)
	}
	if got := cfg.AgentName("lead", ""); got != "ob-lead" {
		t.Errorf("lead agent = %q, want ob-lead (the old lead name follows the new prefix)", got)
	}
	if got := cfg.AgentName("integrator", ""); got != "ob-int" {
		t.Errorf("integrator agent = %q, want ob-int", got)
	}
	// a derived name in allow_push becomes its role; the rest stays.
	if got := strings.Join(cfg.Data.Rules.MayPush, ","); got != "reviewer,lead" {
		t.Errorf("allow_push (local.json's) = %s, want reviewer,lead (pj-lead rewritten)", got)
	}
	if !strings.Contains(x.Read(".obor/config.json"), `"lead"`) || !strings.Contains(x.Read(".obor/config.json"), `"integrator"`) {
		t.Errorf("config.json's allow_push changed:\n%s", x.Read(".obor/config.json"))
	}
	if got := x.Read(".obor/local.json"); strings.Contains(got, "pj-lead") || !strings.Contains(got, `"reviewer"`) {
		t.Errorf("local.json = %s, want pj-lead rewritten to lead, reviewer kept", got)
	}

	if row, _ := x.Row("L-01"); row.Task != "T-1" {
		t.Errorf("L-01 row = %+v, want it untouched (the store is serve's: it ignores the old name)", row)
	}
	if _, ok := x.Row("L-02"); !ok {
		t.Error("L-02's row is gone, want it untouched")
	}

	// no inbox copy step; the old global files stay as they were, none is added.
	agents := filepath.Join(x.Env().Get("OBOR_STATE_HOME"), "agents")
	if entries, err := os.ReadDir(agents); err != nil || len(entries) != 4 {
		t.Errorf("%s: %d entries, %v; want the 4 old files only (nothing copied)", agents, len(entries), err)
	}
	for _, f := range []string{"ob-l-01.inbox", "ob-lead.inbox"} {
		if _, err := os.Stat(filepath.Join(agents, f)); err == nil {
			t.Errorf("%s was written: rename copies no inbox record", f)
		}
	}
	if strings.Contains(out, "copied") {
		t.Errorf("output names a copy:\n%s", out)
	}

	if b, _ := os.ReadFile(filepath.Join(dir, "label")); strings.TrimSpace(string(b)) != "newp" {
		t.Errorf("workspace label = %q, want newp", b)
	}
	for _, want := range []string{"OK 1 config: ", "OK 4 terminal: workspace w9: proj → newp",
		"  pj-l-01 → ob-l-01", "  pj-l-02 → ob-l-02", "  pj-lead → ob-lead", "mv ", "proj-wt"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// a rename rewrites an allow_push entry that was the old prefix's derived name ("pj-lead") to its role
// ("lead"), so it allows the push again (such an entry alone allows nothing); a literal role entry and an
// unknown entry are kept as written.
func TestRenameKeepsAllowPushWorking(t *testing.T) {
	for _, file := range []string{"config.json", "local.json"} {
		t.Run(file, func(t *testing.T) {
			x := renameProject(t)
			x.stubHerdr(t, "proj")
			entries := `["pj-lead", "integrator", "bob", "pj-nobody"]`
			if file == "config.json" {
				x.Config(testkit.Set("rules", `{"may_push": `+entries+`}`))
				x.Write(".obor/local.json", "{}\n")
			} else {
				x.Write(".obor/local.json", `{"rules": {"may_push": `+entries+`}}`+"\n")
			}
			before, err := config.Load(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			if before.PushAllowed("lead") || before.PushAllowed("reviewer") {
				t.Fatalf("precondition: the derived name allows no push, reviewer never: %v", before.Data.Rules.MayPush)
			}
			x.rename(t)
			after, err := config.Load(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			if !after.PushAllowed("lead") || !after.PushAllowed("integrator") {
				t.Errorf("after rename lead/integrator allowed = %v/%v, entries %v", after.PushAllowed("lead"),
					after.PushAllowed("integrator"), after.Data.Rules.MayPush)
			}
			if got := strings.Join(after.Data.Rules.MayPush, ","); got != "lead,integrator,bob,pj-nobody" {
				t.Errorf("allow_push = %s, want lead,integrator,bob,pj-nobody (only pj-lead rewritten)", got)
			}
		})
	}
}

func TestRenameKeepsCustomNamesAndFailsWhileServeRuns(t *testing.T) {
	x := renameProject(t)
	dir := x.stubHerdr(t, "proj")
	before := x.Read(".obor/config.json")
	lock := project.NewLock(filepath.Join(x.Root, ".obor", "state", "serve.flock"))
	if ok, err := lock.Try(); !ok || err != nil {
		t.Fatalf("precondition: the test holds serve's flock: %v, %v", ok, err)
	}
	out, rc := x.run(t, "rename", "--name", "newp", "--prefix", "ob")
	if rc != 1 || !strings.Contains(last(out), "FAIL 0 stop: a serve holds ") {
		t.Fatalf("exit %d, want FAIL 0 stop:\n%s", rc, out)
	}
	if x.Read(".obor/config.json") != before {
		t.Error("config.json changed although serve runs")
	}
	if row, _ := x.Row("L-01"); row.Task != "T-1" {
		t.Errorf("L-01's row = %+v: the store changed although serve runs", row)
	}
	if _, err := os.Stat(filepath.Join(dir, "herdr.log")); err == nil {
		t.Error("herdr was called although serve runs")
	}
	lock.Unlock()

	// An agent of the project runs: refused too.
	x.running([2]string{"pj-l-01", x.LaneDir("L-01")})
	out, rc = x.run(t, "rename", "--name", "newp", "--prefix", "ob")
	if rc != 1 || !strings.Contains(last(out), "FAIL 0 stop: agent pj-l-01 runs in ") {
		t.Fatalf("exit %d, want FAIL 0 stop (agent runs):\n%s", rc, out)
	}
	if x.Read(".obor/config.json") != before {
		t.Error("config.json changed although an agent runs")
	}
}

func TestRenameUsage(t *testing.T) {
	x := renameProject(t)
	for _, args := range [][]string{{"rename"}, {"rename", "--name", "n"}, {"rename", "--prefix", "p"}, {"rename", "--name", "a b", "--prefix", "p"}} {
		if out, rc := x.run(t, args...); rc != 1 || !strings.HasPrefix(last(out), "FAIL ") {
			t.Errorf("%v: exit %d, last %q", args, rc, last(out))
		}
	}
}

// The panes of the project's workspace whose label follows the old prefix are relabelled; a pane with another label,
// and a pane of another workspace, are left alone.
func TestRenameRelabelsTheMappedPanesOnly(t *testing.T) {
	x := renameProject(t)
	dir := x.stubHerdr(t, "proj")
	panes := `{"result":{"panes":[` +
		`{"pane_id":"w9:p1","workspace_id":"w9","label":"pj-int"},` +
		`{"pane_id":"w9:p2","workspace_id":"w9","label":"pj-lead"},` +
		`{"pane_id":"w9:p3","workspace_id":"w9","label":"advisor"},` +
		`{"pane_id":"w9:p4","workspace_id":"w9"},` +
		`{"pane_id":"w3:p1","workspace_id":"w3","label":"pj-l-01"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "panes"), []byte(panes+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := x.rename(t)
	b, _ := os.ReadFile(filepath.Join(dir, "herdr.log"))
	log := string(b)
	for _, want := range []string{"pane rename w9:p1 ob-int\n", "pane rename w9:p2 ob-lead\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("herdr calls lack %q:\n%s", want, log)
		}
	}
	if n := strings.Count(log, "pane rename"); n != 2 {
		t.Errorf("%d pane renames, want 2 (advisor, the unlabelled pane and another workspace's pane stay):\n%s", n, log)
	}
	if !strings.Contains(out, "OK 4 terminal: workspace w9: proj → newp; pane labels w9:p1 pj-int → ob-int, w9:p2 pj-lead → ob-lead") {
		t.Errorf("step 4 line missing:\n%s", out)
	}
}

// A lane worktree's opencode.json that names another obor kit's shim is pointed at this kit's bin/obor; its other
// members and a lane without one stay.
func TestRenameRewritesALanesOpencodeKitPath(t *testing.T) {
	x := renameProject(t)
	x.stubHerdr(t, "proj")
	path := filepath.Join(x.LaneDir("L-01"), "opencode.json")
	x.Write(path, `{"$schema": "https://opencode.ai/config.json", "model": "m/x", "mcp": {"servers": {"obor": {"type": "local", "command": ["/gone/obor-kit/bin/obor", "mcp"]}}}}`+"\n")
	shim := filepath.Join(kit, "bin", "obor")
	out := x.rename(t)
	got := x.Read(path)
	if !strings.Contains(got, `"command": ["`+shim+`", "mcp"]`) || strings.Contains(got, "/gone/") {
		t.Errorf("opencode.json = %s, want the command at %s", got, shim)
	}
	if !strings.Contains(got, `"model": "m/x"`) || !strings.Contains(got, `"$schema"`) {
		t.Errorf("opencode.json = %s, want its other members as they were", got)
	}
	if !strings.Contains(out, "OK 3 files: kit path in L-01/opencode.json") {
		t.Errorf("step 3 line missing:\n%s", out)
	}
	if again := x.rename(t); !strings.Contains(again, "OK 3 files: no kit path to correct (already)") {
		t.Errorf("a rerun changes nothing:\n%s", again)
	}
}

// A lane's opencode.json that is there but cannot be read stops the rename at step 3: only a missing file is skipped
// (a lane without one is normal).
func TestRenameStopsOnALaneOpencodeItCannotRead(t *testing.T) {
	x := renameProject(t)
	x.stubHerdr(t, "proj")
	path := filepath.Join(x.LaneDir("L-01"), "opencode.json")
	if err := os.Mkdir(path, 0o755); err != nil { // a directory: ReadFile answers, but not a not-exist error
		t.Fatal(err)
	}
	if _, err := os.ReadFile(path); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("precondition: reading %s = %v, want an error other than not-exist", path, err)
	}
	out, rc := x.run(t, "rename", "--name", "newp", "--prefix", "ob")
	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 3 files: ") || !strings.Contains(last(out), path) {
		t.Fatalf("exit %d, last line %q, want FAIL 3 files naming %s\n%s", rc, last(out), path, out)
	}
	if strings.Contains(out, "OK 4 terminal") {
		t.Errorf("the run went past the unreadable file:\n%s", out)
	}
}

// while the project's serve LaunchAgent is installed under the old name (obor.serve.<old>), the
// rename is refused before any step runs: no config, no herdr call, no file changed; with none installed it runs.
func TestRenameRefusesWhileTheOldServeAgentIsInstalled(t *testing.T) {
	x := renameProject(t)
	dir := x.stubHerdr(t, "proj")
	before := x.Read(".obor/config.json")
	var asked []string
	plist := filepath.Join(x.Base, "Library", "LaunchAgents", "obor.serve.proj.plist")
	x.installed = func(cfg *config.Config) (string, bool) {
		asked = append(asked, cfg.ProjectName())
		return plist, true
	}
	out, rc := x.run(t, "rename", "--name", "newp", "--prefix", "ob")
	if rc != 1 || last(out) != "FAIL the serve LaunchAgent "+plist+" is installed under the old name: run `obor serve uninstall` first, then obor setup rename again" {
		t.Fatalf("exit %d, want the refusal:\n%s", rc, out)
	}
	if len(asked) != 1 || asked[0] != "proj" {
		t.Errorf("asked for %v, want the old name's (proj) once", asked)
	}
	if strings.Contains(out, "OK ") {
		t.Errorf("a step ran before the refusal:\n%s", out)
	}
	if x.Read(".obor/config.json") != before {
		t.Error("config.json changed although the old serve agent is installed")
	}
	if _, err := os.Stat(filepath.Join(dir, "herdr.log")); err == nil {
		t.Error("herdr was called although the old serve agent is installed")
	}

	x.installed = func(*config.Config) (string, bool) { return plist, false }
	out = x.rename(t)
	if !strings.Contains(out, "OK 1 config: ") || !strings.Contains(out, "OK 5 finish") {
		t.Errorf("not installed: want the rename to run:\n%s", out)
	}
	if cfg, err := config.Load(x.Root); err != nil || cfg.Data.Project.Name != "newp" {
		t.Errorf("not installed: config name = %v, %v; want newp", cfg, err)
	}
}
