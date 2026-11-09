package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/version"
)

// T-1125 (docs/research/config-shape.md §4.2 steps 2-3): the rewrite's decision (RewriteShape) on a fake committer,
// obor config migrate's and serve's alike; the commit path itself is configFile's (cmd/obor's
// TestConfigFileCommitsOnlyTheConfig).

// shapeRoot is a project root with config.json and local.json (none when local is ""), and a fakeConfig on its
// config.json.
func shapeRoot(t *testing.T, cfg, local string) (string, *fakeConfig) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(root, ".obor", name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("config.json", cfg)
	if local != "" {
		write("local.json", local)
	}
	return root, &fakeConfig{path: filepath.Join(root, ".obor", "config.json")}
}

// readShape is the root's .obor/<name> ("" when there is none).
func readShape(root, name string) string {
	b, _ := os.ReadFile(filepath.Join(root, ".obor", name))
	return string(b)
}

const (
	migrateConfig = `{"version":1,"name":"proj","prefix":"pj","lanes":2,"done_file_sections":["Summary"]}` + "\n"
	migrateLocal  = `{"handoff_tokens":400000}` + "\n"
)

// A config.json of shape 1 is rewritten: one commit of the reshaped file (subject "config: shape 2 (obor <version>)",
// the dropped key in the body), local.json reshaped in place, the line naming who, the commit and the dropped key; a
// second run finds shape 2 and writes nothing.
func TestRewriteShapeOnceThenAlready(t *testing.T) {
	t.Parallel()
	root, f := shapeRoot(t, migrateConfig, migrateLocal)
	ctx := context.Background()
	line, refused, err := RewriteShape(ctx, root, f, "obor config migrate")
	want := "config: .obor/config.json rewritten as config shape 2 by obor config migrate (commit c0ffee1; dropped: " +
		"done_file_sections); .obor/local.json rewritten in place"
	if err != nil || refused != "" || line != want {
		t.Fatalf("RewriteShape: %q, %q, %v; want %q", line, refused, err, want)
	}
	msg := "config: shape 2 (obor " + version.Version + ")\n\nRewritten from config shape 1 by obor config migrate.\n" +
		"Dropped (no home in shape 2): done_file_sections"
	if got := f.messages(); len(got) != 1 || got[0] != msg {
		t.Errorf("commits %q, want one:\n%s", got, msg)
	}
	doc := readShape(root, "config.json")
	if config.ShapeOf([]byte(doc)) != config.ShapeVersion || strings.Contains(doc, "done_file_sections") ||
		!strings.HasSuffix(doc, "}\n") {
		t.Errorf("config.json after the rewrite:\n%s", doc)
	}
	local := readShape(root, "local.json")
	if v, _ := config.MemberAt([]byte(local), []string{"agents", "handoff_tokens"}); string(v) != "400000" {
		t.Errorf("local.json after the rewrite:\n%s", local)
	}

	line, refused, err = RewriteShape(ctx, root, f, "obor config migrate")
	if err != nil || refused != "" || line != "" {
		t.Errorf("RewriteShape again: %q, %q, %v; want nothing to do", line, refused, err)
	}
	if len(f.messages()) != 1 || readShape(root, "config.json") != doc || readShape(root, "local.json") != local {
		t.Errorf("the second run committed or wrote: %q", f.messages())
	}
}

// A refusal of ConfigFile.Read (uncommitted changes) is returned and nothing is written; a commit that fails puts
// local.json back (config.json is Commit's to keep: configFile's restores it).
func TestRewriteShapeRefusedOrFailedWritesNothing(t *testing.T) {
	t.Parallel()
	root, f := shapeRoot(t, migrateConfig, migrateLocal)
	f.refused = ".obor/config.json has uncommitted changes in /x: commit or discard them first"
	line, refused, err := RewriteShape(context.Background(), root, f, "obor serve")
	if err != nil || line != "" || refused != f.refused {
		t.Fatalf("RewriteShape of a dirty file: %q, %q, %v; want the refusal", line, refused, err)
	}
	f.refused, f.failCommit = "", errors.New("git commit: hook said no")
	if _, _, err := RewriteShape(context.Background(), root, f, "obor serve"); err == nil ||
		!strings.Contains(err.Error(), "hook said no") {
		t.Fatalf("RewriteShape past a failing commit: %v, want its error", err)
	}
	if readShape(root, "config.json") != migrateConfig || readShape(root, "local.json") != migrateLocal ||
		len(f.messages()) != 0 {
		t.Errorf("a refused or failed rewrite wrote: commits %q\n%s\n%s", f.messages(), readShape(root, "config.json"),
			readShape(root, "local.json"))
	}
}

// RV-167 #1: on obor-kit's shape-1 pair (config.json and local.json, config's testdata), every setup write (Set*)
// refuses with the migrate hint and neither file changes; after the rewrite both are shape 2 and local.json's
// agents.roles.advisor and check slot overrides load.
func TestSetRefusesShape1UntilTheRewriteMovesBoth(t *testing.T) {
	t.Parallel()
	fixture := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "config", "testdata", "migrate", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cfgDoc, localDoc := fixture("obor-kit.config.json"), fixture("obor-kit.local.json")
	root, f := shapeRoot(t, cfgDoc, localDoc)
	if config.ShapeOf([]byte(cfgDoc)) != 1 || !strings.Contains(localDoc, `"advisor"`) {
		t.Fatal("precondition: the fixtures are obor-kit's shape-1 pair, local.json naming the advisor")
	}
	doc := []byte(cfgDoc)
	for name, set := range map[string]func() (config.Write, error){
		"SetLanes":          func() (config.Write, error) { return config.SetLanes(doc, 5) },
		"SetCommand":        func() (config.Write, error) { return config.SetCommand(doc, "lane_setup", "make deps") },
		"SetRoleModel":      func() (config.Write, error) { return config.SetRoleModel(doc, "lead", "opus") },
		"SetDifficulty":     func() (config.Write, error) { return config.SetDifficulty(doc, map[string][]string{"hard": {"opus"}}) },
		"SetLaneLinks":      func() (config.Write, error) { return config.SetLaneLinks(doc, []string{"x"}) },
		"SetSetupConfirmed": func() (config.Write, error) { return config.SetSetupConfirmed(doc, false) },
		"SetKindCommand":    func() (config.Write, error) { return config.SetKindCommand(doc, "opencode", "x") },
	} {
		if w, err := set(); err == nil || !strings.Contains(err.Error(), "is config shape 1: obor config migrate rewrites it") {
			t.Errorf("%s on shape 1: %s, %v; want refused with the migrate hint", name, w.Doc, err)
		}
	}
	if readShape(root, "config.json") != cfgDoc || readShape(root, "local.json") != localDoc {
		t.Fatal("a refused setup write changed a file")
	}

	if line, refused, err := RewriteShape(context.Background(), root, f, "obor serve"); err != nil || refused != "" || line == "" {
		t.Fatalf("RewriteShape of the pair: %q, %q, %v", line, refused, err)
	}
	local := []byte(readShape(root, "local.json"))
	if config.ShapeOf([]byte(readShape(root, "config.json"))) != config.ShapeVersion {
		t.Errorf("config.json after the rewrite is not shape 2:\n%s", readShape(root, "config.json"))
	}
	if again, err := config.Reshape(local); err != nil || !sameCompact(again, local) ||
		strings.Contains(string(local), `"slots"`) {
		t.Errorf("local.json after the rewrite is not shape 2 (%v):\n%s", err, local)
	}
	cfg, unknown, err := config.LoadHook(root)
	if err != nil || len(unknown) != 0 {
		t.Fatalf("the rewritten pair loads: unknown keys %q, %v; want every key at its home", unknown, err)
	}
	if rc, _ := cfg.Role("advisor"); rc.Type != "fable" {
		t.Errorf("agents.roles.advisor %q, want local.json's fable", rc.Type)
	}
	if n, _ := cfg.HeavyAtOnce(); n != 4 || cfg.CheckSlotDir() != "/tmp/herd-check-slots" {
		t.Errorf("checks.heavy_at_once %d, slot_dir %q; want local.json's 4, /tmp/herd-check-slots", n, cfg.CheckSlotDir())
	}
	if cfg.Shape1() {
		t.Error("the rewritten pair still loads as shape 1")
	}
}
