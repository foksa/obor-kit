//go:build integration

package main

import (
	"reflect"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// bash config.test.sh case_validate_* through the binary: OK exit 0; WARN lines then OK; FAIL lines exit 1; a removed key
// past its two versions is an unknown key, so the load refuses it (T-830: cache.warn_tokens).
func TestConfigValidateBinary(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Lane("L-01", "")
	for _, c := range []struct {
		edits []func(map[string]any)
		rc    int
		out   string
	}{
		{nil, 0, "OK\n"},
		{[]func(map[string]any){testkit.Set("cache.warn_tokens", 50000)}, 1, ""},
		{[]func(map[string]any){testkit.Set("version", 2), testkit.Set("paths.research", "r")}, 1,
			"WARN paths.research: no longer used, remove it\nFAIL .version: must be 1, got 2\n"},
		// T-966: merge.receipt_max_age went with the test receipts; naming it is one WARN line, not an error.
		{[]func(map[string]any){testkit.Set("merge.receipt_max_age", 60)}, 0,
			"WARN merge.receipt_max_age: no longer used, remove it\nOK\n"},
		// T-428: serve dispatches and merges nothing without roles.integrator, so validate FAILs.
		{[]func(map[string]any){func(c map[string]any) { delete(c, "roles") }}, 1,
			"FAIL .roles.integrator: required: serve dispatches and merges nothing without an integrator\n"},
	} {
		// The fixture carries the integrator serve needs (T-428); these cases are about the other lines.
		p.Config(append([]func(map[string]any){testkit.Set("roles.integrator", "")}, c.edits...)...)
		if rc, stdout, stderr := run(t, p.Root, nil, "config", "validate"); rc != c.rc || stdout != c.out {
			t.Errorf("validate: exit %d, %q (%s); want %d, %q", rc, stdout, stderr, c.rc, c.out)
		}
	}
}

func TestWhoami(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-02")
	p.Config()

	// bash config.test.sh case_whoami_unarmed
	text, j := whoamiIn(t, lane)
	if text != "role: worker\nagent: pj-l-01\nlane: L-01\nsource: cwd\nlane row: none\n" {
		t.Errorf("unarmed lane: %q", text)
	}
	if want := map[string]any{"role": "worker", "agent": "pj-l-01", "lane": "L-01", "source": "cwd"}; !reflect.DeepEqual(j, want) {
		t.Errorf("unarmed --json: %v", j)
	}
	if text, _ := whoamiIn(t, p.Root, "OBOR_ROLE=lead"); !strings.Contains(text, "role: lead\n") || !strings.Contains(text, "source: env\n") {
		t.Errorf("OBOR_ROLE=lead: %q", text)
	}
	if text, j := whoamiIn(t, p.Root); !strings.Contains(text, "role: none\n") || strings.Contains(text, "agent:") || j["agent"] != nil {
		t.Errorf("main checkout: %q, no agent line", text)
	}
	if rc, _, _ := run(t, p.Root, nil, "whoami", "--bogus"); rc != 2 {
		t.Errorf("whoami --bogus: exit %d, want 2", rc)
	}

	// bash config.test.sh case_whoami_armed; T-496: no allowed globs, only the protected files the stored spec
	// (T-440) unlocks; a spec committed in git counts for nothing.
	p.Write("docs/tasks/T-02.md", "# T-02\n\n## Protected files this task may edit\n`git/only.md`\n")
	p.Git(p.Root, "add", "docs/tasks/T-02.md")
	p.Git(p.Root, "commit", "-q", "-m", "spec T-02 in git")
	p.Spec("T-02", "# T-02\n\n## Files you may touch\n`lib/obor.sh`\n\n## Protected files this task may edit\n"+
		"`docs/design.md`, `docs/*.md` (a glob unlocks nothing)\n\n## Tests\n`not/this.sh`\n")
	p.State("L-01", `{"lane":"L-01","task":"T-02","branch":"task/T-02","agent":"pj-l1","base":"main",
		"dispatched_at":1,"allowed":["stale/copy.sh"],"blocks":0}`)
	text, _ = whoamiIn(t, lane, "OBOR_ROLE=worker", "OBOR_LANE=L-01")
	if want := "role: worker\nagent: pj-l-01\nlane: L-01\nsource: env+cwd\ntask: T-02\nbranch: task/T-02\nbase: main\nspec: rev 1\n" +
		"protected file unlocked: docs/design.md\n"; text != want {
		t.Errorf("armed: %q, want %q", text, want)
	}
	_, j = whoamiIn(t, lane)
	if want := map[string]any{"role": "worker", "agent": "pj-l-01", "lane": "L-01", "source": "cwd", "task": "T-02",
		"branch": "task/T-02", "base": "main", "protected": []any{"docs/design.md"}, "rev": 1.0}; !reflect.DeepEqual(j, want) {
		t.Errorf("armed --json: %v", j)
	}
	p.Spec("T-02", "# T-02\n\nno protected files\n")
	if _, j := whoamiIn(t, lane); j["protected"] != nil || j["rev"] != 2.0 {
		t.Errorf("new rev not shown: %v rev %v", j["protected"], j["rev"])
	}
	p.State("L-01", `{"lane":"L-01","task":"T-09","branch":"task/T-09","base":"main","blocks":0}`)
	if text, j := whoamiIn(t, lane); !strings.Contains(text, "spec: none in obor serve's store\n") || j["rev"] != nil {
		t.Errorf("no spec: %q %v", text, j)
	}
	p.Write(".obor/state/obor.db", "not a database\n")
	if rc, _, stderr := run(t, lane, nil, "whoami"); rc != 1 || !strings.Contains(stderr, "obor.db") {
		t.Errorf("unreadable lanes table: exit %d, %q", rc, stderr)
	}
}

// T-801: whoami in a custom role's env says its role and derived name (a default row's too); once its row is removed the
// role derives no name (no agent line).
func TestWhoamiCustomRole(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Write("docs/agents/ux_buddy.md", "# Role: ux_buddy\n")
	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.ux_buddy", `{"model": "opus", "write": ["docs/ux/"]}`),
		testkit.Set("roles.researcher", "sonnet"))
	text, j := whoamiIn(t, p.Root, "OBOR_ROLE=ux_buddy")
	if text != "role: ux_buddy\nagent: pj-ux-buddy\nsource: env\nlane row: none\n" || j["agent"] != "pj-ux-buddy" || j["role"] != "ux_buddy" {
		t.Errorf("custom role: %q %v", text, j)
	}
	if text, _ := whoamiIn(t, p.Root, "OBOR_ROLE=researcher"); !strings.Contains(text, "role: researcher\nagent: pj-researcher\n") {
		t.Errorf("default row: %q", text)
	}
	if rc, out, _ := run(t, p.Root, nil, "config", "validate"); rc != 0 || !strings.Contains(out, "OK") {
		t.Errorf("config validate: exit %d, %q", rc, out)
	}
	// No push for a custom role, even with an allow_push naming it (validate fails that; the pre-push hook asks this).
	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.ux_buddy", `{"model": "opus", "write": ["docs/ux/"]}`),
		testkit.Set("roles.researcher", "sonnet"), testkit.Set("guards.allow_push", `["lead", "ux_buddy"]`))
	if rc, _, _ := run(t, p.Root, []string{"OBOR_ROLE=lead"}, "whoami", "--can-push"); rc != 0 {
		t.Errorf("precondition: the listed lead may push: exit %d", rc)
	}
	if rc, _, _ := run(t, p.Root, []string{"OBOR_ROLE=ux_buddy"}, "whoami", "--can-push"); rc != 1 {
		t.Errorf("a custom role listed in allow_push: whoami --can-push exit %d, want 1", rc)
	}
	p.Config(testkit.Set("roles.integrator", "opus"), testkit.Set("roles.researcher", "sonnet"))
	if text, _ := whoamiIn(t, p.Root, "OBOR_ROLE=ux_buddy"); strings.Contains(text, "agent:") {
		t.Errorf("row removed: %q, want no agent line", text)
	}
	if rc, out, _ := run(t, p.Root, nil, "config", "validate"); rc != 0 ||
		!strings.Contains(out, "WARN .roles.ux_buddy: docs/agents/ux_buddy.md is a role doc but roles.ux_buddy is not set") {
		t.Errorf("row removed: config validate exit %d, %q; want OK with the doc's WARN", rc, out)
	}
}
