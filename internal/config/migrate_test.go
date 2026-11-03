package config

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// T-765: obor-kit's and axis-alies's configs as they were (testdata/migrate/<name>.config.json, local.json) migrate to
// the expected new ones (<name>.want.*), which load and validate; the lanes, renamed agents and notes are the rules'.
func TestMigrateFixtures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lanes  []string
		agents []AgentRename
		notes  []string // each must be among the notes
	}{
		{"obor-kit", []string{"L-01", "L-02", "L-03", "L-04"},
			[]AgentRename{{"ob-terra", "ob-rev"}, {"ob-fable", "ob-advisor"}},
			[]string{"lane_types.opus.max 4: dropped (no caps)", "lane_types.luna: no level, role or agent names it: dropped"}},
		{"axis-alies", []string{"D-01", "D-02", "D-03", "D-04", "OH-01", "OH-02"},
			[]AgentRename{{"aa-o-02", "aa-int"}, {"lead-mp", "aa-lead"}, {"aa-d-01", "aa-l-01"}, {"aa-d-02", "aa-l-02"},
				{"aa-d-03", "aa-l-03"}, {"aa-d-04", "aa-l-04"}, {"aa-oh-01", "aa-l-05"}, {"aa-oh-02", "aa-l-06"}},
			[]string{"lane_types.muse: no level, role or agent names it: dropped",
				"lane_types.opus.args --strict-mcp-config: dropped (not every claude type has it)",
				`lane_types.fable.settings {"remoteControlAtStartup":false}: dropped`,
				"config.json roles.integrator.worktree O-02 dropped (roles run in the main checkout)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := "testdata/migrate/" + tc.name
			doc, err := os.ReadFile(dir + ".config.json")
			if err != nil {
				t.Fatal(err)
			}
			if OldShape(doc) == "" {
				t.Fatal("the fixture is not of the old shape")
			}
			local, err := os.ReadFile(dir + ".local.json")
			if errors.Is(err, os.ErrNotExist) {
				local = nil
			}
			m, err := Migrate(doc, local)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(dir + ".want.config.json")
			if string(m.Config) != string(want) {
				t.Errorf("config.json:\n%s\nwant\n%s", m.Config, want)
			}
			if local != nil {
				wantLocal, _ := os.ReadFile(dir + ".want.local.json")
				if string(m.Local) != string(wantLocal) {
					t.Errorf("local.json:\n%s\nwant\n%s", m.Local, wantLocal)
				}
			} else if m.Local != nil {
				t.Errorf("local.json written with none there: %s", m.Local)
			}
			if !slices.Equal(m.Lanes, tc.lanes) || !slices.Equal(m.Agents, tc.agents) {
				t.Errorf("lanes %q agents %v, want %q %v", m.Lanes, m.Agents, tc.lanes, tc.agents)
			}
			for _, n := range tc.notes {
				if !slices.Contains(m.Notes, n) {
					t.Errorf("notes lack %q:\n%s", n, strings.Join(m.Notes, "\n"))
				}
			}
			c, err := Load(writeProject(t, string(m.Config), string(m.Local)))
			if err != nil {
				t.Fatalf("the migrated config does not load: %v", err)
			}
			if _, fails := c.Validate(ValidateOptions{}); len(fails) > 0 {
				t.Errorf("the migrated config fails validate: %q", fails)
			}
		})
	}
}

// T-765: the rules one at a time: a type naming a built-in model (by kind and model id) becomes its ref with the
// args' effort, a ref's own effort wins, a type the table lacks goes to models, the handoff thresholds move.
func TestMigrateRules(t *testing.T) {
	m, err := Migrate([]byte(`{"prefix":"pj","lane_types":{
		"opus_high":{"kind":"claude","args":["--model","opus","--effort","high","--permission-mode","auto","--x"]},
		"big":{"kind":"claude","args":["--model","claude-opus-5-5","--permission-mode","auto","--x"],"prompt_extra":"A"},
		"cx":{"kind":"codex","args":["-m","gpt-6-luna","-c","model_reasoning_effort=low","-s","workspace-write","-a","never"]}},
		"lanes":{"L-01":"opus_high","L-02":""},
		"difficulty":{"hard":["opus_high","big:max"]},
		"roles":{"lead":{"type":"opus_high:xhigh","handoff_tokens":400000},"integrator":{"type":"cx","handoff_tokens":0},
			"triage":{}},
		"agents":{"adv":{"type":"big"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(writeProject(t, string(m.Config), ""))
	if err != nil {
		t.Fatalf("%v\n%s", err, m.Config)
	}
	if got := c.DifficultyList("hard"); !slices.Equal(got, []string{"opus:high", "big:max"}) {
		t.Errorf("hard = %q, want opus:high (a built-in by kind and id), big:max (a ref's effort kept)", got)
	}
	for role, want := range map[string]string{"lead": "opus:xhigh", "integrator": "luna:low", "triage": ""} {
		if rc, _ := c.Role(role); ModelRef(rc.Type, rc.Effort) != want || !rc.Configured {
			t.Errorf("roles.%s = %+v, want %q", role, rc, want)
		}
	}
	if big, ok := c.Model("big"); !ok || big.ID != "claude-opus-5-5" || big.Kind != "claude" {
		t.Errorf("models.big = %+v, %v", big, ok)
	}
	if k := c.Data.Kinds.M["claude"]; !slices.Equal(k.Args, []string{"--x"}) || k.PromptExtra != "A" {
		t.Errorf("kinds.claude = %+v, want the args both claude types share and the one prompt_extra", k)
	}
	if c.Data.HandoffTokens == nil || *c.Data.HandoffTokens != 400000 || c.IntegratorHandoffTokens() != 0 {
		t.Errorf("handoff thresholds not moved: %s", m.Config)
	}
	if c.Data.Lanes != 2 || !slices.Equal(m.Lanes, []string{"L-01", "L-02"}) {
		t.Errorf("lanes = %d, %q", c.Data.Lanes, m.Lanes)
	}
	if !slices.Contains(m.Notes, "config.json lanes.L-01: type opus_high dropped (every lane runs the model its dispatch picks)") {
		t.Errorf("notes = %q, want the typed lane's", m.Notes)
	}
	if _, err := Migrate([]byte(`{"prefix":"pj","lanes":2}`), nil); err == nil || !strings.Contains(err.Error(), "new shape already") {
		t.Errorf("a new-shape config: %v, want a refusal", err)
	}
}

// T-765: the loader refuses the old shape with the hint, whichever file has it; the new shape loads.
func TestOldShapeRefused(t *testing.T) {
	for _, tc := range []struct{ cfg, local, key, file string }{
		{`{"prefix":"pj","lane_types":{"opus":{"kind":"claude"}}}`, "", "lane_types", "config.json"},
		{`{"prefix":"pj","lanes":{"L-01":""}}`, "", "lanes", "config.json"},
		{`{"prefix":"pj","roles":{"lead":{"type":"opus"}}}`, "", "roles.lead", "config.json"},
		{`{"prefix":"pj","roles":{"lead":"opus"}}`, `{"roles":{"advisor":{"type":"fable","agent":"ob-fable"}}}`, "roles.advisor", "local.json"},
	} {
		root := writeProject(t, tc.cfg, tc.local)
		for _, load := range []func(string) (*Config, error){Load, LoadDaemon, func(r string) (*Config, error) {
			c, _, err := LoadHook(r)
			return c, err
		}} {
			_, err := load(root)
			if !errors.Is(err, ErrOldShape) || !strings.Contains(err.Error(), "run obor setup migrate") ||
				!strings.Contains(err.Error(), tc.key+" is the old config shape") || !strings.Contains(err.Error(), tc.file) {
				t.Errorf("%s / %s: %v, want the old-shape refusal naming %s in %s", tc.cfg, tc.local, err, tc.key, tc.file)
			}
		}
	}
	if _, err := Load(writeProject(t, `{"prefix":"pj","lanes":2,"roles":{"lead":"opus"}}`, "")); err != nil {
		t.Errorf("the new shape: %v", err)
	}
}
