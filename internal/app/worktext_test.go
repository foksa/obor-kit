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
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
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
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
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
	t.Parallel()
	big := "# Role: worker\n" + strings.Repeat("K", roleTextMax)
	p, kit := roleFixture(t, big, "# Worker here\nproject rules\n")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
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
	t.Parallel()
	at := "# Role: worker\n" + strings.Repeat("K", roleTextMax-len("# Role: worker\n"))
	p, kit := roleFixture(t, at, "")
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Read your role text") || !strings.Contains(text, strings.Repeat("K", 64)) {
		t.Errorf("a role text of exactly roleTextMax bytes is not inlined:\n%s", text)
	}
}

// T-721, T-765: the job's prompt_extra is the kind's of the model its dispatch chose (kinds.<kind>.prompt_extra); a
// dispatch recorded before T-721 (no model) carries none.
func TestWorkTextPromptExtraIsTheDispatchsModels(t *testing.T) {
	t.Parallel()
	p, kit := roleFixture(t, "# Role: worker\nkit rules\n", "")
	cfg := `{"name":"pj","paths":{"agents":"doc/roles"},"lanes":1,"kinds":{
		"claude":{"prompt_extra":"Claude extra."},"codex":{"prompt_extra":"Codex extra."}}}`
	writeRoleFile(t, filepath.Join(p.root, ".obor", "config.json"), cfg)
	text, err := workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01", Type: "luna:high"}}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Codex extra.") || strings.Contains(text, "Claude extra.") {
		t.Errorf("a luna:high dispatch: want codex's prompt_extra only:\n%s", text)
	}
	text, err = workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01", Type: "opus"}}, kit)
	if err != nil || !strings.Contains(text, "Claude extra.") || strings.Contains(text, "Codex extra.") {
		t.Errorf("an opus dispatch: want claude's prompt_extra only (%v):\n%s", err, text)
	}
	text, err = workText(p, mtask{task: "T-9", lane: "L-01", disp: dispatchData{Lane: "L-01"}}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Claude extra.") || strings.Contains(text, "Codex extra.") {
		t.Errorf("a dispatch with no model: want no prompt_extra:\n%s", text)
	}
}

// The kit's own prompt template names no role text: the job carries it.
func TestWorkerPromptNamesNoRoleText(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := workerPrompt(config.Fallback(t.TempDir()), kit, "T-9", config.Model{PromptExtra: "Extra."})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"implement the spec of T-9 exactly", "obor spec show T-9", "Extra."} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the rendered prompt has no %q:\n%s", want, prompt)
		}
	}
	for _, no := range []string{"worker.md", "Also read", "{{", "task_done", "commands.check", "Wrong-tab", "AGENTS.md"} {
		if strings.Contains(prompt, no) {
			t.Errorf("the rendered prompt carries %q:\n%s", no, prompt)
		}
	}
}

// T-674: only an empty "## This project" section is dropped from the job; a project's filled one is carried.
func TestWorkTextDropsOnlyAnEmptyProjectSection(t *testing.T) {
	t.Parallel()
	kitRole := "# Role: worker\nkit rules\n\n## This project\n<!-- fill in: kit note -->\n"
	doc := "# Worker here\nproject rules\n\n## This project\n- run make build\n"
	p, kit := roleFixture(t, kitRole, doc)
	text, err := workText(p, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "- run make build") {
		t.Errorf("the project's filled This project section is not in the job:\n%s", text)
	}
	if strings.Contains(text, "fill in") {
		t.Errorf("the kit's empty This project section is in the job:\n%s", text)
	}
	if n := strings.Count(text, "## This project"); n != 1 {
		t.Errorf("## This project appears %d times, want the project's one:\n%s", n, text)
	}
}

// T-674: the work job of this kit (templates/worker-prompt.txt, templates/agents/worker.md and obor-kit's
// docs/agents/worker.md) says each rule once, carries no empty "This project" section and does not tell a worker to
// fetch the job it is reading.
func TestWorkJobSaysEachRuleOnce(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"),
		[]byte(`{"name":"pj","paths":{"agents":"docs/agents"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(kit, "docs", "agents", "worker.md"))
	if err != nil {
		t.Fatal(err)
	}
	writeRoleFile(t, filepath.Join(root, "docs", "agents", "worker.md"), string(doc))
	text, err := workText(&proj{root: root}, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	// Precondition: the job carries the kit's role text and this project's doc, not an empty job.
	for _, want := range []string{"# Role: worker", "obor run check_affected", "implement the spec of T-9 exactly"} {
		if !strings.Contains(text, want) {
			t.Fatalf("precondition: the work job has no %q:\n%s", want, text)
		}
	}
	for _, rule := range []string{"Read AGENTS.md", "test cache on", "commands.check",
		"call `task_done` with your final report", "wrong tab"} {
		if n := strings.Count(text, rule); n != 1 {
			t.Errorf("the rule %q appears %d times, want once:\n%s", rule, n, text)
		}
	}
	for _, no := range []string{"fill in", "## This project", "before anything else", "your first prompt is a job tag",
		"Call the `job_next` tool", "Finish by calling the obor task_done tool"} {
		if strings.Contains(text, no) {
			t.Errorf("the work job carries %q:\n%s", no, text)
		}
	}
}

// workJobText is the work job of a project whose .obor/config.json is cfg, from the kit's shipped templates: the test
// fails when the prompt or the kit's role text is not in it (an empty job would pass every "names no" check below).
func workJobText(t *testing.T, kit, cfg string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := workText(&proj{root: root}, mtask{task: "T-9", lane: "L-01"}, kit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"implement the spec of T-9 exactly", "# Role: worker"} {
		if !strings.Contains(text, want) {
			t.Fatalf("precondition: the work job has no %q:\n%s", want, text)
		}
	}
	return text
}

// T-678: the work job names the project's configured done_file_sections once, not the defaults; with the default
// config, it names the default labels once.
func TestWorkJobNamesTheConfiguredReportSections(t *testing.T) {
	t.Parallel()
	kit, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, cfg, want, not string }{
		{"configured", `{"name":"pj","done_file_sections":["Summary","Verification"]}`,
			"Your final report has these sections: Summary, Verification.",
			"Summary, Self-check, Skipped, Open questions"},
		{"default", `{"name":"pj"}`,
			"Your final report has these sections: Summary, Self-check, Skipped, Open questions.", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			text := workJobText(t, kit, c.cfg)
			if n := strings.Count(text, c.want); n != 1 {
				t.Errorf("the job names the sections %d times, want once (%q):\n%s", n, c.want, text)
			}
			if c.not != "" && strings.Contains(text, c.not) {
				t.Errorf("the job names %q, not the wanted sections:\n%s", c.not, text)
			}
		})
	}
}

// The fix job names the role texts for a worker started fresh (T-603): the kit's and the project's when the checkout has
// one, the kit's alone when it does not.
func TestFixJobNamesBothRoleTexts(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, doc string
		want      func(p *proj, kit string) string
		absent    bool // the checkout has no project doc to name
	}{
		{"both", "# Worker here\nproject rules\n", func(p *proj, kit string) string {
			return "If this session did not get the work job (a fresh start), read " +
				filepath.Join(kit, "templates", "agents", "worker.md") + " and " +
				filepath.Join(p.root, "doc/roles/worker.md") + " first."
		}, false},
		{"kit alone", "", func(_ *proj, kit string) string {
			return "read " + filepath.Join(kit, "templates", "agents", "worker.md") + " first."
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, kit := roleFixture(t, "# Role: worker\nkit\n", c.doc)
			read, err := fixRead(p, kit)
			if err != nil {
				t.Fatal(err)
			}
			text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}}, store.SpecRow{}, read)
			if !strings.Contains(text, c.want(p, kit)) {
				t.Errorf("the fix job has no %q:\n%s", c.want(p, kit), text)
			}
			if c.absent && strings.Contains(text, p.root) {
				t.Errorf("the fix job names a project doc the checkout lacks:\n%s", text)
			}
		})
	}
}

// T-819: a fix job whose worker never had the spec's rev carries it (a change made while the task was not working);
// at the rev it already has, it does not.
func TestFixJobCarriesAChangedRevOnly(t *testing.T) {
	t.Parallel()
	row := store.SpecRow{Task: "T-9", Rev: 2, Text: "# T-9 — W\n\nDifficulty: medium\n"}
	text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}, specRev: 1}, row,
		"read it.")
	if !strings.Contains(text, "The spec of T-9 (rev 2") {
		t.Errorf("a fix job of a worker at rev 1: %q; want the rev 2 spec", text)
	}
	if text := fixText(mtask{task: "T-9", attempt: 2, attemptVars: attemptVars{line: "add a test"}, specRev: 2}, row,
		"read it."); strings.Contains(text, "The spec of T-9") {
		t.Errorf("a fix job at the spec's rev repeats it: %q", text)
	}
}
