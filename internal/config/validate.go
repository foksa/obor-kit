package config

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"obor/internal/agents"
	"obor/internal/roles"
)

var (
	simpleKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// PrefixRe is what a project prefix (and an obor init --prefix) must match.
	PrefixRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,7}$`)
)

// Validate is `obor config validate`: "FAIL <path>: <why>" per problem, and "WARN …" notes that are no failure (a
// removed key still set, a merge.neutral glob overlapping a class glob, an agent named like another lane).
// Shapes (types, unknown keys) are checked by Load; these are the checks a typed decode cannot make.
func (c *Config) Validate(o ValidateOptions) (warns, fails []string) {
	v := &validator{c: c, d: &c.Data, stateAgent: o.StateAgent}
	v.top()
	v.models()
	v.lanes()
	v.difficulty()
	v.classes()
	v.nightlyCommands()
	v.integrator()
	v.customRoles()
	v.roleDocs()
	v.optional()
	v.laneAgents()
	v.trunk()
	return append(c.warnings(o), v.warns...), v.fails
}

// ValidateOptions are what Validate cannot know itself; a nil func skips its check.
type ValidateOptions struct {
	Overlap func(a, b string) bool // two path globs may match a common path (spec.Overlap)
	// StateAgent is the agent a lane's row names (project.LaneAgent; nil: rows not looked at).
	StateAgent func(lane string) string
}

type validator struct {
	c          *Config
	d          *Data
	stateAgent func(lane string) string
	warns      []string
	fails      []string
}

func (v *validator) fail(path, why string) { v.fails = append(v.fails, "FAIL "+path+": "+why) }

// key is a path step: .name, or ["name"] when name is no identifier.
func key(k string) string {
	if simpleKey.MatchString(k) {
		return "." + k
	}
	return "[" + Compact(k) + "]"
}

func idx(i int) string { return "[" + strconv.Itoa(i) + "]" }

func (v *validator) top() {
	if v.d.Version == nil || *v.d.Version != 1 {
		v.fail(".version", "must be 1, got "+Compact(v.d.Version))
	}
	if v.d.Prefix == "" {
		v.fail(".prefix", "required string, got null")
	} else if !PrefixRe.MatchString(v.d.Prefix) {
		v.fail(".prefix", "must match "+PrefixRe.String()+", got "+Compact(v.d.Prefix))
	}
}

// lanes: the count is not negative (lanes are L-01 … L-NN: no name to collide with a role worktree).
func (v *validator) lanes() {
	if v.d.Lanes < 0 {
		v.fail(".lanes", fmt.Sprintf("must be a non-negative integer (how many lanes), got %d", v.d.Lanes))
	}
}

// models: every models entry and kinds key names a kind with a bridge (agents.Lookup), no kinds args carry a bare
// `--` (obor adds the first prompt after its own options, so a `--` would turn --settings and --resume into
// positionals, RV-47-verify-3 N2); each role names a known model with an effort its kind takes (T-695).
func (v *validator) models() {
	bridged := func(path, kind string) {
		if _, ok := agents.Lookup(kind); !ok {
			v.fail(path, Compact(kind)+" is not a supported kind: use one of "+strings.Join(agents.Names(), ", "))
		}
	}
	for _, k := range v.d.Models.Keys {
		e := v.d.Models.M[k]
		if _, isBuiltin := BuiltinModelNamed(k); e.Kind == "" && !isBuiltin {
			v.fail(".models"+key(k)+".kind", "required (claude, codex, opencode)")
			continue
		}
		if e.Kind != "" {
			bridged(".models"+key(k)+".kind", e.Kind)
		}
	}
	for _, k := range v.d.Kinds.Keys {
		bridged(".kinds"+key(k), k)
		if slices.Contains(v.d.Kinds.M[k].Args, "--") {
			v.fail(".kinds"+key(k)+".args", "`--` is not allowed: obor adds the first prompt itself")
		}
	}
	for _, k := range v.d.Roles.Keys {
		e, path := v.d.Roles.M[k], ".roles"+key(k)
		if e.Object {
			path += ".model"
		}
		v.modelRef(path, e.Model, false)
	}
}

// gatingCommands are the commands a merge or the nightly gates on, a merge class's checks beside them:
// merge.nightly_commands must name none of them (T-817).
var gatingCommands = []string{"lane_setup", "check", "check_milestone", "baseline", "gate", "e2e", "guard"}

// nightlyCommands: every merge.nightly_commands entry is a defined commands.<name> and no gating command — nightly
// runs it non-gating, after its gates (T-817).
func (v *validator) nightlyCommands() {
	gating := slices.Clone(gatingCommands)
	for _, class := range v.d.Merge.Classes {
		gating = append(gating, class.Checks...)
	}
	for i, name := range v.d.Merge.NightlyCommands {
		path := ".merge.nightly_commands" + idx(i)
		if _, ok := v.c.Command(name); !ok {
			v.fail(path, "no commands"+key(name)+": define it, or remove it")
			continue
		}
		if slices.Contains(gating, name) {
			v.fail(path, Compact(name)+" is a gating command: nightly runs it as a gate, never non-gating")
		}
	}
}

// classes: merge.classes names are unique (RV-03 #3).
func (v *validator) classes() {
	seen := map[string]int{}
	for i, class := range v.d.Merge.Classes {
		if j, dup := seen[class.Name]; dup {
			v.fail(".merge.classes"+idx(i)+".name", "duplicate of .merge.classes"+idx(j)+".name ("+Compact(class.Name)+")")
			continue
		}
		seen[class.Name] = i
	}
}

// integrator: every project needs one (T-428). serve dispatches and merges only for a project with roles.integrator,
// so without the entry nothing lands: a v0.9.4 project that relied on the default <prefix>-int agent, or one written
// without the role, would otherwise look valid and only queue up work.
func (v *validator) integrator() {
	if _, ok := v.d.Roles.entry("integrator"); !ok {
		v.fail(".roles.integrator", "required: serve dispatches and merges nothing without an integrator")
	}
}

// optional checks the optional settings: handoff_tokens (T-175), guards.allow_push (T-105), guards.deny_commands
// (T-905).
func (v *validator) optional() {
	v.nonNegative(".handoff_tokens", v.d.HandoffTokens)
	if g := v.d.Guards; g != nil {
		v.allowPush(g.AllowPush)
		v.denyCommands(g.DenyCommands)
	}
	if v.d.Merge.ReceiptMaxAge < 0 {
		v.fail(".merge.receipt_max_age", fmt.Sprintf("must be a non-negative integer (seconds), got %d", v.d.Merge.ReceiptMaxAge))
	}
	if at := v.d.Merge.NightlyAt; at != "" {
		if _, _, ok := ParseClock(at); !ok {
			v.fail(".merge.nightly_at", "must be a local time HH:MM (00:00 to 23:59), got "+Compact(at))
		}
	}
	if s := v.d.Slots; s != nil && s.CPUs != nil && *s.CPUs < 1 {
		v.fail(".slots.cpus", fmt.Sprintf("must be a positive integer, got %d", *s.CPUs))
	}
	if s := v.d.Slots; s != nil && s.LaneCPUs != nil && *s.LaneCPUs < 1 {
		v.fail(".slots.lane_cpus", fmt.Sprintf("must be a positive integer, got %d", *s.LaneCPUs))
	}
	if c := v.d.Checks; c != nil && c.LoadLimit != nil && !(*c.LoadLimit >= 0) {
		v.fail(".checks.load_limit", fmt.Sprintf("must be a non-negative number (a load average; 0: never wait), got %v", *c.LoadLimit))
	}
}

// nonNegative: absent, or not negative (the decode made it an integer).
func (v *validator) nonNegative(path string, n *int64) {
	if n != nil && *n < 0 {
		v.fail(path, fmt.Sprintf("must be a non-negative integer, got %d", *n))
	}
}

// allowPush: guards.allow_push is a list of roles ("lead"), a role's derived name ("ob-lead") read as its slot
// (T-786); an empty entry fails, one that names no role allows nothing: WARN.
func (v *validator) allowPush(entries []string) {
	for _, e := range entries {
		if e == "" {
			v.fail(".guards.allow_push", `must be a list of roles (e.g. ["lead"])`)
			return
		}
	}
	for _, e := range entries {
		slot := v.c.pushSlot(e)
		rc, _ := v.c.Role(slot)
		switch {
		case slot == "":
			v.warns = append(v.warns, "WARN .guards.allow_push: "+Compact(e)+" is no role (nor a role's agent name): it allows no push")
		case rc.Role.Custom:
			v.fail(".guards.allow_push", Compact(e)+" is a custom role: it never pushes (remove it)")
		}
	}
}

// denyCommands: every guards.deny_commands entry has a match that compiles and the message the guard denies with
// (T-905). An empty match is refused too: it would deny every command.
func (v *validator) denyCommands(rules []DenyCommand) {
	for i, r := range rules {
		path := ".guards.deny_commands" + idx(i)
		switch {
		case r.Match == "":
			v.fail(path+".match", "required: a regular expression on the resolved command line")
		default:
			if _, err := regexp.Compile(r.Match); err != nil {
				v.fail(path+".match", "must be a regular expression: "+strings.TrimPrefix(err.Error(), "error parsing regexp: "))
			}
		}
		if r.Message == "" {
			v.fail(path+".message", "required: the text the guard denies with")
		}
	}
}

// customRoles: each roles.<name> that is no table role defines a custom role (T-801): a name roles.CheckCustomName
// takes, write paths inside the project and outside .obor/, and its doc <paths.agents>/<name>.md (a kit default row
// may use the kit's). A table role takes a model ref string; the driver's is never configured (obor drive starts it).
func (v *validator) customRoles() {
	for _, k := range v.d.Roles.Keys {
		e, path := v.d.Roles.M[k], ".roles"+key(k)
		if r, table := roles.Lookup(k); table {
			switch {
			case r.Name == roles.Driver:
				v.fail(path, "the drive agent's role is not configured: obor drive starts it")
			case e.Object:
				v.fail(path, "a built-in role takes a model ref string, not an object")
			}
			continue
		}
		if err := roles.CheckCustomName(k); err != nil {
			v.fail(path, err.Error()+", got "+Compact(k))
			continue
		}
		for i, w := range e.Write {
			if why := writePathProblem(w); why != "" {
				v.fail(path+".write"+idx(i), why+", got "+Compact(w))
			}
		}
		if _, isDefault := roles.DefaultRow(k); isDefault {
			continue
		}
		if doc := v.c.RoleDoc(k); !regularFile(filepath.Join(v.c.Root, doc)) {
			v.fail(path, "no role doc: write "+doc+" (what the role does, what it writes)")
		}
	}
}

// writePathProblem is why a custom role's write path w cannot be one ("": it can): it is relative to the project
// root, stays inside it and not in .obor/ (obor's own state).
func writePathProblem(w string) string {
	clean := path.Clean(w)
	switch {
	case w == "":
		return "an empty path"
	case strings.HasPrefix(w, "/") || clean == ".." || strings.HasPrefix(clean, "../"):
		return "outside the project: a write path is relative to its root"
	case clean == "." || clean == ".obor" || strings.HasPrefix(clean, ".obor/"):
		return "in .obor/: obor's own state is no role's to write"
	}
	return ""
}

// roleDocs: a role doc under paths.agents (first line "# Role: …") named like a custom role the config has no row for
// is no role's (no agent runs it: a row removed, or never added): WARN. A kit default row's doc is not (obor init stubs
// one for each, configured or not).
func (v *validator) roleDocs() {
	dir := v.c.AgentsDir()
	entries, _ := os.ReadDir(filepath.Join(v.c.Root, dir))
	for _, ent := range entries {
		name, ok := strings.CutSuffix(ent.Name(), ".md")
		if _, isDefault := roles.DefaultRow(name); !ok || isDefault || roles.CheckCustomName(name) != nil {
			continue
		}
		if rc, known := v.c.Role(name); known && rc.Configured {
			continue
		}
		b, err := os.ReadFile(filepath.Join(v.c.Root, dir, ent.Name()))
		if err != nil || !bytes.HasPrefix(b, []byte("# Role:")) {
			continue
		}
		v.warns = append(v.warns, "WARN .roles"+key(name)+": "+path.Join(dir, ent.Name())+" is a role doc but roles."+
			name+" is not set: no agent runs it (add the row, or remove the doc)")
	}
}

// regularFile: p is a regular file.
func regularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// laneAgents: no name of a lane's worker is a role's agent (T-189, T-191).
func (v *validator) laneAgents() {
	names := []string{}
	if rc, _ := v.c.Role("worker"); rc.Configured {
		names = append(names, rc.Agent)
	}
	for _, rc := range v.c.RoleAgents() {
		names = append(names, rc.Agent)
	}
	roleAgents := Unique(names...)
	for _, lane := range v.c.Lanes() {
		state := ""
		if v.stateAgent != nil {
			state = v.stateAgent(lane.Name)
		}
		for _, alias := range v.c.LaneAgentAliases(lane.Name, state) {
			for _, agent := range roleAgents {
				if alias == agent {
					v.fail(".lanes", "lane "+lane.Name+"'s agent "+Compact(alias)+" collides with a role agent")
				}
			}
		}
		v.bareLaneName(lane.Name, state, roleAgents)
	}
}

// bareLaneName: a role agent named like lane's lowercase name (l-02) is no name of lane (lookups take no bare lane
// name, T-302) but reads like one: WARN. The lane's own row of that name is its real name.
func (v *validator) bareLaneName(lane, state string, roleAgents []string) {
	lower := strings.ToLower(lane)
	if state == lower {
		return
	}
	for _, agent := range roleAgents {
		if agent == lower {
			v.warns = append(v.warns, "WARN .lanes: agent "+Compact(agent)+" is named like lane "+lane+
				" but is not its agent (lookups take no bare lane name)")
		}
	}
}

// trunk: refs/heads/<branches.main> resolves in the main checkout; serve pauses the project when it does not.
func (v *validator) trunk() {
	name := v.d.Branches.Main
	if name == "" || exec.Command("git", "-C", v.c.Root, "rev-parse", "--git-dir").Run() != nil {
		return // no repository to look in (a bare config directory)
	}
	if exec.Command("git", "-C", v.c.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run() != nil {
		v.fail(".branches.main", "refs/heads/"+name+" does not exist: set branches.main: your trunk is not "+name)
	}
}

// warnings: merge.neutral globs that may share a path with a class glob (that class's files would merge guard-only,
// T-92), removed keys still set. Each removed key WARNs for two minor versions after its removal, then is an unknown
// key (validate_test.go: TestRemovedKeysWithinTwoVersions, its window read from VERSION).
func (c *Config) warnings(o ValidateOptions) []string {
	var out []string
	neutral := c.Data.Merge.Neutral
	if neutral == nil {
		neutral = []string{"docs/**"}
	}
	for _, n := range neutral {
		for _, class := range c.Data.Merge.Classes {
			for _, glob := range class.Paths {
				if o.Overlap != nil && n != "" && glob != "" && o.Overlap(n, glob) {
					out = append(out, "WARN merge.neutral glob "+n+" overlaps class "+class.Name+" glob "+glob)
				}
			}
		}
	}
	removed := func(path string, set bool) {
		if set {
			out = append(out, "WARN "+path+": no longer used, remove it")
		}
	}
	removed("paths.research", c.Data.Paths["research"] != "")
	removed("paths.questions", c.Data.Paths["questions"] != "")
	// T-864: keys no project sets, and the sections that held no key; each WARNs for two minors (v0.28).
	removed("integrator_handoff_tokens", c.Data.IntegratorHandoffTokens != nil)
	removed("stop_idle_workers", c.Data.StopIdleWorkers != nil)
	removed("merge.timeout_grep", c.Data.Merge.TimeoutGrep != nil)
	removed("merge.max_load", c.Data.Merge.MaxLoad != nil)
	removed("merge.load_wait", c.Data.Merge.LoadWait != nil)
	removed("merge.window_wait", c.Data.Merge.WindowWait != nil)
	if s := c.Data.Slots; s != nil {
		removed("slots.merge_block_max", s.MergeBlockMax != nil)
	}
	removed("mcp", c.Data.MCP != nil)
	removed("cache", c.Data.Cache != nil)
	for _, name := range c.ModelNames() { // T-705: opencode's own default model may refuse requests, and effort needs one (T-703)
		m, _ := c.Model(name)
		if kd, known := agents.Lookup(m.Kind); known && kd.Caps().ModelArg && m.ID == "" && c.modelUsed(name) {
			out = append(out, "WARN models."+name+": opencode model without a model id (opencode's own default may not "+
				"work, and an effort needs one): set its \"model\", e.g. \"opencode-go/deepseek-v4.1-flash\"")
		}
	}
	return out
}

// difficulty: each level of .difficulty is hard, medium or simple and names models the config knows (each may name
// an effort, T-695).
func (v *validator) difficulty() {
	keys := make([]string, 0, len(v.d.Difficulty))
	for k := range v.d.Difficulty {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := DifficultyDefault[k]; !ok {
			v.fail(".difficulty"+key(k), "is not a level: use hard, medium or simple")
			continue
		}
		for i, t := range v.d.Difficulty[k] {
			v.modelRef(".difficulty"+key(k)+idx(i), t, true)
		}
	}
}

// modelRef: a model ref `<model>[:<effort>]` at path names a model of the config and, with an effort, one its kind
// takes (CheckModelRef); "" passes unless required.
func (v *validator) modelRef(path, ref string, required bool) {
	if ref == "" && !required {
		return
	}
	if err := v.c.CheckModelRef(ref); err != nil {
		v.fail(path, err.Error())
	}
}

// modelUsed: a level (its own list or the default), a role or an agent of c names model.
func (c *Config) modelUsed(model string) bool { return slices.Contains(c.UsedModels(), model) }

// UsedModels are the models a level (its own list or the default), a role or an agent of c names, known or not, in
// level (hard, medium, simple), role, agent order.
func (c *Config) UsedModels() []string {
	var refs []string
	for _, level := range []string{"hard", "medium", "simple"} {
		refs = append(refs, c.DifficultyList(level)...)
	}
	for _, k := range c.Data.Roles.Keys {
		refs = append(refs, c.Data.Roles.M[k].Model)
	}
	var out []string
	for _, ref := range refs {
		out = append(out, func() string { name, _ := ParseModelRef(ref); return name }())
	}
	return Unique(out...)
}
