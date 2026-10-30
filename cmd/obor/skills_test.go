package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const pluginRoot = "../.."

var (
	// Skills of the kit (T-204): only lead is model-discoverable; setup and role are user-invoked only.
	selfChosenSkills  = []string{"lead"}
	userInvokedSkills = []string{"setup", "role"}
	removedSkills     = []string{"worker", "integrator", "review-brief", "night-shift"}

	herdMentionRe = regexp.MustCompile(`(^|[^A-Za-z0-9_/:.-])obor ([a-z][a-z0-9-]*)`)
	skillFileRe   = regexp.MustCompile(`^[a-z][a-z0-9-]*\.md$`)
)

// frontmatterField is the field's value from a leading "---" … "---" block ("" if no block or field).
func frontmatterField(text, field string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return ""
	}
	for _, l := range lines[1:] {
		if l == "---" {
			return ""
		}
		if v, ok := strings.CutPrefix(l, field+":"); ok {
			return strings.TrimLeft(v, " \t")
		}
	}
	return ""
}

// codeText is the text inside backtick spans and fenced code blocks, one chunk per element.
func codeText(text string) []string {
	var out []string
	fence := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " \t"), "```") {
			fence = !fence
			continue
		}
		if fence {
			out = append(out, l)
			continue
		}
		parts := strings.Split(l, "`")
		for i := 1; i < len(parts); i += 2 {
			out = append(out, parts[i])
		}
	}
	return out
}

// walkFiles is every regular file under the dirs, sorted.
func walkFiles(t *testing.T, dirs ...string) []string {
	t.Helper()
	var files []string
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	return files
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// herdMentions is the sorted unique subcommand names mentioned as `obor <sub>` in the dirs' files.
func herdMentions(t *testing.T, dirs ...string) []string {
	t.Helper()
	set := map[string]bool{}
	for _, f := range walkFiles(t, dirs...) {
		for _, chunk := range codeText(readFile(t, f)) {
			for _, m := range herdMentionRe.FindAllStringSubmatch(chunk, -1) {
				set[m[2]] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// missingSubs is the mentioned subcommands not listed in root/bin/go-commands (skills, templates and the project's
// role docs in docs/agents).
func missingSubs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	for _, d := range []string{"skills", "templates", "docs/agents"} {
		if fi, err := os.Stat(filepath.Join(root, d)); err == nil && fi.IsDir() {
			dirs = append(dirs, filepath.Join(root, d))
		}
	}
	known := map[string]bool{}
	if b, err := os.ReadFile(filepath.Join(root, "bin", "go-commands")); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			known[l] = true
		}
	}
	var out []string
	for _, s := range herdMentions(t, dirs...) {
		if !known[s] {
			out = append(out, s)
		}
	}
	return out
}

// skillFileRefs is the bare `<name>.md` files a skill names in backticks (files beside its SKILL.md).
func skillFileRefs(skillMD string) []string {
	set := map[string]bool{}
	for _, c := range codeText(skillMD) {
		if skillFileRe.MatchString(c) {
			set[c] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func skillDirs(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(pluginRoot, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func skillMD(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, filepath.Join(pluginRoot, "skills", name, "SKILL.md"))
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func eqStrings(t *testing.T, want, got []string, what string) {
	t.Helper()
	if strings.Join(want, "\n") != strings.Join(got, "\n") {
		t.Errorf("%s: want %q, got %q", what, want, got)
	}
}

// bash tests/skills.test.sh lead skill has frontmatter name and description
// bash tests/skills.test.sh every skill file has frontmatter name and description
func TestSkillFrontmatter(t *testing.T) {
	names := skillDirs(t)
	if len(names) == 0 {
		t.Fatal("precondition: no skills")
	}
	for _, n := range names {
		md := skillMD(t, n)
		if got := frontmatterField(md, "name"); got != n {
			t.Errorf("%s: name = %q, want %q", n, got, n)
		}
		if frontmatterField(md, "description") == "" {
			t.Errorf("%s: empty description", n)
		}
	}
	if d := frontmatterField(skillMD(t, "lead"), "description"); len(d) < 40 {
		t.Errorf("lead description too short: %q", d)
	}
}

// bash tests/skills.test.sh frontmatter check rejects a missing block and fields after it
func TestFrontmatterFieldRejectsMissingBlockAndLateFields(t *testing.T) {
	tests := []struct{ name, text, field, want string }{
		{"no block", "# no frontmatter\nname: x\n", "name", ""},
		{"field after the block", "---\nname: x\n---\ndescription: late\n", "description", ""},
		{"name", "---\nname: x\ndescription: when to use x\n---\n", "name", "x"},
		{"description", "---\nname: x\ndescription: when to use x\n---\n", "description", "when to use x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := frontmatterField(tc.text, tc.field); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// bash tests/skills.test.sh mentions count backticks and fences, not prose
func TestMentionsCountBackticksAndFencesNotProse(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"r/skills/x/SKILL.md": "Run `obor dispatch O-01 T-1` and `obor ghost`; the obor hooks deny; `/obor:lead`; `herdr agent list`.\n",
		"r/templates/a.md":    "text\n```\nobor fenced --flag\n```\nobor prose here\n",
		"r/bin/go-commands":   "version\ndispatch\n",
	})
	r := filepath.Join(root, "r")
	eqStrings(t, []string{"dispatch", "fenced", "ghost"},
		herdMentions(t, filepath.Join(r, "skills"), filepath.Join(r, "templates")), "mentions")
	eqStrings(t, []string{"fenced", "ghost"}, missingSubs(t, r), "missing: dispatch is a Go command (bin/go-commands)")
}

// bash tests/skills.test.sh every obor subcommand in skills and templates exists
func TestEveryHerdSubcommandInSkillsAndTemplatesExists(t *testing.T) {
	found := herdMentions(t, filepath.Join(pluginRoot, "skills"), filepath.Join(pluginRoot, "templates"),
		filepath.Join(pluginRoot, "docs", "agents"))
	for _, want := range []string{"dispatch", "sendback"} {
		if !contains(found, want) {
			t.Fatalf("precondition: mentions %v lack %q", found, want)
		}
	}
	if missing := missingSubs(t, pluginRoot); len(missing) > 0 {
		t.Errorf("obor subcommands not in bin/go-commands: %v", missing)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// bash tests/skills.test.sh skills are exactly lead, setup and role; the removed ones are gone
func TestSkillsAreExactlyLeadSetupAndRole(t *testing.T) {
	eqStrings(t, []string{"lead", "role", "setup"}, skillDirs(t), "skill dirs")
	for _, s := range removedSkills {
		if _, err := os.Stat(filepath.Join(pluginRoot, "skills", s)); err == nil {
			t.Errorf("removed skill %s still exists", s)
		}
	}
}

// bash tests/skills.test.sh every skill is declared self-chosen or user-invoked
func TestEverySkillIsDeclaredSelfChosenOrUserInvoked(t *testing.T) {
	names := skillDirs(t)
	if len(names) == 0 {
		t.Fatal("precondition: no skills")
	}
	for _, n := range names {
		if !contains(selfChosenSkills, n) && !contains(userInvokedSkills, n) {
			t.Errorf("%s is not classified", n)
		}
	}
}

// bash tests/skills.test.sh self-chosen skills say when to use them and stay model-invocable
func TestSelfChosenSkillsSayWhenToUseThem(t *testing.T) {
	for _, s := range selfChosenSkills {
		md := skillMD(t, s)
		if d := frontmatterField(md, "description"); !strings.Contains(d, "Use when") {
			t.Errorf("%s: description lacks %q: %q", s, "Use when", d)
		}
		if v := frontmatterField(md, "disable-model-invocation"); v != "" {
			t.Errorf("%s: disable-model-invocation = %q, want unset", s, v)
		}
	}
}

// bash tests/skills.test.sh user-invoked skills set disable-model-invocation and keep a description
func TestUserInvokedSkillsSetDisableModelInvocation(t *testing.T) {
	for _, s := range userInvokedSkills {
		md := skillMD(t, s)
		if v := frontmatterField(md, "disable-model-invocation"); v != "true" {
			t.Errorf("%s: disable-model-invocation = %q, want true", s, v)
		}
		if frontmatterField(md, "description") == "" {
			t.Errorf("%s: empty description (the menu needs one)", s)
		}
	}
}

// bash tests/skills.test.sh every file a skill points to exists beside it
func TestEveryFileASkillPointsToExists(t *testing.T) {
	refs := skillFileRefs(skillMD(t, "lead"))
	for _, want := range []string{"review-brief.md"} {
		if !contains(refs, want) {
			t.Errorf("precondition: the lead skill does not name %s (refs %v)", want, refs)
		}
	}
	for _, d := range skillDirs(t) {
		for _, ref := range skillFileRefs(skillMD(t, d)) {
			if _, err := os.Stat(filepath.Join(pluginRoot, "skills", d, ref)); err != nil {
				t.Errorf("%s/%s named by the skill does not exist beside it", d, ref)
			}
		}
	}
	got := skillFileRefs("Read `ghost.md` and `templates/agents/x.md`.\n")
	eqStrings(t, []string{"ghost.md"}, got, "refs: bare names only")
}

var removedSkillRe = regexp.MustCompile(`(obor:|skills/)(` + strings.Join(removedSkills, "|") + `)([^a-z0-9-]|$)`)

// removedRefs is the "file:line" of every line under the paths naming a removed skill (`/obor:<name>`, `obor:<name>`,
// `skills/<name>`). Go tests are skipped: they use `/obor:worker` as a sample slash command for the wrong-tab guard.
func removedRefs(t *testing.T, paths ...string) []string {
	t.Helper()
	var hits []string
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		files := []string{p}
		if fi.IsDir() {
			files = walkFiles(t, p)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			for i, l := range strings.Split(string(b), "\n") {
				if removedSkillRe.MatchString(l) {
					hits = append(hits, f+":"+strconv.Itoa(i+1)+":"+l)
				}
			}
		}
	}
	return hits
}

// bash tests/skills.test.sh no reference to a removed skill in skills, templates, docs/agents, AGENTS.md, README, bin, internal, hooks
func TestNoReferenceToARemovedSkill(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"r/skills/a.md": "run `/obor:integrator` per task\nthe obor:lead skill, skills/lead/x.md, obor:worker-ish\n",
	})
	probe := strings.Join(removedRefs(t, filepath.Join(root, "r")), "\n")
	if !strings.Contains(probe, "/obor:integrator") {
		t.Fatalf("precondition: the grep finds a removed name, got %q", probe)
	}
	if strings.Contains(probe, "obor:lead") {
		t.Fatalf("precondition: kept skills are no hit, got %q", probe)
	}
	var paths []string
	for _, p := range []string{"skills", "templates", "docs/agents", "AGENTS.md", "README.md", "bin", "internal", "hooks"} {
		if _, err := os.Stat(filepath.Join(pluginRoot, p)); err == nil {
			paths = append(paths, filepath.Join(pluginRoot, p))
		}
	}
	if !contains(paths, filepath.Join(pluginRoot, "internal")) {
		t.Fatalf("precondition: paths found, got %v", paths)
	}
	if hits := removedRefs(t, paths...); len(hits) > 0 {
		t.Errorf("references to removed skills:\n%s", strings.Join(hits, "\n"))
	}
}

var (
	roleDocRefRe = regexp.MustCompile(`templates/agents/[a-z][a-z0-9-]*\.md`)
	readmeRowRe  = regexp.MustCompile(`\]\(([a-z][a-z0-9-]*\.md)\)`)
)

// bash tests/skills.test.sh role skill names /obor:role, and every referenced role doc exists
func TestRoleSkillNamesSlashRoleAndEveryReferencedRoleDocExists(t *testing.T) {
	md := skillMD(t, "role")
	for _, want := range []string{"/obor:role <name>", "templates/agents/<name>.md"} {
		if !strings.Contains(md, want) {
			t.Errorf("role skill lacks %q", want)
		}
	}
	for _, f := range []string{"advisor.md", "triage.md"} {
		if _, err := os.Stat(filepath.Join(pluginRoot, "templates/agents", f)); err != nil {
			t.Errorf("%s stub missing", f)
		}
	}
	set := map[string]bool{}
	for _, f := range walkFiles(t, filepath.Join(pluginRoot, "skills"), filepath.Join(pluginRoot, "docs/agents"),
		filepath.Join(pluginRoot, "templates")) {
		for _, m := range roleDocRefRe.FindAllString(readFile(t, f), -1) {
			set[m] = true
		}
	}
	for _, m := range readmeRowRe.FindAllStringSubmatch(readFile(t, filepath.Join(pluginRoot, "templates/agents/README.md")), -1) {
		set["templates/agents/"+m[1]] = true
	}
	if !set["templates/agents/advisor.md"] {
		t.Fatalf("precondition: README rows found, got %v", set)
	}
	for ref := range set {
		if _, err := os.Stat(filepath.Join(pluginRoot, ref)); err != nil {
			t.Errorf("referenced role doc does not exist: %s", ref)
		}
	}
}

// bash tests/skills.test.sh role texts have no tuning log
func TestRoleTextsHaveNoTuningLog(t *testing.T) {
	var files []string
	for _, g := range []string{"templates/agents/*.md", "docs/agents/*.md", "skills/*/*.md"} {
		m, err := filepath.Glob(filepath.Join(pluginRoot, g))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("precondition: no role texts")
	}
	for _, f := range files {
		for _, l := range strings.Split(readFile(t, f), "\n") {
			if strings.HasPrefix(l, "## Tuning log") {
				t.Errorf("%s has a Tuning log section", f)
				break
			}
		}
	}
}
