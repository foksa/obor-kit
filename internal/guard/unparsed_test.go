package guard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"obor/internal/project"
)

// unparsed is root's recorded unparsed Bash calls, oldest first.
func unparsed(t *testing.T, root string) []project.UnparsedCall {
	t.Helper()
	b, err := os.ReadFile(project.UnparsedFile(root))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []project.UnparsedCall
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var c project.UnparsedCall
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			t.Fatalf("record line %q: %v", line, err)
		}
		out = append(out, c)
	}
	return out
}

// a Bash call the parser rejects is allowed and recorded once with its role and the command's first 200 characters; a
// parsable call leaves no record. Both answers are unchanged.
func TestUnparsedBashCallRecorded(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	if b, err := os.ReadFile(project.UnparsedFile(f.P)); err == nil {
		t.Fatalf("precondition: no record yet, got %q", b)
	}
	if code, reason := runAs(f.Project, "worker w1", bgInput(f.L, "go test ./internal/guard", "test", false)); code != 0 || reason != "" {
		t.Fatalf("parsable call: code %d, reason %q, want allow", code, reason)
	}
	if b, err := os.ReadFile(project.UnparsedFile(f.P)); err == nil {
		t.Fatalf("a parsable call was recorded: %q", b)
	}
	cmd := "echo ${(U)foo}; " + strings.Repeat("echo a; ", 30) + "ls"
	if code, reason := runAs(f.Project, "worker w1", bgInput(f.L, cmd, "zsh", false)); code != 0 || reason != "" {
		t.Fatalf("unparsed call: code %d, reason %q, want allow", code, reason)
	}
	calls := unparsed(t, f.P)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v, want one", calls)
	}
	c := calls[0]
	if c.Role != "worker" || c.Agent != "pj-l-01" || c.At == 0 || c.Error == "" {
		t.Errorf("call = %+v", c)
	}
	if c.Command != cmd[:200] {
		t.Errorf("command = %q (%d characters), want the first 200", c.Command, len(c.Command))
	}
}
