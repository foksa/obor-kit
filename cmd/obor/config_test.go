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
	p := testkit.NewPlain(t)
	if rc, _, stderr := run(t, p.Root, nil, "config"); rc != 3 || !strings.Contains(stderr, "no "+p.Root+"/.obor/config.json") {
		t.Errorf("config without .obor/config.json: exit %d, %q", rc, stderr)
	}
	if rc, _, _ := run(t, p.Root, nil, "whoami"); rc != 3 {
		t.Errorf("whoami without .obor/config.json: exit %d", rc)
	}
	lane := p.PlainLane("L-01", "task/T-01")
	p.Config()
	p.Write(".obor/local.json", `{"merge": {"nightly_at": "02:30"}}`)

	rc, stdout, _ := run(t, p.Root, nil, "config")
	var eff struct {
		Prefix string `json:"prefix"`
		Merge  struct {
			NightlyAt string `json:"nightly_at"`
		} `json:"merge"`
	}
	if err := json.Unmarshal([]byte(stdout), &eff); rc != 0 || err != nil || eff.Prefix != "pj" || eff.Merge.NightlyAt != "02:30" {
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
		{lane, []string{"config", "get", ".merge.nightly_at"}, 0, "02:30\n"},
		{p.Root, []string{"config", "get", ".notify"}, 1, ""}, // removed (T-830): an unknown key now
		{p.Root, []string{"config", "get", ".nosuch"}, 1, ""},
		{p.Root, []string{"config", "get", ".mcp.jobs"}, 1, ""}, // removed (T-830): an unknown key now
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

// T-846: a config of the shape before v0.23 is an ordinary unknown-key failure now (its message named the migration
// release for its two-minor window): `obor config validate` names the key and says nothing of a migration.
func TestConfigValidateOldShapeIsUnknownKey(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ config, key string }{
		{`{"version":1,"name":"proj","prefix":"pj","lane_types":{"opus":{"kind":"claude"}}}`, "lane_types"},
		{`{"version":1,"name":"proj","prefix":"pj","lanes":2,"agents":{"triage":{"type":"opus"}}}`, "agents"},
	} {
		p := testkit.NewPlain(t)
		p.Write(".obor/config.json", c.config+"\n")
		rc, _, stderr := run(t, p.Root, nil, "config", "validate")
		if rc != 1 || !strings.Contains(stderr, `unknown field "`+c.key+`"`) || strings.Contains(stderr, "predates v0.23") {
			t.Errorf("%s: exit %d, %q; want exit 1 and the unknown field %q", c.config, rc, stderr, c.key)
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

// bash config.test.sh case_whoami_deep_dive, and a lead has no lead line any more (T-467).
func TestWhoamiDeepDiveAndLead(t *testing.T) {
	t.Parallel()
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "task/T-02")
	p.Config(testkit.Set("roles.deep_dive", "opus"))
	text, j := whoamiIn(t, p.Root, "OBOR_ROLE=lead", "OBOR_AGENT=lead-mp")
	if !strings.Contains(text, "role: lead\n") || !strings.Contains(text, "source: env\nlane row: none\n") {
		t.Errorf("lead: %q", text)
	}
	if strings.Contains(text, "\nlane: ") { // T-554: no empty `lane:` line for a role without a lane
		t.Errorf("a lead has no lane line: %q", text)
	}
	if want := map[string]any{"role": "lead", "agent": "pj-lead", "lane": "", "source": "env"}; !reflect.DeepEqual(j, want) {
		t.Errorf("lead --json: %v", j)
	}
	if text, j := whoamiIn(t, lane); strings.Contains(text, "lead:") || j["lead"] != nil {
		t.Errorf("a worker has no lead line: %q", text)
	} else if !strings.Contains(text, "\nlane: L-01\n") {
		t.Errorf("a worker's lane line is missing: %q", text)
	}
	dd := p.PlainLane("deep-dive", "")
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
	p := testkit.NewPlain(t)
	lane := p.PlainLane("L-01", "task/T-02")
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
