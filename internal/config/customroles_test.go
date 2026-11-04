package config_test

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"obor/internal/config"
)

// T-801: the kit's default rows (advisor, researcher, triage) keep working from a config that names them as strings:
// same agent names, their writes from the default row; an object replaces the write list, a string sets the model.
func TestFoldedRolesFromStrings(t *testing.T) {
	p := newProject(t)
	edits := []edit{set("roles.advisor", "opus:high"), set("roles.researcher", "sonnet:medium"), set("roles.triage", "sonnet")}
	if got := validate(t, p, edits...); len(got) != 0 {
		t.Fatalf("validate = %q, want OK", got)
	}
	c, err := config.Load(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		role, agent, model, effort string
		write                      []string
	}{
		{"advisor", "pj-advisor", "opus", "high", []string{"docs/research/", "docs/plan/"}},
		{"researcher", "pj-researcher", "sonnet", "medium", []string{"docs/research/"}},
		{"triage", "pj-triage", "sonnet", "", []string{"docs/BUGS.md"}},
	} {
		rc, ok := c.Role(w.role)
		if !ok || !rc.Configured || rc.Agent != w.agent || rc.Type != w.model || rc.Effort != w.effort ||
			!rc.Role.Custom || !rc.Role.GitReadOnly || !reflect.DeepEqual(rc.Role.WritePaths, w.write) {
			t.Errorf("Role(%s) = %+v, %v; want agent %s, model %s:%s, writes %q", w.role, rc, ok, w.agent, w.model,
				w.effort, w.write)
		}
		if info, ok := c.AgentLookup(w.agent, "", nil); !ok || info.Role != w.role || info.Cwd != p.Root {
			t.Errorf("AgentLookup(%s) = %+v, %v; want role %s in the main checkout", w.agent, info, ok, w.role)
		}
		if c.Self(w.role, "") != w.agent {
			t.Errorf("Self(%s) = %q, want %s", w.role, c.Self(w.role, ""), w.agent)
		}
	}
	if rc, ok := c.Role("advisor"); !ok || rc.Role.Seeds != nil {
		t.Errorf("advisor seeds = %v, want none", rc.Role.Seeds)
	}

	// Not named: a default row is known (the guard holds a session to it) but no role agent.
	p2 := newProject(t)
	validate(t, p2)
	c2, _ := config.Load(p2.Root)
	if rc, ok := c2.Role("researcher"); !ok || rc.Configured {
		t.Errorf("unnamed researcher = %+v, %v; want known, not configured", rc, ok)
	}
	for _, rc := range c2.RoleAgents() {
		if rc.Role.Custom {
			t.Errorf("unnamed default row %s counts as a role agent", rc.Role.Name)
		}
	}

	// An object replaces the write list and keeps the model; an object without write keeps the default row's.
	validate(t, p, set("roles.researcher", `{"model": "sonnet", "write": ["notes/"]}`),
		set("roles.advisor", `{"model": "opus"}`))
	c, _ = config.Load(p.Root)
	if rc, _ := c.Role("researcher"); rc.Type != "sonnet" || !reflect.DeepEqual(rc.Role.WritePaths, []string{"notes/"}) {
		t.Errorf("researcher object = %+v, want sonnet writing notes/", rc)
	}
	if rc, _ := c.Role("advisor"); !reflect.DeepEqual(rc.Role.WritePaths, []string{"docs/research/", "docs/plan/"}) {
		t.Errorf("advisor object without write = %+v, want the default row's writes", rc.Role.WritePaths)
	}
}

// T-801: a custom role is a roles.<name> row (a model ref string, or an object with a write list) and its doc; it
// validates, is a role agent (in config order after the table and default rows) named <prefix>-<name, _ → ->.
func TestValidCustomRoles(t *testing.T) {
	p := newProject(t)
	p.Write("docs/agents/buddy.md", "# Role: buddy\n")
	p.Write("docs/agents/ux_designer.md", "# Role: ux_designer\n")
	got := validate(t, p, set("roles.buddy", "sonnet"),
		set("roles.ux_designer", `{"model": "opus:high", "write": ["docs/design/", "STYLE.md", "./docs/ux/"]}`))
	if len(got) != 0 {
		t.Fatalf("validate = %q, want OK", got)
	}
	c, _ := config.Load(p.Root)
	buddy, ok := c.Role("buddy")
	if !ok || !buddy.Configured || buddy.Agent != "pj-buddy" || buddy.Type != "sonnet" || !buddy.Role.Custom ||
		buddy.Role.WritePaths != nil {
		t.Errorf("buddy = %+v, %v; want pj-buddy, sonnet, writing nothing", buddy, ok)
	}
	ux, _ := c.Role("ux_designer")
	if ux.Agent != "pj-ux-designer" || ux.Type != "opus" || ux.Effort != "high" ||
		!reflect.DeepEqual(ux.Role.WritePaths, []string{"docs/design/", "STYLE.md", "./docs/ux/"}) {
		t.Errorf("ux_designer = %+v", ux)
	}
	var names []string
	for _, rc := range c.RoleAgents() {
		names = append(names, rc.Role.Name)
	}
	if want := []string{"lead", "integrator", "reviewer", "buddy", "ux_designer"}; !reflect.DeepEqual(names, want) {
		t.Errorf("RoleAgents = %q, want %q", names, want)
	}
	if info, ok := c.AgentLookup("pj-ux-designer", "", nil); !ok || info.Role != "ux_designer" || info.Type != "opus" {
		t.Errorf("AgentLookup(pj-ux-designer) = %+v, %v", info, ok)
	}
	if c.RoleDoc("ux_designer") != "docs/agents/ux_designer.md" {
		t.Errorf("RoleDoc = %q, want docs/agents/ux_designer.md", c.RoleDoc("ux_designer"))
	}
	if !slices.Contains(c.UsedModels(), "opus") {
		t.Errorf("UsedModels = %q, want the custom role's opus", c.UsedModels())
	}
	if c.MayPush("buddy") {
		t.Error("a custom role may push")
	}
	// The effective config keeps both shapes (obor config get).
	if v, ok := c.Raw("roles.buddy"); !ok || v != "sonnet" {
		t.Errorf("config get roles.buddy = %q, %v", v, ok)
	}
	if v, ok := c.Raw("roles.ux_designer.model"); !ok || v != "opus:high" {
		t.Errorf("config get roles.ux_designer.model = %q, %v", v, ok)
	}
}

// T-801: validate refuses a bad name, a collision with a table role (its name or agent suffix), a write path outside
// the project or into .obor/, a missing role doc (naming the file to write), an allow_push listing a custom role, an
// object for a table role and a roles.driver.
func TestCustomRoleRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		edits []edit
		want  string
	}{
		{"bad name", []edit{set("roles.Buddy", "sonnet")}, `FAIL .roles.Buddy: a custom role's name must match ^[a-z][a-z0-9_]{0,23}$, got "Buddy"`},
		{"bad name: dash", []edit{set("roles.ux-buddy", "sonnet")}, `FAIL .roles["ux-buddy"]: a custom role's name must match`},
		{"too long", []edit{set("roles.abcdefghijklmnopqrstuvwxy", "sonnet")}, "FAIL .roles.abcdefghijklmnopqrstuvwxy: a custom role's name must match"},
		{"table suffix", []edit{set("roles.rev", "sonnet")}, `FAIL .roles.rev: collides with the built-in role reviewer (its name or agent suffix), got "rev"`},
		{"table suffix: int", []edit{set("roles.int", "sonnet")}, "FAIL .roles.int: collides with the built-in role integrator"},
		{"table suffix: drive", []edit{set("roles.drive", "sonnet")}, "FAIL .roles.drive: collides with the built-in role driver"},
		{"reserved", []edit{set("roles.none", "sonnet")}, "FAIL .roles.none: none is reserved"},
		{"driver", []edit{set("roles.driver", "sonnet")}, "FAIL .roles.driver: the drive agent's role is not configured: obor drive starts it"},
		{"table object", []edit{set("roles.reviewer", `{"model": "opus", "write": ["x/"]}`)}, "FAIL .roles.reviewer: a built-in role takes a model ref string, not an object"},
		{"write in .obor", []edit{set("roles.buddy", `{"model": "sonnet", "write": [".obor/state/"]}`)}, `FAIL .roles.buddy.write[0]: in .obor/: obor's own state is no role's to write, got ".obor/state/"`},
		{"write .obor itself", []edit{set("roles.buddy", `{"model": "sonnet", "write": ["docs/", ".obor"]}`)}, "FAIL .roles.buddy.write[1]: in .obor/"},
		{"write the root", []edit{set("roles.buddy", `{"model": "sonnet", "write": ["./"]}`)}, "FAIL .roles.buddy.write[0]: in .obor/"},
		{"write outside", []edit{set("roles.buddy", `{"model": "sonnet", "write": ["../other/"]}`)}, `FAIL .roles.buddy.write[0]: outside the project: a write path is relative to its root, got "../other/"`},
		{"write outside via ..", []edit{set("roles.buddy", `{"model": "sonnet", "write": ["docs/../../x"]}`)}, "FAIL .roles.buddy.write[0]: outside the project"},
		{"write absolute", []edit{set("roles.buddy", `{"model": "sonnet", "write": ["/tmp/x/"]}`)}, "FAIL .roles.buddy.write[0]: outside the project"},
		{"write empty", []edit{set("roles.buddy", `{"model": "sonnet", "write": [""]}`)}, "FAIL .roles.buddy.write[0]: an empty path"},
		{"default row write outside", []edit{set("roles.researcher", `{"model": "sonnet", "write": ["../x/"]}`)}, "FAIL .roles.researcher.write[0]: outside the project"},
		{"allow_push lists it", []edit{set("roles.buddy", "sonnet"), set("guards.allow_push", `["lead", "buddy"]`)}, `FAIL .guards.allow_push: "buddy" is a custom role: it never pushes (remove it)`},
		{"allow_push lists its agent", []edit{set("roles.buddy", "sonnet"), set("guards.allow_push", `["pj-buddy"]`)}, `FAIL .guards.allow_push: "pj-buddy" is a custom role`},
		{"allow_push lists a default row", []edit{set("roles.advisor", "opus"), set("guards.allow_push", `["advisor"]`)}, `FAIL .guards.allow_push: "advisor" is a custom role`},
		{"bad model", []edit{set("roles.buddy", `{"model": "nosuch"}`)}, `FAIL .roles.buddy.model: "nosuch" is not a model`},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProject(t)
			p.Write("docs/agents/buddy.md", "# Role: buddy\n")
			got := validate(t, p, c.edits...)
			if !slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, c.want) }) {
				t.Errorf("validate = %q, want a line starting %q", got, c.want)
			}
		})
	}

	// The doc: a custom row without <paths.agents>/<name>.md fails naming the file; a default row needs none (the kit
	// ships its text).
	p := newProject(t)
	got := validate(t, p, set("roles.designer", "opus"), set("roles.advisor", "opus"))
	if want := []string{"FAIL .roles.designer: no role doc: write docs/agents/designer.md (what the role does, what it writes)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
	got = validate(t, p, set("roles.designer", "opus"), set("paths.agents", "doc/roles"))
	if want := "FAIL .roles.designer: no role doc: write doc/roles/designer.md"; len(got) != 1 || !strings.HasPrefix(got[0], want) {
		t.Errorf("validate = %q, want %q", got, want)
	}
	p.Write("doc/roles/designer.md", "# Role: designer\n")
	if got := validate(t, p, set("roles.designer", "opus"), set("paths.agents", "doc/roles")); len(got) != 0 {
		t.Errorf("with its doc: validate = %q, want OK", got)
	}
	// An invalid row is no role.
	validate(t, p, set("roles.rev", "opus"))
	c, _ := config.Load(p.Root)
	if rc, ok := c.Role("rev"); ok {
		t.Errorf("roles.rev = %+v, want no role", rc)
	}
	if info, ok := c.AgentLookup("pj-rev", "", nil); !ok || info.Role != "reviewer" {
		t.Errorf("pj-rev = %+v, %v; want the reviewer", info, ok)
	}
}

// T-801: removing a custom role's row leaves its doc behind: validate warns of it, the role is gone from every list
// (no derived name, no role agent), nothing else changes; a doc without a "# Role:" first line is no role doc.
func TestRemovedCustomRoleRow(t *testing.T) {
	p := newProject(t)
	p.Write("docs/agents/buddy.md", "# Role: buddy\n")
	p.Write("docs/agents/gotchas.md", "# Gotchas\n")
	if got := validate(t, p, set("roles.buddy", "sonnet")); len(got) != 0 {
		t.Fatalf("precondition: with its row validate = %q, want OK", got)
	}
	c, _ := config.Load(p.Root)
	if _, ok := c.AgentLookup("pj-buddy", "", nil); !ok {
		t.Fatal("precondition: pj-buddy is a role agent")
	}
	got := validate(t, p)
	want := []string{"WARN .roles.buddy: docs/agents/buddy.md is a role doc but roles.buddy is not set: no agent runs it (add the row, or remove the doc)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("row removed: validate = %q, want %q", got, want)
	}
	c, _ = config.Load(p.Root)
	if _, ok := c.Role("buddy"); ok {
		t.Error("a removed row is still a role")
	}
	if c.Self("buddy", "") != "" {
		t.Errorf("Self(buddy) = %q, want none", c.Self("buddy", ""))
	}
	if _, ok := c.AgentLookup("pj-buddy", "", nil); ok {
		t.Error("pj-buddy is still a role agent")
	}
	for _, rc := range c.RoleAgents() {
		if rc.Role.Name == "buddy" {
			t.Error("buddy still counts as a role agent")
		}
	}
	// A kit default row's doc with its row unset (obor init stubs one for each) and a table role's doc never warn.
	p.Write("docs/agents/advisor.md", "# Role: advisor — here\n")
	p.Write("docs/agents/reviewer.md", "# Role: reviewer — here\n")
	os.Remove(p.Path("docs/agents/buddy.md"))
	if got = validate(t, p); len(got) != 0 {
		t.Errorf("validate = %q, want OK", got)
	}
}

// T-801: the setup edit of a custom role's model: a string row is replaced, an object row keeps its write list (its
// model key is set); an unconfigured custom role is refused like any optional role.
func TestEditCustomRoleModel(t *testing.T) {
	p := newProject(t)
	p.Write("docs/agents/buddy.md", "# Role: buddy\n")
	p.Write("docs/agents/designer.md", "# Role: designer\n")
	validate(t, p, set("roles.buddy", "sonnet"), set("roles.designer", `{"model": "sonnet", "write": ["docs/design/"]}`))
	c, _ := config.Load(p.Root)
	doc, _ := os.ReadFile(p.Path(".obor/config.json"))
	ch, err := c.Edit(doc, "roles.designer", []byte(`"opus:high"`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := config.LoadWith(p.Root, ch.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if rc, _ := after.Role("designer"); rc.Type != "opus" || rc.Effort != "high" ||
		!reflect.DeepEqual(rc.Role.WritePaths, []string{"docs/design/"}) {
		t.Errorf("designer after the edit = %+v, want opus:high still writing docs/design/", rc)
	}
	ch, err = c.Edit(doc, "roles.buddy", []byte(`"haiku"`))
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := config.LoadWith(p.Root, ch.Doc); after == nil || func() string { rc, _ := after.Role("buddy"); return rc.Type }() != "haiku" {
		t.Errorf("buddy after the edit: %s", ch.Doc)
	}
	if _, err := c.Edit(doc, "roles.researcher", []byte(`"opus"`)); err == nil || !strings.Contains(err.Error(), "not a configured role") ||
		!strings.Contains(err.Error(), "buddy, designer") {
		t.Errorf("an unnamed default row: %v, want refused naming the configured roles", err)
	}
}
