package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// `obor setup rename` (T-587): a project with guards naming its agents (and one off-prefix), local.json, a dispatched
// store, session files of the old agents, and a stub herdr (on PATH) holding the workspace. Agent names follow the
// prefix (T-765: no configured role or lane agents).

func renameProject(t *testing.T) *fx {
	t.Helper()
	x := newFx(t, testkit.Set("guards", `{"allow_push": ["pj-lead", "pj-int", "bob"]}`))
	x.Lane("L-01", "task/T-1")
	x.Lane("L-02", "task/T-2")
	x.Config(testkit.Set("guards", `{"allow_push": ["pj-lead", "pj-int", "bob"]}`))
	x.Write(".obor/local.json", `{"guards": {"allow_push": ["pj-rev", "bob"]}}`+"\n")
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

func TestRenameMapsConfigStoreFilesAndWorkspace(t *testing.T) {
	x := renameProject(t)
	dir := x.stubHerdr(t, "proj")
	if row, _ := x.Row("L-01"); row.Agent != "pj-l-01" {
		t.Fatalf("precondition: L-01's row agent = %q", row.Agent)
	}
	out := x.rename(t)

	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.Name != "newp" || cfg.Data.Prefix != "ob" {
		t.Errorf("name/prefix = %s/%s, want newp/ob", cfg.Data.Name, cfg.Data.Prefix)
	}
	if got := cfg.AgentName("lead", ""); got != "ob-lead" {
		t.Errorf("lead agent = %q, want ob-lead (the old lead name follows the new prefix)", got)
	}
	if got := cfg.AgentName("integrator", ""); got != "ob-int" {
		t.Errorf("integrator agent = %q, want ob-int", got)
	}
	if got := strings.Join(cfg.Data.Guards.AllowPush, ","); got != "ob-rev,bob" {
		t.Errorf("allow_push (local.json's) = %s, want ob-rev,bob", got)
	}
	if !strings.Contains(x.Read(".obor/config.json"), `"ob-lead"`) || !strings.Contains(x.Read(".obor/config.json"), `"ob-int"`) {
		t.Errorf("config.json's allow_push not mapped:\n%s", x.Read(".obor/config.json"))
	}
	if !strings.Contains(x.Read(".obor/local.json"), `"ob-rev"`) || strings.Contains(x.Read(".obor/local.json"), "pj-") {
		t.Errorf("local.json = %s", x.Read(".obor/local.json"))
	}

	if row, _ := x.Row("L-01"); row.Agent != "pj-l-01" || row.Task != "T-1" {
		t.Errorf("L-01 row = %+v, want it untouched (the store is serve's: it ignores the old name)", row)
	}
	if row, _ := x.Row("L-02"); row.Agent != "pj-l-02" {
		t.Errorf("L-02 row agent = %q, want it untouched", row.Agent)
	}

	agents := filepath.Join(x.Env().Get("OBOR_STATE_HOME"), "agents")
	for from, to := range map[string]string{"pj-l-01.inbox": "ob-l-01.inbox", "pj-lead.inbox": "ob-lead.inbox"} {
		if b, err := os.ReadFile(filepath.Join(agents, to)); err != nil || string(b) != "old "+from+"\n" {
			t.Errorf("%s = %q, %v; want the copy of %s", to, b, err, from)
		}
		if _, err := os.Stat(filepath.Join(agents, from)); err != nil {
			t.Errorf("%s is gone: the old files stay", from)
		}
	}
	if _, err := os.Stat(filepath.Join(agents, "bob.inbox")); err != nil {
		t.Errorf("bob.inbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(agents, "ob-l-01.sid")); err == nil {
		t.Error("a .sid file was copied: only the inbox record is (T-599)")
	}

	if b, _ := os.ReadFile(filepath.Join(dir, "label")); strings.TrimSpace(string(b)) != "newp" {
		t.Errorf("workspace label = %q, want newp", b)
	}
	for _, want := range []string{"OK 1 config: ", "OK 4 herdr: workspace w9: proj → newp",
		"  pj-l-01 → ob-l-01", "  pj-l-02 → ob-l-02", "  pj-lead → ob-lead", "mv ", "proj-wt"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestRenameRerunIsAlready(t *testing.T) {
	x := renameProject(t)
	x.stubHerdr(t, "proj")
	x.rename(t)
	before := x.Read(".obor/config.json")
	out := x.rename(t)
	for _, step := range []string{"OK 0 stop", "OK 1 config", "OK 3 files", "OK 4 herdr"} {
		found := false
		for _, l := range strings.Split(out, "\n") {
			found = found || strings.HasPrefix(l, step) && strings.HasSuffix(l, "(already)")
		}
		if !found {
			t.Errorf("%s is not (already) on the rerun:\n%s", step, out)
		}
	}
	if x.Read(".obor/config.json") != before {
		t.Error("the rerun changed config.json")
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
	if row, _ := x.Row("L-01"); row.Agent != "pj-l-01" {
		t.Errorf("L-01's agent = %q: the store changed although serve runs", row.Agent)
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
	if !strings.Contains(out, "OK 4 herdr: workspace w9: proj → newp; pane labels w9:p1 pj-int → ob-int, w9:p2 pj-lead → ob-lead") {
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
	if !strings.Contains(out, "; kit path in L-01/opencode.json") {
		t.Errorf("step 3 line missing:\n%s", out)
	}
	if again := x.rename(t); !strings.Contains(again, "OK 3 files: no agent file left to copy, no kit path to correct (already)") {
		t.Errorf("a rerun changes nothing:\n%s", again)
	}
}
