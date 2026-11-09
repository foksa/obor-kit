package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fixture is a file of testdata/migrate: obor-kit's and axis-alies' configs and obor-kit's local.json, each before
// (shape 1) and want (shape 2, docs/research/config-shape.md §2 and §3 with today's values).
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "migrate", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameJSON: a and b are the same JSON value (key order aside).
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("%v: %s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return reflect.DeepEqual(x, y)
}

// topKeys are doc's top-level keys in order.
func topKeys(doc []byte) []string {
	ms, _ := object(doc)
	var out []string
	for _, m := range ms {
		out = append(out, m.Key)
	}
	return out
}

// T-1124 (config-shape.md §4.4 P2): Reshape on the four fixtures equals §2's files, writes the groups in §2's order,
// and a second run changes nothing.
func TestReshapeFixtures(t *testing.T) {
	for _, name := range []string{"obor-kit.config", "obor-kit.local", "axis-alies.config"} {
		t.Run(name, func(t *testing.T) {
			before, want := fixture(t, name+".json"), fixture(t, strings.Replace(name, ".", ".want.", 1)+".json")
			if ShapeOf(before) != 1 || strings.HasSuffix(name, ".config") && ShapeOf(want) != 2 {
				t.Fatalf("precondition: before is shape 1 (%d), want shape 2 (%d)", ShapeOf(before), ShapeOf(want))
			}
			got, err := Reshape(before)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, got, want) {
				t.Errorf("Reshape =\n%s\nwant\n%s", Pretty(json.RawMessage(got)), want)
			}
			keys := topKeys(got)
			if i := slices.IndexFunc(keys, func(k string) bool { return !slices.Contains(shapeGroups, k) }); i >= 0 {
				t.Errorf("top-level key %q is no group of shape 2", keys[i])
			}
			if !slices.IsSortedFunc(keys, func(a, b string) int {
				return slices.Index(shapeGroups, a) - slices.Index(shapeGroups, b)
			}) {
				t.Errorf("groups %q, want them in §2's order %q", keys, shapeGroups)
			}
			again, err := Reshape(got)
			if err != nil || !bytes.Equal(again, got) {
				t.Errorf("a second run changed it (%v):\n%s\nwas\n%s", err, again, got)
			}
			if same, err := Reshape(want); err != nil || !sameJSON(t, same, want) {
				t.Errorf("Reshape of the shape-2 file changed it (%v): %s", err, same)
			}
		})
	}
}

// T-1124: Reshape works on a partial file (local.json: no version added), keeps a key it does not know where it is,
// drops the keys with no home, and splits and joins what the table splits and joins; on a mixed file (the test kit's)
// a value already at its shape-2 home wins.
func TestReshapePartsAndMixes(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"a partial file", `{"lanes":2,"slots":{"dir":"/s"}}`, `{"lanes":{"count":2},"checks":{"slot_dir":"/s"}}`},
		{"a lanes list", `{"lanes":[{"name":"L-01","keep_agent":true}]}`,
			`{"lanes":{"list":[{"name":"L-01","keep_agent":true}]}}`},
		{"a null lanes", `{"lanes":null,"lanes_dir":"../wt"}`, `{"lanes":{"dir":"../wt"}}`},
		{"unknown keys stay", `{"merge":{"guard":"g","future":1},"keep_warm":1}`,
			`{"merge":{"future":1,"by_path":{"guard":"g"}},"keep_warm":1}`},
		{"keys with no home go", `{"done_file_sections":["S"],"limits":{"max_panes_per_tab":3},"paths":{"research":"r",` +
			`"tasks":"t","agents":"a"},"merge":{"trailer":"x","affected_full":["y"],"scratch_dir":"z"}}`,
			`{"agents":{"docs_dir":"a"}}`},
		{"the flake pair splits", `{"merge":{"flake_regex":["T","P"]}}`,
			`{"checks":{"output":{"failed_test":"T","failed_package":"P"}}}`},
		{"a retest string takes its join", `{"commands":{"retest":"make retest"},"merge":{"retest_join":","}}`,
			`{"merge":{"retest":{"cmd":"make retest","join":","}}}`},
		{"the seven commands go home", `{"commands":{"lane_setup":"a","lane_ready":"b","compile":"c","check":"d",` +
			`"retest":"e","after_merge":{"cmd":"f","when":["x"]},"check_milestone":"g","own":"h"}}`,
			`{"lanes":{"setup":"a","ready":"b"},"merge":{"compile":"c","check":"d","retest":"e",` +
				`"after":{"cmd":"f","when_changed":["x"]}},"nightly":{"slow_check":"g"},"commands":{"own":"h"}}`},
		{"version 1 becomes 2", `{"version":1}`, `{"version":2}`},
		{"a newer version stays", `{"version":3,"name":"n"}`, `{"version":3,"project":{"name":"n"}}`},
		{"mixed: the shape-2 value wins", `{"version":2,"agents":{"roles":{"lead":"opus"}},` +
			`"roles":{"lead":"sonnet","integrator":"opus"},"lanes":{"count":3},"lane_links":["x"]}`,
			`{"version":2,"lanes":{"count":3,"links":["x"]},"agents":{"roles":{"lead":"opus","integrator":"opus"}}}`},
	} {
		got, err := Reshape([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !sameJSON(t, got, []byte(c.want)) {
			t.Errorf("%s: Reshape(%s) = %s, want %s", c.name, c.in, got, c.want)
		}
		if again, _ := Reshape(got); !bytes.Equal(again, got) {
			t.Errorf("%s: a second run changed %s to %s", c.name, got, again)
		}
	}
	if _, err := Reshape([]byte(`[]`)); err == nil {
		t.Error("Reshape of a list: no error")
	}
}

// T-1124 (§4.2): a shape-1 config.json (and the local.json beside it) loads reshaped in memory to the same effective
// config as its shape-2 rewrite; Load warns once per process, LoadReader names it, LoadHook says nothing, and obor
// config validate prints one WARN for it, no FAIL. The shape-2 files load without a word.
func TestShape1LoadWarnsOnceAndEqualsShape2(t *testing.T) {
	for _, c := range []struct{ name, local, wantLocal string }{
		{"obor-kit", "obor-kit.local.json", "obor-kit.want.local.json"},
		{"axis-alies", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var local, wantLocal string
			if c.local != "" {
				local, wantLocal = string(fixture(t, c.local)), string(fixture(t, c.wantLocal))
			}
			old := writeProject(t, string(fixture(t, c.name+".config.json")), local)
			cur := writeProject(t, string(fixture(t, c.name+".want.config.json")), wantLocal)
			var out bytes.Buffer
			defer func(w interface{ Write([]byte) (int, error) }) { Warn = w }(Warn)
			Warn = &out
			c1, err := Load(old)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Load(old); err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(out.String(), "config shape 1"); n != 1 || !c1.Shape1() {
				t.Errorf("two loads of shape 1 warned %d times (Shape1 %v), want once:\n%s", n, c1.Shape1(), out.String())
			}
			out.Reset()
			c2, err := Load(cur)
			if err != nil {
				t.Fatal(err)
			}
			if out.Len() != 0 || c2.Shape1() {
				t.Errorf("shape 2 warned %q (Shape1 %v), want nothing", out.String(), c2.Shape1())
			}
			if !bytes.Equal(c1.json, c2.json) {
				t.Errorf("shape 1 loads as\n%s\nshape 2 as\n%s", c1.json, c2.json)
			}
			if _, warn, err := LoadReader(old); err != nil || !strings.Contains(warn, "config shape 1") {
				t.Errorf("LoadReader: %q, %v; want the shape named", warn, err)
			}
			if _, _, err := LoadHook(old); err != nil || out.Len() != 0 {
				t.Errorf("LoadHook: %v, Warn %q; want nothing said", err, out.String())
			}
			v, _, err := LoadForValidate(old)
			if err != nil {
				t.Fatal(err)
			}
			warns, fails := v.Validate(ValidateOptions{})
			var version []string
			for _, l := range append(warns, fails...) {
				if strings.Contains(l, " .version") {
					version = append(version, l)
				}
			}
			if len(version) != 1 || !strings.HasPrefix(version[0], "WARN .version: config shape 1") {
				t.Errorf("validate on shape 1: %q, want one WARN", version)
			}
			if warns, fails := c2.Validate(ValidateOptions{}); slices.ContainsFunc(append(warns, fails...),
				func(l string) bool { return strings.Contains(l, " .version") }) {
				t.Errorf("validate on shape 2 names the version: %q %q", warns, fails)
			}
		})
	}
}

// T-1124: Config.Command finds the seven commands obor runs by a fixed name in their groups, and the project's own in
// commands; validate fails a fixed name under commands (it would never run) and a count beside a lanes list.
func TestFixedCommands(t *testing.T) {
	c := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"setup":"a","ready":"b"},`+
		`"merge":{"compile":"c","check":{"cmd":"d","heavy":true},"retest":"e","after":{"cmd":"f","when_changed":["x"]}},`+
		`"nightly":{"slow_check":"g"},"commands":{"own":"h"}}`, "")
	for name, want := range map[string]string{"lane_setup": "a", "lane_ready": "b", "compile": "c", "check": "d",
		"retest": "e", "after_merge": "f", "check_milestone": "g", "own": "h"} {
		if cmd, ok := c.Command(name); !ok || cmd.Cmd != want || cmd.Name != name {
			t.Errorf("Command(%s) = %+v, %v; want cmd %q named %s", name, cmd, ok, want, name)
		}
	}
	if cmd, _ := c.Command("check"); !cmd.Heavy {
		t.Errorf("merge.check's heavy lost: %+v", cmd)
	}
	if cmd, _ := c.Command("after_merge"); !slices.Equal(cmd.When, []string{"x"}) {
		t.Errorf("merge.after's when_changed lost: %+v", cmd)
	}
	if len(FixedCommands) != 7 {
		t.Errorf("FixedCommands has %d names, want the seven", len(FixedCommands))
	}
	bad := load(t, `{"version":2,"project":{"agent_prefix":"pj"},"agents":{"roles":{"integrator":"opus"}},`+
		`"commands":{"check":"make check"},"lanes":{"count":2,"list":[{"name":"L-01"}]}}`, "")
	_, fails := bad.Validate(ValidateOptions{})
	for _, want := range []string{"FAIL .commands.check: obor runs check from merge.check: move it there",
		"FAIL .lanes.count: set beside lanes.list: name the lanes one way (the list wins)"} {
		if !slices.Contains(fails, want) {
			t.Errorf("validate %q, want %q", fails, want)
		}
	}
	if bad.LaneCount() != 1 {
		t.Errorf("lanes %d beside a list of one, want the list's", bad.LaneCount())
	}
}

// T-1124: the setup writes write shape 2 (a shape-1 file is refused, never reshaped alone: RV-167 #1, T-1125); rename
// and the push list keep a file's shape, so a shape-1 local.json is edited where it has the key.
func TestSetupWritesShape2(t *testing.T) {
	w, err := SetCommand([]byte(`{"version":2,"lanes":{"count":1}}`), "lane_setup", "make deps")
	if err != nil || w.Key != "lanes.setup" || !sameJSON(t, w.Doc, []byte(`{"version":2,"lanes":{"count":1,"setup":"make deps"}}`)) {
		t.Errorf("SetCommand lane_setup: %s %q %v", w.Doc, w.Key, err)
	}
	w, err = SetCommand([]byte(`{"version":2}`), "lint", "make lint")
	if err != nil || w.Key != "commands.lint" || !sameJSON(t, w.Doc, []byte(`{"version":2,"commands":{"lint":"make lint"}}`)) {
		t.Errorf("SetCommand lint: %s %q %v", w.Doc, w.Key, err)
	}
	w, err = SetLanes([]byte(`{"version":2,"lanes":{"list":[{"name":"L-01"}]},"project":{"agent_prefix":"pj"}}`), 2)
	if err != nil || w.Key != "lanes.count" ||
		!sameJSON(t, w.Doc, []byte(`{"version":2,"project":{"agent_prefix":"pj"},"lanes":{"count":2}}`)) {
		t.Errorf("SetLanes: %s %q %v; want the count replacing the list", w.Doc, w.Key, err)
	}
	for _, c := range []struct{ name, doc, want string }{
		{"shape 2", `{"version":2,"project":{"name":"a","agent_prefix":"a"}}`,
			`{"version":2,"project":{"name":"b","agent_prefix":"bb"}}`},
		{"shape 1", `{"version":1,"name":"a","prefix":"a"}`, `{"version":1,"name":"b","prefix":"bb"}`},
	} {
		out, err := RenameProject([]byte(c.doc), "b", "bb", true)
		if err != nil || !sameJSON(t, out, []byte(c.want)) {
			t.Errorf("RenameProject %s: %s, %v; want %s", c.name, out, err, c.want)
		}
	}
	if out, _ := RenameProject([]byte(`{"roles":{"advisor":"fable"}}`), "b", "bb", false); string(out) != `{"roles":{"advisor":"fable"}}` {
		t.Errorf("RenameProject on a local.json without the keys: %s, want it unchanged", out)
	}
	if got := PushEntries([]byte(`{"guards":{"allow_push":["lead"]}}`)); !slices.Equal(got, []string{"lead"}) {
		t.Errorf("PushEntries shape 1 = %q", got)
	}
	if got := PushEntries([]byte(`{"version":2,"rules":{"may_push":["lead"]}}`)); !slices.Equal(got, []string{"lead"}) {
		t.Errorf("PushEntries shape 2 = %q", got)
	}
	if out, _ := SetPushEntries([]byte(`{"version":2,"rules":{"may_push":["x"]}}`), []string{"lead"}); !sameJSON(t, out,
		[]byte(`{"version":2,"rules":{"may_push":["lead"]}}`)) {
		t.Errorf("SetPushEntries shape 2 = %s", out)
	}
}
