package setup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
)

// initFx is a bare project (no config) for obor init: a fake `claude` (and, with codex, a fake `codex`) first on the
// runs' PATH, a herdr with no pane, and a Serve that counts its calls. The fake claude logs its argv to claude.log; `plugin install <p>`
// enables <p> in the project's .claude/settings.json unless <bin>/fail exists (exit 7).
type initFx struct {
	*fx
	bin, log string
	serves   int
}

func newInitFx(t *testing.T, codex bool) *initFx {
	t.Helper()
	return initFxOn(t, bare(t), codex)
}

// newGitInitFx is newInitFx in a git repository (bareGit): for the integration tests of what obor init leaves in git.
func newGitInitFx(t *testing.T, codex bool) *initFx {
	t.Helper()
	return initFxOn(t, bareGit(t), codex)
}

// initFxOn is newInitFx on the project of f.
func initFxOn(t *testing.T, f *fx, codex bool) *initFx {
	t.Helper()
	x := &initFx{fx: f}
	x.h.Panes = nil // no tab of its own: the fake numbers new tabs t1, t2 … (the caller's tab is none)
	x.bin = filepath.Join(x.Base, "fakebin")
	x.log = filepath.Join(x.bin, "claude.log")
	script := `#!/bin/sh
d=${0%/*}
echo "$*" >> "$d/claude.log"
case "$*" in
"plugin install "*)
	[ -e "$d/fail" ] && exit 7
	mkdir -p .claude
	printf '{"enabledPlugins":{"%s":true}}' "$3" > .claude/settings.json ;;
esac
`
	if err := os.MkdirAll(x.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	x.Write(filepath.Join(x.bin, "claude"), script)
	os.Chmod(filepath.Join(x.bin, "claude"), 0o755)
	if codex {
		x.Write(filepath.Join(x.bin, "codex"), "#!/bin/sh\nexit 0\n")
		os.Chmod(filepath.Join(x.bin, "codex"), 0o755)
	}
	x.env = append(x.env, "PATH="+x.bin+":/usr/bin:/bin")
	x.serve = func(context.Context, *config.Config) (string, string, error) {
		x.serves++
		if x.serves > 1 {
			return "already", "obor.serve.proj installed and loaded", nil
		}
		return "done", "INSTALLED obor.serve.proj", nil
	}
	return x
}

func (x *initFx) init(t *testing.T, args ...string) string {
	t.Helper()
	return x.ok(t, append([]string{"obor-init"}, args...)...)
}

func (x *initFx) claudeCalls() string { b, _ := os.ReadFile(x.log); return string(b) }

// summary is the step line of name in out ("" when none): "  name  state  what".
func summary(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 1 && strings.HasPrefix(l, "  ") && f[0] == name {
			return strings.Join(f[1:], " ")
		}
	}
	return ""
}

func stateOf(out, name string) string {
	f := strings.Fields(summary(out, name))
	return append(f, "")[0]
}

func (x *initFx) agents(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, p := range x.h.Panes {
		if p.Name != "" {
			got[p.Name] = p.Tab
		}
	}
	return got
}

// herdr down: FAIL first, before any file is written or claude is called.
func TestOborInitFailsFirstWhenHerdrIsDown(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	x.srv.Die()
	out, rc := x.run(t, "obor-init")
	if rc != 1 || !strings.HasPrefix(last(out), "FAIL herdr is not reachable (") ||
		!strings.HasSuffix(last(out), "): start herdr, then obor init again") {
		t.Fatalf("exit %d, last line %q\n%s", rc, last(out), out)
	}
	if x.Exists(".obor/config.json") || x.Exists("BACKLOG.md") || x.claudeCalls() != "" {
		t.Errorf("wrote or called claude before the FAIL:\n%s", out)
	}
}

// no claude on PATH: FAIL (the lead needs it), nothing written.
func TestOborInitFailsWithoutClaude(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	os.Remove(filepath.Join(x.bin, "claude"))
	x.failing(t, "FAIL claude is not on PATH (the lead runs it): install it, then obor init again", "obor-init")
	if x.Exists(".obor/config.json") || x.Exists("BACKLOG.md") || len(x.h.Plugins) != 0 {
		t.Errorf("wrote before the FAIL")
	}
}

// One row per detection: a Makefile check: target, go.mod, package.json with each lockfile and script, nothing.
func TestDetectCommands(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name             string
		files            map[string]string
		check, laneSetup string
	}{
		{"makefile check", map[string]string{"Makefile": "build:\n\tgo build\ncheck: build\n\tgo test\n", "go.mod": "module x\n"}, "make check", ""},
		{"makefile without check", map[string]string{"Makefile": "build:\n\tgo build\nprecheck:\n"}, "", ""},
		{"go.mod", map[string]string{"go.mod": "module x\n"}, "go test ./...", ""},
		{"bun.lockb", map[string]string{"package.json": `{"scripts":{"check":"tsc","test":"x"}}`, "bun.lockb": ""}, "bun run check", "bun install --frozen-lockfile"},
		{"bun.lock", map[string]string{"package.json": `{"scripts":{"test":"x"}}`, "bun.lock": ""}, "bun run test", "bun install --frozen-lockfile"},
		{"pnpm", map[string]string{"package.json": `{"scripts":{"test":"x"}}`, "pnpm-lock.yaml": ""}, "pnpm run test", "pnpm install --frozen-lockfile"},
		{"npm lock", map[string]string{"package.json": `{"scripts":{"check":"x"}}`, "package-lock.json": "{}"}, "npm run check", "npm ci"},
		{"no lockfile, no script", map[string]string{"package.json": `{}`}, "", "npm install"},
		{"makefile and package.json", map[string]string{"Makefile": "check:\n", "package.json": `{"scripts":{"test":"x"}}`, "pnpm-lock.yaml": ""}, "make check", "pnpm install --frozen-lockfile"},
		{"nothing", map[string]string{"README.md": "hi"}, "", ""},
	} {
		dir := t.TempDir()
		for f, body := range c.files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		d := detectCommands(dir)
		ready := ""
		if c.laneSetup != "" {
			ready = "test -d node_modules"
		}
		if d.check != c.check || d.laneSetup != c.laneSetup || d.laneReady != ready {
			t.Errorf("%s: check %q lane_setup %q lane_ready %q, want %q %q %q", c.name, d.check, d.laneSetup, d.laneReady,
				c.check, c.laneSetup, ready)
		}
		if (len(d.from) == 0) != (c.name == "nothing" || c.name == "makefile without check") {
			t.Errorf("%s: from %v", c.name, d.from)
		}
	}
}

// obor init on an existing config.json this obor cannot load (shape 1: not read) is refused with
// the load's own error before it writes anything (it would edit the config in place): the file as it was, no init file,
// claude not called.
func TestOborInitRefusesAnUnloadableConfig(t *testing.T) {
	t.Parallel()
	x := newInitFx(t, true)
	shape1 := `{"version":1,"name":"proj","prefix":"pj","lanes":0}` + "\n"
	x.Write(".obor/config.json", shape1)
	out, rc := x.run(t, "obor-init")
	want := "/.obor/config.json: config shape 1, which this obor no longer reads: run obor config migrate with obor " +
		"v0.33, then upgrade (docs/UPGRADE-v0.33.md lists old and new keys)"
	if rc != 1 || !strings.HasPrefix(last(out), "FAIL ") || !strings.HasSuffix(last(out), want) {
		t.Fatalf("exit %d, last line %q, want the load's shape-1 FAIL\n%s", rc, last(out), out)
	}
	if x.Read(".obor/config.json") != shape1 || x.Exists("BACKLOG.md") || x.claudeCalls() != "" {
		t.Errorf("wrote or called claude before the FAIL:\n%s", out)
	}
}
