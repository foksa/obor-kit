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
	v := &validator{c: c, d: &c.Data}
	v.top()
	v.models()
	v.lanes()
	v.difficulty()
	v.classes()
	v.flakeRegex()
	v.nightlyCommands()
	v.integrator()
	v.customRoles()
	v.roleDocs()
	v.optional()
	v.laneNames()
	v.trunk()
	v.workflow()
	v.laneEnv()
	v.terminal()
	v.projectFiles()
	return append(c.warnings(o), v.warns...), v.fails
}

// pluginKind: kinds.<k>.command names no compiled kind, is executable and its describe answers with k's manifest
// (agents.PluginLoader.Check, T-1036: it runs the command once). The kind as it answered is the one the rest of the
// validation checks efforts against (T-1042: no cache needed, a stale one not read).
func (v *validator) pluginKind(k string) {
	path := ".kinds" + key(k) + ".command"
	switch {
	case agents.Plugins == nil:
		v.fail(path, "this obor binary runs no plugin kinds")
	case !agents.IsPlugin(k):
		if _, compiled := agents.Lookup(k); compiled {
			v.fail(path, k+" is a compiled kind: a plugin kind takes a name of its own")
			return
		}
		fallthrough
	default:
		kind, err := agents.Plugins.Check(k, v.c.KindCommand(k))
		if err != nil {
			v.fail(path, err.Error())
			return
		}
		if v.described == nil {
			v.described = map[string]agents.Kind{}
		}
		v.described[k] = kind
	}
}

// projectFiles: the notes each agent kind has on the project's committed CLI config in the main checkout (T-1033,
// agents.ProjectWarnings): a file that does not parse, a key that switches part of obor off. Never a failure. A plugin
// kind pluginKind described answers as described (T-1054: the registry's has no manifest before a cache).
func (v *validator) projectFiles() {
	if v.c.Root == "" {
		return
	}
	for _, w := range agents.ProjectWarnings(v.c.Root, os.ReadFile, v.described) {
		v.warns = append(v.warns, "WARN "+w)
	}
}

// ValidateOptions are what Validate cannot know itself; a nil func skips its check.
type ValidateOptions struct {
	Overlap func(a, b string) bool // two path globs may match a common path (spec.Overlap)
}

type validator struct {
	c     *Config
	d     *Data
	warns []string
	fails []string
	// described are the plugin kinds as their describe answered (pluginKind), for the model refs' efforts
	described map[string]agents.Kind
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

// lanes: a count is not negative; a list's names are unique and of the L-NN shape (T-1031: no name to collide with
// a role worktree).
func (v *validator) lanes() {
	if !v.d.Lanes.Listed() {
		if v.d.Lanes.N < 0 {
			v.fail(".lanes", fmt.Sprintf("must be a non-negative integer (how many lanes), got %d", v.d.Lanes.N))
		}
		return
	}
	seen := map[string]bool{}
	for i, e := range v.d.Lanes.List {
		at := fmt.Sprintf(".lanes[%d].name", i)
		switch {
		case !LaneNameRE.MatchString(e.Name):
			v.fail(at, "must be a lane name L-NN (L-01, L-02 …), got "+Compact(e.Name))
		case seen[e.Name]:
			v.fail(at, "lane "+e.Name+" is listed twice")
		}
		seen[e.Name] = true
	}
}

// LaneNameRE is a lane's name as LaneName writes it: L-01 … L-99, L-100 ….
var LaneNameRE = regexp.MustCompile(`^L-(0[1-9]|[1-9][0-9]+)$`)

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
		if v.d.Kinds.M[k].Command != "" {
			v.pluginKind(k)
		} else if _, kit := KitKinds[k]; !kit {
			bridged(".kinds"+key(k), k)
		}
		if slices.Contains(v.d.Kinds.M[k].Args, "--") {
			v.fail(".kinds"+key(k)+".args", "`--` is not allowed: obor adds the first prompt itself")
		}
	}
	for _, k := range v.c.PluginKinds() {
		// a config from before T-1046 that uses a kit kind runs the kit's binary, until v0.32.0
		if v.d.Kinds.M[k].Command == "" && v.c.namesKind(k) {
			v.warns = append(v.warns, fmt.Sprintf("WARN kinds.%s.command: not set, so obor runs the kit's %s (obor "+
				"setup agents sets it; v0.32.0 drops the default)", k, KitKinds[k]))
			v.pluginKind(k)
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
var gatingCommands = []string{"lane_setup", "compile", "check", "check_milestone", "baseline", "gate", "e2e", "guard"}

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

// classes: merge.classes names are unique (RV-03 #3) and with_full is refused on a full class, which already runs
// check (T-1007).
func (v *validator) classes() {
	seen := map[string]int{}
	for i, class := range v.d.Merge.Classes {
		if class.Full && class.WithFull {
			v.fail(".merge.classes"+idx(i)+".with_full", "meaningless with full: true: a full class already runs check")
		}
		if j, dup := seen[class.Name]; dup {
			v.fail(".merge.classes"+idx(i)+".name", "duplicate of .merge.classes"+idx(j)+".name ("+Compact(class.Name)+")")
			continue
		}
		seen[class.Name] = i
	}
}

// flakeRegex: merge.flake_regex is a pair — the failed-test line, then the failed-package line — and each compiles and
// captures its name in the group named for it (T-1047). Unset is fine: no flake parsing.
func (v *validator) flakeRegex() {
	pair := v.d.Merge.FlakeRegex
	switch len(pair) {
	case 0:
		return
	case 2:
	default:
		v.fail(".merge.flake_regex", fmt.Sprintf("must be a pair (the failed-test line, then the failed-package line), got %d", len(pair)))
		return
	}
	for i, group := range []string{"test", "package"} {
		at := ".merge.flake_regex" + idx(i)
		re, err := regexp.Compile(pair[i])
		if err != nil {
			v.fail(at, "is no regexp: "+err.Error())
			continue
		}
		if re.SubexpIndex(group) < 0 {
			v.fail(at, "has no (?P<"+group+">…) named group: the name to read from the line")
		}
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
	if at := v.d.Merge.NightlyAt; at != "" {
		if _, _, ok := ParseClock(at); !ok {
			v.fail(".merge.nightly_at", "must be a local time HH:MM (00:00 to 23:59), got "+Compact(at))
		}
	}
	v.promote()
	if s := v.d.Slots; s != nil && s.CPUs != nil && *s.CPUs < 1 {
		v.fail(".slots.cpus", fmt.Sprintf("must be a positive integer, got %d", *s.CPUs))
	}
	if s := v.d.Slots; s != nil && s.LaneCPUs != nil && *s.LaneCPUs < 1 {
		v.fail(".slots.lane_cpus", fmt.Sprintf("must be a positive integer, got %d", *s.LaneCPUs))
	}
}

// laneEnv: slots.lane_env's names are env names and its values' one placeholder is {cpus} (T-1041): a value's other
// {…} is a typo, and obor substitutes nothing else.
func (v *validator) laneEnv() {
	s := v.d.Slots
	if s == nil {
		return
	}
	keys := make([]string, 0, len(s.LaneEnv))
	for k := range s.LaneEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := ".slots.lane_env" + key(k)
		if !simpleKey.MatchString(k) {
			v.fail(path, "is no env name: letters, digits and _ only, not starting with a digit, got "+Compact(k))
		}
		for _, ph := range laneEnvPlaceholder.FindAllString(s.LaneEnv[k], -1) {
			if ph != LaneCPUsPlaceholder {
				v.fail(path, "unknown placeholder "+ph+": the one placeholder is "+LaneCPUsPlaceholder+" (slots.lane_cpus)")
			}
		}
	}
}

// laneEnvPlaceholder is a {…} in a slots.lane_env value; {cpus} is the one validate takes (LaneCPUsPlaceholder).
var laneEnvPlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// workflow: workflow.kind names a kind serve has an implementation of (T-1029).
func (v *validator) workflow() {
	if k := v.d.Workflow.Kind; k != "" && !slices.Contains(WorkflowKinds, k) {
		v.fail(".workflow.kind", "must be one of "+strings.Join(WorkflowKinds, ", ")+", got "+Compact(k))
	}
}

// terminal: terminal.kind names a terminal obor has an adapter for (T-1044).
func (v *validator) terminal() {
	if k := v.d.Terminal.Kind; k != "" && !slices.Contains(TerminalKinds, k) {
		v.fail(".terminal.kind", "must be one of "+strings.Join(TerminalKinds, ", ")+", got "+Compact(k))
	}
}

// promote: branches.promote (T-974) is a branch name other than branches.main and has commands.check (the suite each
// promotion runs); merge.promote_weight is positive;
// merge.weights names only difficulty levels, each with a positive weight.
func (v *validator) promote() {
	if b := v.d.Branches.Promote; b != "" {
		switch {
		case b == v.d.Branches.Main:
			v.fail(".branches.promote", "must differ from .branches.main ("+Compact(b)+"): serve promotes the target to it")
		case strings.ContainsAny(b, " ~^:?*[\\") || strings.HasPrefix(b, "-") || strings.Contains(b, ".."):
			v.fail(".branches.promote", "is no branch name: "+Compact(b))
		}
		if _, ok := v.c.Command("check"); !ok {
			v.fail(".branches.promote", "needs commands.check: each promotion runs the full suite")
		}
	}
	if w := v.d.Merge.PromoteWeight; w != nil && *w < 1 {
		v.fail(".merge.promote_weight", fmt.Sprintf("must be a positive integer, got %d", *w))
	}
	keys := make([]string, 0, len(v.d.Merge.Weights))
	for k := range v.d.Merge.Weights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch w := v.d.Merge.Weights[k]; {
		case DefaultWeights[k] == 0:
			v.fail(".merge.weights"+key(k), "is not a level: use hard, medium or simple")
		case w < 1:
			v.fail(".merge.weights"+key(k), fmt.Sprintf("must be a positive integer, got %d", w))
		}
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
			case len(e.Verbs) > 0:
				v.fail(path+".verbs", "a built-in role's rights are its own: verbs are a custom role's (T-1039)")
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
		v.verbs(path, e.Verbs)
		if _, isDefault := roles.DefaultRow(k); isDefault {
			continue
		}
		if doc := v.c.RoleDoc(k); !regularFile(filepath.Join(v.c.Root, doc)) {
			v.fail(path, "no role doc: write "+doc+" (what the role does, what it writes)")
		}
	}
}

// verbs: each of a custom role's verbs (roles.<name>.verbs, T-1039) is a wire method a role may be granted
// (roles.Verbs), named once.
func (v *validator) verbs(path string, verbs []string) {
	seen := map[string]bool{}
	for i, m := range verbs {
		switch {
		case !slices.Contains(roles.Verbs, m):
			v.fail(path+".verbs"+idx(i), "unknown wire method "+Compact(m)+": a verb is one of "+strings.Join(roles.Verbs, ", "))
		case seen[m]:
			v.fail(path+".verbs"+idx(i), m+" is listed twice")
		}
		seen[m] = true
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

// laneNames: no name of a lane's worker is a role's agent (T-189, T-191).
func (v *validator) laneNames() {
	names := []string{}
	if rc, _ := v.c.Role("worker"); rc.Configured {
		names = append(names, rc.Agent)
	}
	for _, rc := range v.c.RoleAgents() {
		names = append(names, rc.Agent)
	}
	roleAgents := Unique(names...)
	for _, lane := range v.c.Lanes() {
		for _, alias := range v.c.LaneAgentAliases(lane.Name) {
			for _, agent := range roleAgents {
				if alias == agent {
					v.fail(".lanes", "lane "+lane.Name+"'s agent "+Compact(alias)+" collides with a role agent")
				}
			}
		}
		v.bareLaneName(lane.Name, roleAgents)
	}
}

// bareLaneName: a role agent named like lane's lowercase name (l-02) is no name of lane (lookups take no bare lane
// name, T-302) but reads like one: WARN.
func (v *validator) bareLaneName(lane string, roleAgents []string) {
	lower := strings.ToLower(lane)
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
	// T-962: v0.29 dropped checks.load_limit with the check slots' machine-load wait.
	if ch := c.Data.Checks; ch != nil {
		removed("checks.load_limit", ch.LoadLimit != nil)
	}
	// T-966: v0.29 dropped merge.receipt_max_age with the test receipts.
	removed("merge.receipt_max_age", c.Data.Merge.ReceiptMaxAge != nil)
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
// takes (CheckModelRef, a plugin kind's efforts as its describe answered); "" passes unless required.
func (v *validator) modelRef(path, ref string, required bool) {
	if ref == "" && !required {
		return
	}
	if err := v.c.checkModelRef(ref, v.described); err != nil {
		v.fail(path, err.Error())
	}
}

// modelUsed: a level (its own list or the default), a role or an agent of c names model.
func (c *Config) modelUsed(model string) bool { return slices.Contains(c.UsedModels(), model) }

// namesKind: c names kind k, as kinds.<k>, a models entry's kind or the kind of a model it uses.
func (c *Config) namesKind(k string) bool {
	if _, ok := c.Data.Kinds.M[k]; ok {
		return true
	}
	for _, name := range c.Data.Models.Keys {
		if c.Data.Models.M[name].Kind == k {
			return true
		}
	}
	for _, name := range c.UsedModels() {
		if m, ok := c.Model(name); ok && m.Kind == k {
			return true
		}
	}
	return false
}

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
