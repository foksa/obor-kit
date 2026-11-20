//go:build integration

package herdrapp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/herdrapp"
)

// script writes an executable stub CLI answering with body, and returns its path.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("precondition: the stub %s is not executable: %v", path, err)
	}
	return path
}

// TestClientRunsCommand is the CLI path itself, with Client.Run nil: a stub herdr-app on disk answers `--version`,
// `ls` and `do` as the app does, and a closed window is the typed error the caller places by labels on.
func TestClientRunsCommand(t *testing.T) {
	dir := t.TempDir()
	const ls = `{"spaces":[{"id":"spa1","name":"obor-kit","folder":"/work/obor-kit","machine":null,
"active":true,"tabs":[{"id":"tb1","name":"1","active":true,"settled":false,"terminals":[]}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "ls.json"), []byte(ls), 0o644); err != nil {
		t.Fatal(err)
	}
	live := script(t, dir, "herdr-app", `#!/bin/sh
case "$1" in
  --version) echo "herdr-app 0.1.16";;
  ls) cat "$(dirname "$0")/ls.json";;
  do) echo '{"space":"sp1","tab":"tb1"}';;
esac
`)
	c := &herdrapp.Client{Command: live}
	spaces, err := c.Spaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(spaces) != 1 || spaces[0].ID != "spa1" || spaces[0].Name != "obor-kit" || len(spaces[0].Tabs) != 1 {
		t.Fatalf("Spaces = %+v, want the stub's one space", spaces)
	}
	tab, err := c.EnsureTab(context.Background(), "spa1", "lead", "/p")
	if err != nil {
		t.Fatal(err)
	}
	if tab.ID != "tb1" {
		t.Errorf("EnsureTab = %+v, want the stub's tb1", tab)
	}

	closed := script(t, dir, "closed", `#!/bin/sh
echo "herdr-app: no Herdr window at $HOME/herdr-app.sock: No such file or directory (os error 2)" >&2
exit 2
`)
	if _, err := (&herdrapp.Client{Command: closed}).Spaces(context.Background()); !port.NotRunning(err) {
		t.Errorf("Spaces with no window = %v, want port.NotRunning", err)
	}
	missing := filepath.Join(dir, "not-there")
	if _, err := (&herdrapp.Client{Command: missing}).Spaces(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "not-there") {
		t.Errorf("Spaces with no CLI = %v, want an error naming it", err)
	}

	refusing := script(t, dir, "refusing", `#!/bin/sh
case "$1" in
  --version) echo "herdr-app 0.1.16";;
  ls) echo '{"spaces":[]}';;
  do) echo '{"error":"folder /work/p is not there"}'; exit 1;;
esac
`)
	if _, err := (&herdrapp.Client{Command: refusing}).EnsureSpace(context.Background(), "p", "/work/p"); err == nil ||
		!strings.Contains(err.Error(), "folder /work/p is not there") {
		t.Errorf("EnsureSpace of a refusing app = %v, want its error reply", err)
	}

	slow := script(t, dir, "slow", "#!/bin/sh\nsleep 5\n")
	if _, err := (&herdrapp.Client{Command: slow, Timeout: 100 * time.Millisecond}).Spaces(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "timed out") {
		t.Errorf("Spaces of a hung CLI = %v, want a timeout", err)
	}
}
