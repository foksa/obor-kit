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
	dir := filepath.Join(project.StateHome(env), "opencode-state", cfg.ProjectName(), "pj-o-01")
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
