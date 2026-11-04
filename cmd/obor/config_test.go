package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// bash config.test.sh case_config_no_config, case_config_print_and_get: exit 3 without a config; the effective config
// (local over config over defaults), one value, from a lane the main checkout's config; null prints nothing; a missing
// key is FAIL on stderr, exit 1; usage errors exit 2.
func TestConfigPrintAndGet(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	if rc, _, stderr := run(t, p.Root, nil, "config"); rc != 3 || !strings.Contains(stderr, "no "+p.Root+"/.obor/config.json") {
		t.Errorf("config without .obor/config.json: exit %d, %q", rc, stderr)
	}
	if rc, _, _ := run(t, p.Root, nil, "whoami"); rc != 3 {
		t.Errorf("whoami without .obor/config.json: exit %d", rc)
	}
	lane := p.Lane("L-01", "task/T-01")
	p.Config()
	p.Write(".obor/local.json", `{"merge": {"receipt_max_age": 60}}`)

	rc, stdout, _ := run(t, p.Root, nil, "config")
	var eff struct {
		Prefix string `json:"prefix"`
		Merge  struct {
			ReceiptMaxAge int64 `json:"receipt_max_age"`
		} `json:"merge"`
	}
	if err := json.Unmarshal([]byte(stdout), &eff); rc != 0 || err != nil || eff.Prefix != "pj" || eff.Merge.ReceiptMaxAge != 60 {
		t.Errorf("config: exit %d, %v\n%s", rc, err, stdout)
	}
	if !strings.Contains(stdout, `"task_id_pattern": "T-[0-9]+[a-z]?"`) {
		t.Errorf("config lacks the default task_id_pattern:\n%s", stdout)
	}
	for _, c := range []struct {
		dir  string
		args []string
		rc   int
		out  string
	}{
		{p.Root, []string{"config", "get", ".prefix"}, 0, "pj\n"},
		{lane, []string{"config", "get", ".merge.receipt_max_age"}, 0, "60\n"},
		{p.Root, []string{"config", "get", ".notify"}, 0, ""}, // a known field, unset: null prints nothing (obsolete, T-465)
		{p.Root, []string{"config", "get", ".nosuch"}, 1, ""},
		{p.Root, []string{"config", "get", ".mcp.jobs"}, 0, ""}, // a known field, unset: null prints nothing (obsolete, T-437)
		{p.Root, []string{"config", "get", ".mcp.nope"}, 1, ""},
		{p.Root, []string{"config", "get", ".lanes"}, 0, "1\n"},
		{p.Root, []string{"config", "get"}, 2, ""},
		{p.Root, []string{"config", "bogus"}, 2, ""},
		{p.Root, []string{"config", "validate", "--roles"}, 2, ""},
		{p.Root, []string{"config", "get", ".prefix.x"}, 1, ""},
	} {
		if rc, stdout, stderr := run(t, c.dir, nil, c.args...); rc != c.rc || stdout != c.out {
			t.Errorf("obor %q: exit %d, %q (%s); want %d, %q", c.args, rc, stdout, stderr, c.rc, c.out)
		}
	}
	// A missing key: FAIL on stderr only, so x=$(obor config get …) never captures it.
	if rc, stdout, stderr := run(t, p.Root, nil, "config", "get", ".prefix.x"); rc != 1 || stdout != "" ||
		stderr != "FAIL no such key: .prefix.x\n" {
		t.Errorf("missing key: exit %d, stdout %q, stderr %q", rc, stdout, stderr)
	}
}

// bash config.test.sh case_validate_* through the binary: OK exit 0; WARN lines then OK; FAIL lines exit 1; a removed key
// still in its two versions loads and only warns (T-463: cache.warn_tokens, v0.12.0).
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
		{[]func(map[string]any){testkit.Set("cache.warn_tokens", 50000)}, 0,
			"WARN cache.warn_tokens: no longer used, remove it\nOK\n"},
		{[]func(map[string]any){testkit.Set("version", 2), testkit.Set("paths.research", "r")}, 1,
			"WARN paths.research: no longer used, remove it\nFAIL .version: must be 1, got 2\n"},
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

// T-789: a config of the shape before v0.23 (lane types, the agents key) is just invalid: `obor config validate` names
// the unknown key and the release whose `obor setup migrate` rewrites it (the one-time code lived one release).
func TestConfigValidateOldShape(t *testing.T) {
	t.Parallel()
	for _, config := range []string{
		`{"version":1,"name":"proj","prefix":"pj","lane_types":{"opus":{"kind":"claude"}}}`,
		`{"version":1,"name":"proj","prefix":"pj","lanes":2,"agents":{"triage":{"type":"opus"}}}`,
	} {
		p := testkit.New(t)
		p.Write(".obor/config.json", config+"\n")
		rc, _, stderr := run(t, p.Root, nil, "config", "validate")
		if rc != 1 ||
			!strings.Contains(stderr, "this config predates v0.23: migrate it with obor v0.24 (`obor setup migrate`), then upgrade") {
			t.Errorf("%s: exit %d, %q; want the old-shape refusal", config, rc, stderr)
		}
	}
}

// whoami is `obor whoami [--json]` in dir with env: exit code, text and the decoded JSON.
func whoamiIn(t *testing.T, dir string, env ...string) (string, map[string]any) {
	t.Helper()
	rc, text, stderr := run(t, dir, env, "whoami")
	if rc != 0 {
		t.Fatalf("whoami in %s: exit %d, %s", dir, rc, stderr)
	}
	rc, stdout, stderr := run(t, dir, env, "whoami", "--json")
	var j map[string]any
	if err := json.Unmarshal([]byte(stdout), &j); rc != 0 || err != nil {
		t.Fatalf("whoami --json in %s: exit %d, %v %s", dir, rc, err, stderr)
	}
	return text, j
}

func TestWhoami(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-02")
	p.Config()

	// bash config.test.sh case_whoami_unarmed
	text, j := whoamiIn(t, lane)
	if text != "role: worker\nlane: L-01\nsource: cwd\nlane row: none\n" {
		t.Errorf("unarmed lane: %q", text)
	}
	if want := map[string]any{"role": "worker", "lane": "L-01", "source": "cwd"}; !reflect.DeepEqual(j, want) {
		t.Errorf("unarmed --json: %v", j)
	}
	if text, _ := whoamiIn(t, p.Root, "OBOR_ROLE=lead"); !strings.Contains(text, "role: lead\n") || !strings.Contains(text, "source: env\n") {
		t.Errorf("OBOR_ROLE=lead: %q", text)
	}
	if text, _ := whoamiIn(t, p.Root); !strings.Contains(text, "role: none\n") {
		t.Errorf("main checkout: %q", text)
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
	if want := "role: worker\nlane: L-01\nsource: env+cwd\ntask: T-02\nbranch: task/T-02\nbase: main\nspec: rev 1\n" +
		"protected file unlocked: docs/design.md\n"; text != want {
		t.Errorf("armed: %q, want %q", text, want)
	}
	_, j = whoamiIn(t, lane)
	if want := map[string]any{"role": "worker", "lane": "L-01", "source": "cwd", "task": "T-02",
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

// bash config.test.sh case_whoami_deep_dive, and a lead has no lead line any more (T-467).
func TestWhoamiDeepDiveAndLead(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-02")
	p.Config(testkit.Set("roles.deep_dive", "opus"))
	text, j := whoamiIn(t, p.Root, "OBOR_ROLE=lead", "OBOR_AGENT=lead-mp")
	if !strings.Contains(text, "role: lead\n") || !strings.Contains(text, "source: env\nlane row: none\n") {
		t.Errorf("lead: %q", text)
	}
	if strings.Contains(text, "\nlane: ") { // T-554: no empty `lane:` line for a role without a lane
		t.Errorf("a lead has no lane line: %q", text)
	}
	if want := map[string]any{"role": "lead", "lane": "", "source": "env"}; !reflect.DeepEqual(j, want) {
		t.Errorf("lead --json: %v", j)
	}
	if text, j := whoamiIn(t, lane); strings.Contains(text, "lead:") || j["lead"] != nil {
		t.Errorf("a worker has no lead line: %q", text)
	} else if !strings.Contains(text, "\nlane: L-01\n") {
		t.Errorf("a worker's lane line is missing: %q", text)
	}
	dd := p.Lane("deep-dive", "")
	// T-777: no role worktree is reserved under lanes_dir any more; the deep-dive worktree is a lane and its cwd wins
	// over OBOR_ROLE.
	if text, _ := whoamiIn(t, dd, "OBOR_ROLE=deep_dive", "OBOR_AGENT=pj-deep-dive"); !strings.Contains(text, "role: worker\n") ||
		!strings.Contains(text, "lane: deep-dive\n") || !strings.Contains(text, "source: env+cwd\n") {
		t.Errorf("deep-dive worktree with OBOR_ROLE: %q", text)
	}
	if text, _ := whoamiIn(t, dd); !strings.Contains(text, "role: worker\n") || !strings.Contains(text, "lane: deep-dive\n") {
		t.Errorf("deep-dive worktree is a lane: %q", text)
	}
}

// T-793: `whoami --can-push` (hidden; the pre-push hook's question) exits 0 for a non-lane-bound role in
// guards.allow_push and 1 otherwise, printing nothing.
func TestWhoamiCanPush(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	lane := p.Lane("L-01", "task/T-02")
	p.Config(func(c map[string]any) { c["guards"] = map[string]any{"allow_push": []string{"lead", "worker"}} })
	for _, c := range []struct {
		name string
		dir  string
		env  []string
		rc   int
	}{
		{"lead listed", p.Root, []string{"OBOR_ROLE=lead"}, 0},
		{"reviewer not listed", p.Root, []string{"OBOR_ROLE=reviewer"}, 1},
		{"no role", p.Root, nil, 1},
		{"worker in its lane, listed", lane, []string{"OBOR_ROLE=worker", "OBOR_LANE=L-01"}, 1},
	} {
		rc, stdout, stderr := run(t, c.dir, c.env, "whoami", "--can-push")
		if rc != c.rc || stdout != "" || stderr != "" {
			t.Errorf("%s: exit %d (want %d), stdout %q stderr %q", c.name, rc, c.rc, stdout, stderr)
		}
	}
}
