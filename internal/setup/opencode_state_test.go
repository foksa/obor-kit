package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/testkit"
)

// T-696: obor setup pane starts an OpenCode lane with XDG_STATE_HOME at the agent's own dir
// (${OBOR_STATE_HOME}/opencode-state/<project>/<agent>), emptied first: a setup start is fresh, so a variant an
// earlier start remembered there is gone; the user's own ~/.local/state/opencode is not touched.
func TestSetupOpencodeStartsWithItsOwnEmptiedStateDir(t *testing.T) {
	t.Parallel()
	x := newFx(t, testkit.Set("lane_types", `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]}}`))
	x.ok(t, "lane", "O-01", "oc")
	cfg, err := config.Load(x.Root)
	if err != nil {
		t.Fatal(err)
	}
	env := x.Env(x.env...)
	dir := filepath.Join(project.StateHome(env), "opencode-state", elem(cfg.ProjectName()), elem("pj-o-01"))
	user := filepath.Join(env.Get("HOME"), ".local", "state", "opencode", "model.json")
	for _, f := range []string{filepath.Join(dir, "model.json"), user} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(`{"variant":{"go/ds":"default"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := x.ok(t, "pane", "O-01", "--tab", "w1:t1")
	if !strings.HasPrefix(last(out), "OK O-01: pj-o-01 in pane ") {
		t.Fatalf("last line %q", last(out))
	}
	calls := strings.Join(x.reqs(), "\n")
	has(t, calls, "XDG_STATE_HOME="+dir, "the split's env")
	hasNot(t, calls, "OBOR_XDG_STATE_WAS", "no saved original (T-698)")
	has(t, calls, "kind=opencode name=pj-o-01", "the start")
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("the state dir %s is not there after the start: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "model.json")); !os.IsNotExist(err) {
		t.Errorf("the dir's model.json survived the fresh start: %v", err)
	}
	if _, err := os.Stat(user); err != nil {
		t.Errorf("the user's own state: %v", err)
	}
}

// elem is a state-dir path element for name (T-698): its lower case, "-" and 8 hex digits of the exact name's sha256.
func elem(name string) string {
	sum := sha256.Sum256([]byte(name))
	return strings.ToLower(name) + "-" + hex.EncodeToString(sum[:4])
}

// T-698: the XDG_STATE_HOME obor setup hands herdr for a new pane follows one rule. An OpenCode agent gets its own dir
// (replacing whatever setup's own env holds); any other kind drops an inherited obor dir (an OpenCode agent running
// setup) and keeps a value of the user's own, which it hands nothing for. A new pane's shell is never typed at.
func TestSetupNewPaneXDGStateHomeRule(t *testing.T) {
	t.Parallel()
	const types = `{"oc":{"kind":"opencode","args":["--auto","--model","go/ds"]},"cl":{"kind":"claude","args":[]}}`
	for _, c := range []struct {
		name, lane, inherited string // inherited: "obor" (a dir under the state home's opencode-state), "mine", ""
		want                  string // "own dir", "dropped", "kept"
	}{
		{"opencode from an inherited obor dir: replaced", "oc", "obor", "own dir"},
		{"claude from an inherited obor dir: dropped", "cl", "obor", "dropped"},
		{"claude from the user's own: kept", "cl", "mine", "kept"},
		{"claude from none: nothing", "cl", "", "kept"},
		{"opencode from the user's own: replaced", "oc", "mine", "own dir"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			x := newFx(t, testkit.Set("lane_types", types))
			home := project.StateHome(x.Env(x.env...))
			inherited := map[string]string{"obor": filepath.Join(home, "opencode-state", "lead-1", "pj-lead-2"), "mine": "/mine/state"}[c.inherited]
			if inherited != "" {
				x.env = append(x.env, "XDG_STATE_HOME="+inherited)
			}
			x.ok(t, "lane", "O-01", c.lane)
			cfg, err := config.Load(x.Root)
			if err != nil {
				t.Fatal(err)
			}
			own := filepath.Join(home, "opencode-state", elem(cfg.ProjectName()), elem("pj-o-01"))
			x.reset()
			out := x.ok(t, "pane", "O-01", "--tab", "w1:t1")
			if !strings.HasPrefix(last(out), "OK O-01: pj-o-01 in pane ") {
				t.Fatalf("last line %q", last(out))
			}
			calls := strings.Join(x.reqs(), "\n")
			has(t, calls, "OBOR_AGENT=pj-o-01", "precondition: the new pane's env")
			if len(x.envLines()) != 0 {
				t.Errorf("a new pane's shell is typed at: %q", x.envLines())
			}
			switch c.want {
			case "own dir":
				has(t, calls, "XDG_STATE_HOME="+own, "the agent's own dir")
				hasNot(t, calls, inherited, "the inherited value")
			case "dropped":
				has(t, calls, "XDG_STATE_HOME=}", "an empty value, which XDG reads as unset (env keys are sorted: it is the last)")
				hasNot(t, calls, inherited, "the inherited obor dir")
			case "kept":
				hasNot(t, calls, "XDG_STATE_HOME", "nothing handed for the user's own or none")
			}
		})
	}
}
