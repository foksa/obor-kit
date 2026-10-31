package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"obor/internal/herdr/herdrtest"
	"obor/internal/project"
	"obor/internal/store"
	"obor/internal/testkit"
)

// `obor migrate` (P7-0b, until v0.17): a herd-era project is built from a fixture project by moving .obor back to
// .herd, so its tracked and untracked files, its store and its lane rows are real.

const herdSettings = `{"enabledPlugins":{"herd@herd-kit-stable":true}}`

// herdProject is a herd project: .herd/ (config.json tracked with the herd key and HERD_CHANGED_FILES, local.json,
// drafts/ and state/herd.db untracked, a dispatched lane L-01 in the store), .gitignore lines for .herd, a Claude
// settings file enabling herd@herd-kit-stable, and core.hooksPath on a herd kit's githooks.
func herdProject(t *testing.T) *fx {
	t.Helper()
	x := bare(t)
	x.Lane("L-01", "task/T-1")
	x.Config(testkit.Set("herd", `{"version": "0.15.0"}`),
		testkit.Set("commands", `{"check": "cat $HERD_CHANGED_FILES"}`))
	x.Dispatched("L-01")
	x.Write(".obor/local.json", `{"prefix": "pj"}`+"\n")
	x.Write(".obor/drafts/T-9.md", "draft\n")
	if err := os.Rename(x.Path(".obor"), x.Path(".herd")); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(x.Path(".herd/state/obor.db*"))
	if len(entries) == 0 {
		t.Fatal("precondition: the fixture store has a database")
	}
	for _, f := range entries {
		if err := os.Rename(f, strings.Replace(f, "obor.db", "herd.db", 1)); err != nil {
			t.Fatal(err)
		}
	}
	x.Write(".gitignore", ".herd/state/\n.herd/local.json\n.herd/drafts/\n.DS_Store\n")
	x.Write(".claude/settings.json", herdSettings)
	old := x.Write(filepath.Join(x.Base, "old-kit/.claude-plugin/plugin.json"), `{"name": "herd"}`)
	hooks := filepath.Join(filepath.Dir(filepath.Dir(old)), "githooks")
	x.Git(x.Root, "config", "--local", "core.hooksPath", hooks)
	x.Git(x.Root, "add", "-A")
	x.Git(x.Root, "commit", "-q", "-m", "herd project")
	return x
}

// fakeClaude puts a `claude` first on the runs' PATH: it logs its arguments to <dir>/claude.log, `plugin uninstall`
// drops the herd key from the project's settings, `plugin install` enables obor@obor-kit there unless <dir>/fail
// exists (exit 7). Only shell builtins: the PATH holds nothing else. Returns the log's path and the fail file's.
func (x *fx) fakeClaude(t *testing.T) (log, fail string) {
	t.Helper()
	dir := filepath.Join(x.Base, "fakebin")
	log, fail = filepath.Join(dir, "claude.log"), filepath.Join(dir, "fail")
	script := `#!/bin/bash
d=${0%/*}
echo "$*" >> "$d/claude.log"
case "$*" in
"plugin uninstall "*) printf '{"enabledPlugins":{}}' > .claude/settings.json ;;
"plugin install "*)
	[ -e "$d/fail" ] && exit 7
	printf '{"enabledPlugins":{"obor@obor-kit":true}}' > .claude/settings.json ;;
esac
`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x.env = append(x.env, "PATH="+dir)
	return log, fail
}

func (x *fx) migrate(t *testing.T) (string, int) { t.Helper(); return x.run(t, "migrate") }

func (x *fx) migrated(t *testing.T) string {
	t.Helper()
	out, rc := x.migrate(t)
	if rc != 0 {
		t.Fatalf("migrate: exit %d\n%s", rc, out)
	}
	return out
}

// steps are the OK lines of a migrate run, step by step.
func steps(out string) []string {
	var ok []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "OK ") {
			ok = append(ok, l)
		}
	}
	return ok
}

func TestMigrateMovesAHerdProjectAndItsHistory(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running() // no agent
	if _, err := os.Stat(x.Path(".obor")); err == nil {
		t.Fatal("precondition: no .obor/")
	}

	out := x.migrated(t)

	if got := steps(out); len(got) != 7 {
		t.Errorf("want one OK line per step (7), got %d:\n%s", len(got), out)
	}
	for _, gone := range []string{".herd", ".obor/state/herd.db"} {
		if x.Exists(gone) {
			t.Errorf("%s still exists", gone)
		}
	}
	for _, kept := range []string{".obor/config.json", ".obor/local.json", ".obor/drafts/T-9.md", ".obor/state/obor.db"} {
		if !x.Exists(kept) {
			t.Errorf("%s missing after the move", kept)
		}
	}
	if tracked := x.Git(x.Root, "ls-files", ".obor/config.json"); tracked == "" {
		t.Error(".obor/config.json is not tracked: it moved with a plain rename, not git mv")
	}
	if st := x.Git(x.Root, "status", "--porcelain", "--", ".herd", ".obor/config.json"); !strings.Contains(st, ".herd/config.json -> .obor/config.json") {
		t.Errorf("git sees no rename of config.json:\n%s", st)
	}
	// the store opens with its history
	row, ok, err := store.ReadLaneAt(x.Root, "L-01")
	if err != nil || !ok || row.Task != "T-1" {
		t.Errorf("lane L-01 row after the move = %+v, %v, %v; want task T-1", row, ok, err)
	}
	// config keys: herd → obor, HERD_CHANGED_FILES → OBOR_CHANGED_FILES, the name and prefix stay
	cfg := x.Read(".obor/config.json")
	has(t, cfg, `"obor"`, "config.json")
	has(t, cfg, "$OBOR_CHANGED_FILES", "config.json")
	has(t, cfg, `"name": "proj"`, "config.json")
	has(t, cfg, `"prefix": "pj"`, "config.json")
	hasNot(t, cfg, `"herd"`, "config.json")
	hasNot(t, cfg, "HERD_", "config.json")
	// .gitignore
	if got, want := x.Read(".gitignore"), ".obor/state/\n.obor/local.json\n.obor/drafts/\n.DS_Store\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
	// githooks re-pointed from the herd kit's
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != filepath.Join(kit, "githooks") {
		t.Errorf("core.hooksPath = %s, want this kit's githooks", got)
	}
}

func TestMigrateRerunSaysAlready(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.h.Plugins = []herdrtest.Plugin{{ID: "herd.events", Root: "/old/herdr-plugin"}}
	x.running()
	x.Write(filepath.Join(x.Base, "home/.herd/pj-lead.sid"), "s1\n")
	first := x.migrated(t)
	for i, l := range steps(first) { // steps 1 (a check) and 6 (a listing) have nothing to do on a fresh project too
		if i != 0 && i != 5 && strings.Contains(l, "(already)") {
			t.Fatalf("precondition: the first run does the work, but %q says already:\n%s", l, first)
		}
	}
	x.reset()

	second := x.migrated(t)

	got := steps(second)
	if len(got) != 7 {
		t.Fatalf("second run: %d OK lines, want 7:\n%s", len(got), second)
	}
	for _, l := range got {
		if !strings.HasSuffix(l, "(already)") && !strings.Contains(l, "(already)") {
			t.Errorf("second run: %q does not say (already)", l)
		}
	}
	if r := x.reqs(); strings.Contains(strings.Join(r, "\n"), "plugin.link") || strings.Contains(strings.Join(r, "\n"), "plugin.unlink") {
		t.Errorf("second run touched herdr's plugins: %v", r)
	}
}

func TestMigrateRefusesWhileAServeHoldsAFlock(t *testing.T) {
	for _, dir := range []string{".herd", ".obor"} {
		t.Run(dir, func(t *testing.T) {
			x := herdProject(t)
			x.fakeClaude(t)
			x.running()
			l := project.NewLock(x.Path(dir + "/state/serve.flock"))
			if ok, err := l.Try(); !ok || err != nil {
				t.Fatalf("precondition: take the flock: %v %v", ok, err)
			}
			defer l.Unlock()

			out, rc := x.migrate(t)

			if rc != 1 || !strings.HasPrefix(last(out), "FAIL 1 stop: a serve holds ") {
				t.Fatalf("exit %d, output:\n%s\nwant FAIL 1 stop: a serve holds …", rc, out)
			}
			if !x.Exists(".herd/config.json") || x.Exists(".obor/config.json") && dir == ".herd" {
				t.Error("files moved although the serve runs")
			}
			if x.Exists(".herd/state/herd.db") == false {
				t.Error("herd.db moved although the serve runs")
			}
			if got := x.Read(".gitignore"); !strings.Contains(got, ".herd/state/") {
				t.Errorf(".gitignore rewritten although the serve runs: %q", got)
			}
		})
	}
}

func TestMigrateStopChecksTheFlockHolderPid(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	flock := x.Path(".herd/state/serve.flock")
	// a file naming a live pid (not flocked: a serve in another namespace, a copy) refuses; one naming a gone pid does not
	x.Write(".herd/state/serve.flock", strconv.Itoa(os.Getpid())+"\n")
	if out, rc := x.migrate(t); rc != 1 || !strings.Contains(last(out), "still runs") {
		t.Fatalf("live pid in the flock file: exit %d\n%s", rc, out)
	}
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	x.Write(".herd/state/serve.flock", strconv.Itoa(c.ProcessState.Pid())+"\n")
	if out, rc := x.migrate(t); rc != 0 {
		t.Fatalf("gone pid in the flock file (%s): exit %d\n%s", flock, rc, out)
	}
}

func TestMigrateRefusesWhileAnAgentOfTheProjectRuns(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running([2]string{"pj-l-01", x.LaneDir("L-01")})

	out, rc := x.migrate(t)

	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 1 stop: agent pj-l-01 runs in ") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if x.Exists(".obor") || !x.Exists(".herd/config.json") {
		t.Error("files moved although an agent runs")
	}
	// the panes may stay: an agent of another project does not refuse
	x.running([2]string{"zz-lead", filepath.Join(x.Base, "elsewhere")})
	if out, rc := x.migrate(t); rc != 0 {
		t.Fatalf("an agent of another project refused: exit %d\n%s", rc, out)
	}
}

func TestMigrateFinishesAPartlyMigratedProject(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	// config moved by hand (rename.sh moves the tracked files), the state and the database not
	if err := os.MkdirAll(x.Path(".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	x.Git(x.Root, "mv", ".herd/config.json", ".obor/config.json")

	out := x.migrated(t)

	if x.Exists(".herd") {
		t.Errorf(".herd/ left over:\n%s", out)
	}
	if _, ok, err := store.ReadLaneAt(x.Root, "L-01"); !ok || err != nil {
		t.Errorf("history lost: %v %v", ok, err)
	}
	has(t, out, "OK 2 files: ", "output")
	hasNot(t, x.Read(".gitignore"), ".herd", ".gitignore")
}

func TestMigrateStopsOnAFileInBothPlaces(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	x.Write(".obor/config.json", "{}\n")

	out, rc := x.migrate(t)

	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 2 files: .herd/config.json and .obor/config.json both exist") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
	if got := x.Read(".obor/config.json"); got != "{}\n" {
		t.Errorf("the .obor file was replaced: %q", got)
	}
	if !x.Exists(".herd/config.json") {
		t.Error("the .herd file was lost")
	}
}

func TestMigrateSwapsTheClaudePlugin(t *testing.T) {
	x := herdProject(t)
	log, fail := x.fakeClaude(t)
	x.running()
	if err := os.WriteFile(fail, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	out, rc := x.migrate(t)

	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 3 plugin: claude plugin install obor@obor-kit --scope project failed (exit 7)") {
		t.Fatalf("a failing install: exit %d\n%s", rc, out)
	}
	if !x.Exists(".obor/config.json") {
		t.Error("step 2 did not run before the plugin step")
	}
	os.Remove(fail)

	x.migrated(t)

	b, _ := os.ReadFile(log)
	want := "plugin uninstall herd@herd-kit-stable --scope project\n" +
		"plugin marketplace add " + kit + "\n" +
		"plugin install obor@obor-kit --scope project\n" +
		"plugin marketplace add " + kit + "\n" +
		"plugin install obor@obor-kit --scope project\n"
	if string(b) != want {
		t.Errorf("claude calls:\n%s\nwant:\n%s", b, want)
	}
	hasNot(t, string(b), "marketplace remove", "claude calls")
	has(t, x.Read(".claude/settings.json"), "obor@obor-kit", "settings")
}

func TestMigrateNeedsClaudeOnPath(t *testing.T) {
	x := herdProject(t)
	x.env = append(x.env, "PATH="+x.Base)
	x.running()

	out, rc := x.migrate(t)

	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 3 plugin: claude is not on PATH") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
}

func TestMigrateLinksOborEventsOnceAndLeavesHerdEvents(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	x.h.Plugins = []herdrtest.Plugin{{ID: "herd.events", Root: "/old/herdr-plugin"}}
	x.reset()

	out := x.migrated(t)

	if ids := pluginIDs(x); strings.Join(ids, ",") != "herd.events,obor.events" {
		t.Errorf("herdr plugins = %v, want herd.events left and obor.events linked", ids)
	}
	r := strings.Join(x.reqs(), "\n")
	if n := strings.Count(r, "plugin.link"); n != 1 {
		t.Errorf("plugin.link sent %d times, want once:\n%s", n, r)
	}
	hasNot(t, r, "plugin.unlink", "herdr requests")
	has(t, out, "herd.events left linked (removed in v0.17)", "output")
}

func pluginIDs(x *fx) []string {
	var ids []string
	for _, p := range x.h.Plugins {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestMigrateCopiesTheUserHomeAndAddsTheOpenCodeEntry(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	x.Write(filepath.Join(x.Base, "home/.herd/pj-lead.sid"), "s1\n")
	x.Write(filepath.Join(x.Base, "home/.herd/bin/herd-0.15.0-darwin-arm64"), "binary")
	x.Write(filepath.Join(x.Base, "state-home/pj-lead.sid"), "mine\n") // never overwritten
	x.Write(filepath.Join(x.Base, "home/.herd/state/a.txt"), "a\n")
	conf := x.Write(filepath.Join(x.Base, "home/.config/opencode/opencode.json"),
		`{"mcp":{"servers":{"herd":{"type":"local","command":["/old/herd","mcp"]}}}}`+"\n")
	x.Write(filepath.Join(x.Base, "state-home/pj-lead.sid"), "mine\n")

	x.migrated(t)

	if got := x.Read(filepath.Join(x.Base, "state-home/state/a.txt")); got != "a\n" {
		t.Errorf("~/.herd/state/a.txt copy = %q", got)
	}
	if got := x.Read(filepath.Join(x.Base, "state-home/pj-lead.sid")); got != "mine\n" {
		t.Errorf("the existing file was overwritten: %q", got)
	}
	if x.Exists(filepath.Join(x.Base, "state-home/bin")) {
		t.Error("the old release binaries were copied")
	}
	for _, kept := range []string{filepath.Join(x.Base, "home/.herd/pj-lead.sid"), filepath.Join(x.Base, "home/.herd/state/a.txt"), filepath.Join(x.Base, "home/.herd/bin/herd-0.15.0-darwin-arm64")} {
		if !x.Exists(kept) {
			t.Errorf("%s is gone: ~/.herd must be copied, never moved", kept)
		}
	}
	b, _ := os.ReadFile(conf)
	has(t, string(b), `"herd"`, "opencode.json (the herd entry stays)")
	has(t, string(b), `"obor"`, "opencode.json (the obor entry added)")
}

func TestMigrateListsProseThatStillSaysHerd(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	x.Write("AGENTS.md", "# rules\nrun herd status\nand herdr is fine\n")
	x.Write("docs/agents/lead.md", "see HERD_ROLE\n")
	x.Write("docs/plan.md", "the herd swarm\n")
	x.Write("docs/agents/worker.md", "nothing here\n")

	out := x.migrated(t)

	has(t, out, "OK 6 prose: 3 file(s) still say herd", "output")
	has(t, out, "AGENTS.md (line 2)", "output")
	has(t, out, "docs/agents/lead.md (line 1)", "output")
	has(t, out, "docs/plan.md (line 1)", "output")
	hasNot(t, out, "worker.md", "output")
	if got := x.Read("AGENTS.md"); !strings.Contains(got, "run herd status") {
		t.Errorf("AGENTS.md edited: %q", got)
	}
}

func TestMigrateFinishNamesTheLabelledPanes(t *testing.T) {
	x := herdProject(t)
	x.fakeClaude(t)
	x.running()
	x.h.Shell("w1:p1", "pj-lead", x.Root)
	x.h.Shell("w1:q1", "pj-l-01", x.LaneDir("L-01"))
	x.h.Shell("w1:z9", "other", x.Root)

	out := x.migrated(t)

	has(t, out, "obor restart pj-l-01 --fresh", "output")
	has(t, out, "obor restart pj-lead --fresh   (the lead loses its context: at a handoff)", "output")
	hasNot(t, out, "restart other", "output")
	if i, j := strings.Index(out, "pj-l-01 --fresh"), strings.Index(out, "pj-lead --fresh"); i > j {
		t.Error("the lead is not last")
	}
}

func TestMigrateOutsideAProjectFails(t *testing.T) {
	x := bare(t)
	x.fakeClaude(t)
	x.running()

	out, rc := x.migrate(t)

	if rc != 1 || !strings.HasPrefix(last(out), "FAIL 1 stop: ") || !strings.Contains(last(out), "neither .herd/config.json nor .obor/config.json") {
		t.Fatalf("exit %d\n%s", rc, out)
	}
}

func TestGithooksRepointsAHerdKitsHooksAndChainsOthers(t *testing.T) {
	x := newFx(t)
	mine := filepath.Join(kit, "githooks")
	// a herd kit's githooks (its plugin.json names herd): re-pointed
	old := filepath.Join(x.Base, "herd-kit-stable")
	x.Write(old+"/.claude-plugin/plugin.json", `{"name": "herd"}`)
	x.Git(x.Root, "config", "--local", "core.hooksPath", old+"/githooks")
	out := x.ok(t, "githooks")
	has(t, out, "core.hooksPath = "+mine, "output")
	if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
		t.Errorf("core.hooksPath = %s, want %s", got, mine)
	}
	// a kit that is gone, named like one: re-pointed
	x.Git(x.Root, "config", "--local", "core.hooksPath", "/nowhere/herd-kit/githooks")
	x.ok(t, "githooks")
	// anyone else's hooks dir is the project's own: chained, not refused (T-620)
	for _, other := range []string{"/nowhere/husky/githooks", "/usr/local/hooks", x.Base + "/githooks"} {
		x.Git(x.Root, "config", "--local", "core.hooksPath", other)
		has(t, x.ok(t, "githooks"), "chaining "+other, "chained hooks "+other)
		if got := x.Git(x.Root, "config", "--local", "--get", "obor.hooksChain"); got != other {
			t.Errorf("obor.hooksChain = %s, want %s", got, other)
		}
		if got := x.Git(x.Root, "config", "--local", "core.hooksPath"); got != mine {
			t.Errorf("core.hooksPath %s changed to %s", other, got)
		}
	}
}
