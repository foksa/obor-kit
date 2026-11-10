package guard

import (
	"os"
	"strings"
	"testing"

	"obor/internal/project"
	"obor/internal/testkit"
)

// brokenConfigs are the ways .obor/config.json exists but does not load (every route, beside an unknown key).
var brokenConfigs = []struct {
	name, file, text string
}{
	{"a syntax error in config.json", ".obor/config.json", `{"version": 1,`},
	{"a syntax error in local.json", ".obor/local.json", `{"lanes.dir": `},
	{"a type error", ".obor/local.json", `{"nightly": {"at": 5}}`}, // shape 2, as the fixture's config.json
}

// unknownKey adds keys this binary does not know (keep_warm is one such retired key) to the fixture's config.
func unknownKey(f *testkit.Project, protected string) {
	f.Config(testkit.Set("rules.protected_files", protected), testkit.Set("keep_warm", `{"lanes": 1}`),
		testkit.Set("merge.future", 1))
}

// PreToolUse: an unknown config key is ignored (the guard runs on the rest of the config); a config that
// does not load fails closed: every session but a lead (and a role-less one in the main checkout) is denied writes in
// the repo and git writes, reads and writes outside the repo stay allowed, and git push, pkill and headless runs stay
// denied for everyone.
func TestConfigUnreadableFailsClosed(t *testing.T) {
	t.Parallel()
	const why = "config unreadable"
	f := fixture(t)
	P, L, scratch := f.P, f.L, f.Base+"/scratch/x.txt"
	// Precondition: a clean config allows the writes the broken rows deny.
	check(t, f.Project, []tc{
		edit("worker", L, "Edit", L+"/src/a/x.go", ""),
		edit("integrator", P, "Edit", P+"/README.md", ""),
	})

	unknownKey(f.Project, `["src/core/types.ts"]`)
	check(t, f.Project, []tc{
		edit("worker", L, "Edit", L+"/src/a/x.go", ""),
		bash("worker", L, "echo x > src/core/types.ts", "protected file"),
		bash("worker", L, "git commit -m x", "workers don't run git commit"),
		edit("integrator", P, "Edit", P+"/README.md", ""),
		bash("lead", P, "git push", "git push is blocked"),
	})

	for _, b := range brokenConfigs {
		t.Run(b.name, func(t *testing.T) {
			f.Config(testkit.Set("rules.protected_files", `["src/core/types.ts"]`))
			os.Remove(f.Path(".obor/local.json"))
			f.Write(b.file, b.text)
			check(t, f.Project, []tc{
				edit("worker", L, "Edit", L+"/src/a/x.go", why),
				edit("worker", L, "Write", P+"/README.md", why),
				bash("worker", L, "echo x > src/a/x.txt", why),
				bash("worker", L, "cp README.md src/a/", why),
				bash("worker", L, "git commit -m x", why),
				bash("worker", L, "git status && git diff && obor config validate", ""),
				edit("worker", L, "Write", scratch, ""),
				bash("worker", L, "echo x > "+scratch, ""),
				edit("-", L, "Edit", L+"/src/a/x.go", why), // a worker by its cwd, on the default lanes.dir
				edit("integrator", P, "Edit", P+"/README.md", why),
				bash("integrator", P, "git merge task/T-01", why),
				edit("reviewer", P, "Write", P+"/docs/reviews/RV-1.md", why),
				edit("lead", P, "Edit", P+"/README.md", ""),
				bash("lead", P, "git commit -m x", ""),
				edit("-", P, "Edit", P+"/README.md", ""),
				bash("lead", P, "git push", "git push is blocked"),
				bash("-", P, "pkill -f claude", "pkill"),
				bash("lead", P, "codex exec hi", "codex exec"),
				bash("lead", P, "claude -p hi", "claude -p"),
			})
		})
	}
}

// SessionStart: an unknown key is one WARN line naming it, then the usual context; a config that does not
// load is one line saying so (no role line: nothing is guarded on the default config's view); its default config
// derives no name, so the last readable session's inbox record stays as it was (never one under a wrong name).
func TestSessionConfigUnknownKeyAndUnreadable(t *testing.T) {
	t.Parallel()
	f, o1 := sessionFx(t, true)
	env := []string{"OBOR_STATE_HOME=" + f.Base + "/state",
		"CLAUDE_CODE_MESSAGING_SOCKET=/tmp/cc-socks/1.sock", "CLAUDE_CODE_MESSAGING_TOKEN=tok-1"}
	out := f.session(t, o1, "sid-0", env...)
	contains(t, out, "clean config", "obor: role worker, lane L-01, task T-05")
	lacks(t, out, "clean config", "WARN", "config unreadable")

	unknownKey(f.Project, `[]`)
	out = f.session(t, o1, "sid-1", env...)
	contains(t, out, "unknown key", "obor: WARN:", `keep_warm`, `merge.future`, "obor config validate",
		"obor: role worker, lane L-01, task T-05", "Protected files this task may edit: docs/design.md")
	lacks(t, out, "unknown key", "config unreadable")

	for i, b := range brokenConfigs {
		f.Config()
		os.Remove(f.Path(".obor/local.json"))
		f.Write(b.file, b.text)
		sid := "sid-broken-" + string(rune('a'+i))
		out := f.session(t, o1, sid, env...)
		contains(t, out, b.name, "obor: config unreadable: ", "obor config validate")
		lacks(t, out, b.name, "obor: role ", "Hooks:")
		if strings.Count(out, "\n") != 1 {
			t.Errorf("%s: want one line, got\n%s", b.name, out)
		}
		in, err := project.ReadInbox(f.Root, "L-01")
		if err != nil || in != (project.Inbox{Socket: "/tmp/cc-socks/1.sock", Token: "tok-1", Session: "sid-1"}) {
			t.Errorf("%s: .inbox = %+v, %v; want the last readable session's (sid-1)", b.name, in, err)
		}
	}
}

// UserPromptSubmit: the wrong-tab guard still asks with an unknown key (and says nothing of it) and with a
// config that does not load (on the default config: the lane by its cwd).
func TestPromptConfigUnknownKeyAndUnreadable(t *testing.T) {
	t.Parallel()
	f, o1 := promptFx(t, true)
	f.guarded(t, o1, "", "please fix the login bug") // precondition
	unknownKey(f.Project, `[]`)
	if c := f.prompt(t, o1, "please fix the login bug", ""); !strings.Contains(c, guardLine) || strings.Contains(c, "keep_warm") {
		t.Errorf("unknown key: context %q, want the guard and no word of the key", c)
	}
	for _, b := range brokenConfigs {
		f.Config()
		os.Remove(f.Path(".obor/local.json"))
		f.Write(b.file, b.text)
		if c := f.prompt(t, o1, "please fix the login bug", ""); !strings.Contains(c, "I am worker L-01 on T-05. Did you mean to type here?") {
			t.Errorf("%s: context %q, want the guard (no prefix to derive the agent from: the lane)", b.name, c)
		}
	}
}

// the hooks line names one headless form per registered kind, from its bridge (xkind: by its program).
func TestHeadlessFormsFromTheBridges(t *testing.T) {
	if got := strings.Join(headlessForms(), ", "); got != "claude -p, codex exec, opencode run, x-agent -p" {
		t.Errorf("headlessForms = %q", got)
	}
}
