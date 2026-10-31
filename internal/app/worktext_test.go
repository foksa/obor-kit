package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"obor/internal/config"
	"obor/internal/store"
)

// T-603: the work job carries the worker's role texts, so the worker reads them once (in the job) instead of spending
// turns on the files, and a role text too big to carry is named for the worker to read.

// roleFixture is a project root whose config puts the agents docs in doc/roles, and the obor kit it is dispatched from:
// a worker prompt template and the kit's worker role text (kitRole).
func roleFixture(t *testing.T, kitRole, doc string) (*proj, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"name":"pj","paths":{"agents":"doc/roles"}}`
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if doc != "" {
		writeRoleFile(t, filepath.Join(root, "doc/roles/worker.md"), doc)
	}
	kit := t.TempDir()
	writeRoleFile(t, filepath.Join(kit, "templates", "worker-prompt.txt"), "Implement {{task}} on {{base}}.\n"+
		"Report: {{done_sections}}.\n")
	writeRoleFile(t, filepath.Join(kit, "templates", "agents", "worker.md"), kitRole)
	return &proj{root: root}, kit
}

// writeRoleFile writes text to path, creating its directories.
func writeRoleFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWorkTextCarriesBothRoleTexts(t *testing.T) {
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "O-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Implement T-9 on main.", "# Role: worker\nkit rules", "# Worker here\nproject rules",
		filepath.Join(kit, "templates", "agents", "worker.md"), filepath.Join(p.root, "doc/roles/worker.md")} {
		if !strings.Contains(text, want) {
			t.Errorf("work text has no %q:\n%s", want, text)
		}
	}
	for _, no := range []string{"Read AGENTS.md and your role text", "Also read", "{{"} {
		if strings.Contains(text, no) {
			t.Errorf("work text carries %q (the roles are in the job, not a read line):\n%s", no, text)
		}
	}
}

func TestWorkTextSkipsAMissingProjectDoc(t *testing.T) {
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "")
	text, err := workText(p, mtask{task: "T-9", lane: "O-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "kit rules") {
		t.Errorf("the kit's role text is not in the job:\n%s", text)
	}
	if strings.Contains(text, filepath.Join(p.root, "doc/roles/worker.md")) || strings.Contains(text, "This project's") {
		t.Errorf("a project worker doc the checkout lacks is in the job:\n%s", text)
	}
}

func TestWorkTextNamesARoleTextTooLargeToCarry(t *testing.T) {
	big := "# Role: worker\n" + strings.Repeat("K", roleTextMax)
	p, kit := roleFixture(t, big, "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "O-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(kit, "templates", "agents", "worker.md")
	if !strings.Contains(text, "Read your role text before you act: "+path+" (too large to carry here).") {
		t.Errorf("no read line naming the oversized role text %s:\n%s", path, text)
	}
	if strings.Contains(text, strings.Repeat("K", 64)) {
		t.Error("a role text over roleTextMax is inlined")
	}
	if !strings.Contains(text, "project rules") {
		t.Errorf("the role text under the cap is not inlined:\n%s", text)
	}
}

func TestRoleTextAtTheCapIsInlined(t *testing.T) {
	at := "# Role: worker\n" + strings.Repeat("K", roleTextMax-len("# Role: worker\n"))
	p, kit := roleFixture(t, at, "")
	text, err := workText(p, mtask{task: "T-9", lane: "O-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Read your role text") || !strings.Contains(text, strings.Repeat("K", 64)) {
		t.Errorf("a role text of exactly roleTextMax bytes is not inlined:\n%s", text)
	}
}

// The kit's own prompt template names no role text: the job carries it.
func TestWorkerPromptNamesNoRoleText(t *testing.T) {
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := workerPrompt(config.Fallback(t.TempDir()), kit, "T-9", config.LaneType{PromptExtra: "Extra."})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Read AGENTS.md", "implement the spec of T-9 exactly", "Summary, Self-check", "Extra."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the rendered prompt has no %q:\n%s", want, prompt)
		}
	}
	for _, no := range []string{"worker.md", "Also read", "{{"} {
		if strings.Contains(prompt, no) {
			t.Errorf("the rendered prompt carries %q:\n%s", no, prompt)
		}
	}
}

// The fix job names the role texts for a worker started fresh, both paths (T-603, fix job: the texts are not repeated).
func TestFixJobNamesBothRoleTexts(t *testing.T) {
	p, kit := roleFixture(t, "# Role: worker\nkit\n", "# Worker here\nproject rules\n")
	read, err := fixRead(p, kit)
	if err != nil {
		t.Fatal(err)
	}
	text := fixText(mtask{task: "T-9", attempt: 2, line: "add a test"}, store.SpecRow{}, read)
	want := "If this session did not get the work job (a fresh start), read " +
		filepath.Join(kit, "templates", "agents", "worker.md") + " and " +
		filepath.Join(p.root, "doc/roles/worker.md") + " first."
	if !strings.Contains(text, want) {
		t.Errorf("the fix job has no %q:\n%s", want, text)
	}
}

func TestFixJobNamesTheKitRoleTextAloneWithoutAProjectDoc(t *testing.T) {
	p, kit := roleFixture(t, "# Role: worker\nkit\n", "")
	read, err := fixRead(p, kit)
	if err != nil {
		t.Fatal(err)
	}
	text := fixText(mtask{task: "T-9", attempt: 2, line: "add a test"}, store.SpecRow{}, read)
	want := "read " + filepath.Join(kit, "templates", "agents", "worker.md") + " first."
	if !strings.Contains(text, want) {
		t.Errorf("the fix job has no %q:\n%s", want, text)
	}
	if strings.Contains(text, p.root) {
		t.Errorf("the fix job names a project doc the checkout lacks:\n%s", text)
	}
}
