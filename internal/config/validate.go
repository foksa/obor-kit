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
// removed key still set, a merge.by_path.no_check glob overlapping a rule's glob, an agent named like another lane, a
// config of shape 1).
// Shapes are checked by the load (types by Load, unknown keys by LoadForValidate); these are the checks a typed decode cannot make.
func (c *Config) Validate(o ValidateOptions) (warns, fails []string) {
	v := &validator{c: c, d: &c.Data}
	v.top()
	v.fixedCommands()
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

// pluginKind: agents.kinds.<k>.plugin names no compiled kind, is executable and its describe answers with k's manifest
// (agents.PluginLoader.Check, T-1036: it runs the command once). The kind as it answered is the one the rest of the
// validation checks efforts against (T-1042: no cache needed, a stale one not read).
func (v *validator) pluginKind(k string) {
	path := ".agents.kinds" + key(k) + ".plugin"
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

// top: the version is the shape this obor reads (a file of shape 1 is one WARN, not a FAIL: it runs reshaped until
// serve rewrites it, and the merge guard runs validate in between), and the agent prefix is set.
func (v *validator) top() {
	switch {
	case v.c.Shape1():
		v.warns = append(v.warns, "WARN .version: config shape 1, read as shape 2 for this release and refused by the"+
			" next: serve rewrites it at its start (obor config migrate by hand)")
	case v.d.Version == nil || *v.d.Version != ShapeVersion:
		v.fail(".version", fmt.Sprintf("must be %d, got %s", ShapeVersion, Compact(v.d.Version)))
	}
	if p := v.d.Project.AgentPrefix; p == "" {
		v.fail(".project.agent_prefix", "required string, got null")
	} else if !PrefixRe.MatchString(p) {
		v.fail(".project.agent_prefix", "must match "+PrefixRe.String()+", got "+Compact(p))
	}
}

// fixedCommands: commands names none of the commands obor runs by a fixed name (FixedCommands): each has its home in
// its group, and commands.<name> of one would never run.
func (v *validator) fixedCommands() {
	for _, name := range v.d.Commands.Keys {
		if at, fixed := FixedCommands[name]; fixed {
			v.fail(".commands"+key(name), "obor runs "+name+" from "+strings.Join(at, ".")+": move it there")
		}
	}
}

// lanes: a count is not negative and not set beside a list; a list's names are unique and of the L-NN shape (T-1031:
// no name to collide with a role worktree).
func (v *validator) lanes() {
	l := v.d.Lanes
	if l.List == nil {
		if l.Count < 0 {
			v.fail(".lanes.count", fmt.Sprintf("must be a non-negative integer (how many lanes), got %d", l.Count))
		}
		return
	}
	if l.Count != 0 {
		v.fail(".lanes.count", "set beside lanes.list: name the lanes one way (the list wins)")
	}
	seen := map[string]bool{}
	for i, e := range l.List {
		at := fmt.Sprintf(".lanes.list[%d].name", i)
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

// models: every agents.models entry and agents.kinds key names a kind with a bridge (agents.Lookup), no kinds args carry a bare
// `--` (obor adds the first prompt after its own options, so a `--` would turn --settings and --resume into
// positionals, RV-47-verify-3 N2); each role names a known model with an effort its kind takes (T-695).
func (v *validator) models() {
	bridged := func(path, kind string) {
		if _, ok := agents.Lookup(kind); !ok {
			v.fail(path, Compact(kind)+" is not a supported kind: use one of "+strings.Join(agents.Names(), ", "))
		}
	}
	a := v.d.Agents
	for _, k := range a.Models.Keys {
		e := a.Models.M[k]
		if _, isBuiltin := BuiltinModelNamed(k); e.Kind == "" && !isBuiltin {
			v.fail(".agents.models"+key(k)+".kind", "required (claude, codex, opencode)")
			continue
		}
		if e.Kind != "" {
			bridged(".agents.models"+key(k)+".kind", e.Kind)
		}
	}
	for _, k := range a.Kinds.Keys {
		if a.Kinds.M[k].Plugin != "" {
			v.pluginKind(k)
		} else if _, kit := KitKinds[k]; !kit {
			bridged(".agents.kinds"+key(k), k)
		}
		if slices.Contains(a.Kinds.M[k].Args, "--") {
			v.fail(".agents.kinds"+key(k)+".args", "`--` is not allowed: obor adds the first prompt itself")
		}
	}
	for _, k := range v.c.PluginKinds() {
		// a config from before T-1046 that uses a kit kind runs the kit's binary, until v0.33.0
		if a.Kinds.M[k].Plugin == "" && v.c.namesKind(k) {
			v.warns = append(v.warns, fmt.Sprintf("WARN agents.kinds.%s.plugin: not set, so obor runs the kit's %s (obor "+
				"setup agents sets it; v0.33.0 drops the default)", k, KitKinds[k]))
			v.pluginKind(k)
		}
	}
	for _, k := range a.Roles.Keys {
		e, path := a.Roles.M[k], ".agents.roles"+key(k)
		if e.Object {
			path += ".model"
		}
		v.modelRef(path, e.Model, false)
	}
}

// gatingCommands are the commands a merge or the nightly gates on, a by_path rule's run beside them:
// nightly.also_run must name none of them (T-817).
var gatingCommands = []string{"lane_setup", "compile", "check", "check_milestone", "guard"}

// nightlyCommands: every nightly.also_run entry is a defined command and no gating command — nightly runs it
// non-gating, after its gates (T-817).
func (v *validator) nightlyCommands() {
	gating := slices.Clone(gatingCommands)
	for _, rule := range v.d.Merge.ByPath.Rules {
		gating = append(gating, rule.Run...)
	}
	for i, name := range v.d.Nightly.AlsoRun {
		path := ".nightly.also_run" + idx(i)
		if _, ok := v.c.Command(name); !ok {
			v.fail(path, "no commands"+key(name)+": define it, or remove it")
			continue
		}
		if slices.Contains(gating, name) {
			v.fail(path, Compact(name)+" is a gating command: nightly runs it as a gate, never non-gating")
		}
	}
}

// commandStillRuns: a by_path rule's run, merge.by_path.guard or nightly.also_run names name, so it runs there even
// though obor no longer runs it by its own name (T-1083).
func (c *Config) commandStillRuns(name string) bool {
	if c.Data.Merge.ByPath.Guard == name || slices.Contains(c.Data.Nightly.AlsoRun, name) {
		return true
	}
	for _, rule := range c.Data.Merge.ByPath.Rules {
		if slices.Contains(rule.Run, name) {
			return true
		}
	}
	return false
}

// classes: merge.by_path.rules names are unique (RV-03 #3) and also_on_full is refused on a full rule, which already
// runs merge.check (T-1007).
func (v *validator) classes() {
	seen := map[string]int{}
	for i, rule := range v.d.Merge.ByPath.Rules {
		at := ".merge.by_path.rules" + idx(i)
		if rule.Full && rule.AlsoOnFull {
			v.fail(at+".also_on_full", "meaningless with full: true: a full rule already runs merge.check")
		}
		if j, dup := seen[rule.Name]; dup {
			v.fail(at+".name", "duplicate of .merge.by_path.rules"+idx(j)+".name ("+Compact(rule.Name)+")")
			continue
		}
		seen[rule.Name] = i
	}
}

// flakeRegex: checks.output.failed_test and failed_package are set together, and each compiles and captures its name
// in the group named for it (T-1047). Both unset is fine: no flake parsing.
func (v *validator) flakeRegex() {
	o := v.d.Checks.Output
	pair := []string{o.FailedTest, o.FailedPackage}
	names := []string{".checks.output.failed_test", ".checks.output.failed_package"}
	switch {
	case pair[0] == "" && pair[1] == "":
		return
	case pair[0] == "" || pair[1] == "":
		missing := names[0]
		if pair[1] == "" {
			missing = names[1]
		}
		v.fail(missing, "required beside the other one: the failed-test line and the failed-package line go together")
		return
	}
	for i, group := range []string{"test", "package"} {
		at := names[i]
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

// integrator: every project needs one (T-428). serve dispatches and merges only for a project with
// agents.roles.integrator, so without the entry nothing lands: a v0.9.4 project that relied on the default
// <prefix>-int agent, or one written without the role, would otherwise look valid and only queue up work.
func (v *validator) integrator() {
	if _, ok := v.d.Agents.Roles.entry("integrator"); !ok {
		v.fail(".agents.roles.integrator", "required: serve dispatches and merges nothing without an integrator")
	}
}

// optional checks the optional settings: agents.handoff_tokens (T-175), rules.may_push (T-105), rules.deny_commands
// (T-905), nightly.at, checks.cpus_per_check, lanes.cpus.
func (v *validator) optional() {
	v.nonNegative(".agents.handoff_tokens", v.d.Agents.HandoffTokens)
	v.allowPush(v.d.Rules.MayPush)
	v.denyCommands(v.d.Rules.DenyCommands)
	if at := v.d.Nightly.At; at != "" {
		if _, _, ok := ParseClock(at); !ok {
			v.fail(".nightly.at", "must be a local time HH:MM (00:00 to 23:59), got "+Compact(at))
		}
	}
	if n := v.d.Checks.CPUsPerCheck; n != nil && *n < 1 {
		v.fail(".checks.cpus_per_check", fmt.Sprintf("must be a positive integer, got %d", *n))
	}
	if n := v.d.Lanes.CPUs; n != nil && *n < 1 {
		v.fail(".lanes.cpus", fmt.Sprintf("must be a positive integer, got %d", *n))
	}
}

// laneEnv: lanes.env's names are env names and its values' one placeholder is {cpus} (T-1041): a value's other {…}
// is a typo, and obor substitutes nothing else.
func (v *validator) laneEnv() {
	env := v.d.Lanes.Env
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := ".lanes.env" + key(k)
		if !simpleKey.MatchString(k) {
			v.fail(path, "is no env name: letters, digits and _ only, not starting with a digit, got "+Compact(k))
		}
		for _, ph := range laneEnvPlaceholder.FindAllString(env[k], -1) {
			if ph != LaneCPUsPlaceholder {
				v.fail(path, "unknown placeholder "+ph+": the one placeholder is "+LaneCPUsPlaceholder+" (lanes.cpus)")
			}
		}
	}
}

// laneEnvPlaceholder is a {…} in a lanes.env value; {cpus} is the one validate takes (LaneCPUsPlaceholder).
var laneEnvPlaceholder = regexp.MustCompile(`\{[^}]*\}`)

// workflow: merge.workflow names a kind serve has an implementation of (T-1029).
func (v *validator) workflow() {
	if k := v.d.Merge.Workflow; k != "" && !slices.Contains(WorkflowKinds, k) {
		v.fail(".merge.workflow", "must be one of "+strings.Join(WorkflowKinds, ", ")+", got "+Compact(k))
	}
}

// terminal: project.terminal names a terminal obor has an adapter for (T-1044).
func (v *validator) terminal() {
	if k := v.d.Project.Terminal; k != "" && !slices.Contains(TerminalKinds, k) {
		v.fail(".project.terminal", "must be one of "+strings.Join(TerminalKinds, ", ")+", got "+Compact(k))
	}
}

// nonNegative: absent, or not negative (the decode made it an integer).
func (v *validator) nonNegative(path string, n *int64) {
	if n != nil && *n < 0 {
		v.fail(path, fmt.Sprintf("must be a non-negative integer, got %d", *n))
	}
}

// allowPush: rules.may_push is a list of roles ("lead"); an empty entry fails, one that names no role allows
// nothing: WARN.
func (v *validator) allowPush(entries []string) {
	for _, e := range entries {
		if e == "" {
			v.fail(".rules.may_push", `must be a list of roles (e.g. ["lead"])`)
			return
		}
	}
	for _, e := range entries {
		rc, ok := v.c.Role(e)
		switch {
		case !ok:
			v.warns = append(v.warns, "WARN .rules.may_push: "+Compact(e)+" is no role: it allows no push")
		case rc.Role.Custom:
			v.fail(".rules.may_push", Compact(e)+" is a custom role: it never pushes (remove it)")
		}
	}
}

// denyCommands: every rules.deny_commands entry has a match that compiles and the message the guard denies with
// (T-905). An empty match is refused too: it would deny every command.
func (v *validator) denyCommands(rules []DenyCommand) {
	for i, r := range rules {
		path := ".rules.deny_commands" + idx(i)
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

// customRoles: each agents.roles.<name> that is no table role defines a custom role (T-801): a name
// roles.CheckCustomName takes, write paths inside the project and outside .obor/, and its doc
// <agents.docs_dir>/<name>.md (a kit default row may use the kit's). A table role takes a model ref string; the
// driver's is never configured (obor drive starts it).
func (v *validator) customRoles() {
	for _, k := range v.d.Agents.Roles.Keys {
		e, path := v.d.Agents.Roles.M[k], ".agents.roles"+key(k)
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

// verbs: each of a custom role's verbs (agents.roles.<name>.verbs, T-1039) is a wire method a role may be granted
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

// roleDocs: a role doc under agents.docs_dir (first line "# Role: …") named like a custom role the config has no row for
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
		v.warns = append(v.warns, "WARN .agents.roles"+key(name)+": "+path.Join(dir, ent.Name())+" is a role doc but"+
			" agents.roles."+name+" is not set: no agent runs it (add the row, or remove the doc)")
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

// trunk: refs/heads/<merge.target_branch> resolves in the main checkout; serve pauses the project when it does not.
func (v *validator) trunk() {
	name := v.d.Merge.TargetBranch
	if name == "" || exec.Command("git", "-C", v.c.Root, "rev-parse", "--git-dir").Run() != nil {
		return // no repository to look in (a bare config directory)
	}
	if exec.Command("git", "-C", v.c.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run() != nil {
		v.fail(".merge.target_branch", "refs/heads/"+name+" does not exist: set merge.target_branch: your trunk is not "+
			name)
	}
}

// warnings: merge.by_path.no_check globs that may share a path with a rule's glob (that rule's files would merge
// guard-only, T-92), commands obor no longer runs by name (T-1083). A removed key WARNs for two minor versions after its removal,
// then is an unknown key (validate_test.go: TestRemovedKeysWithinTwoVersions, its window read from VERSION).
func (c *Config) warnings(o ValidateOptions) []string {
	var out []string
	for _, n := range c.Data.Merge.ByPath.NoCheck {
		for _, rule := range c.Data.Merge.ByPath.Rules {
			for _, glob := range rule.Paths {
				if o.Overlap != nil && n != "" && glob != "" && o.Overlap(n, glob) {
					out = append(out, "WARN merge.by_path.no_check glob "+n+" overlaps rule "+rule.Name+" glob "+glob)
				}
			}
		}
	}
	// T-1083: baseline, gate and e2e are free command names again; a config that sets one loses its run silently unless
	// it is named where a merge or the nightly still runs it.
	for _, name := range []string{"baseline", "gate", "e2e"} {
		if _, set := c.Command(name); set && !c.commandStillRuns(name) {
			out = append(out, "WARN commands."+name+": obor no longer runs it at merge or nightly (v0.32); delete it, "+
				"or name it in a merge.by_path.rules run or nightly.also_run")
		}
	}
	for _, name := range c.ModelNames() { // T-705: opencode's own default model may refuse requests, and effort needs one (T-703)
		m, _ := c.Model(name)
		if kd, known := agents.Lookup(m.Kind); known && kd.Caps().ModelArg && m.ID == "" && c.modelUsed(name) {
			out = append(out, "WARN agents.models."+name+": opencode model without a model id (opencode's own default "+
				"may not work, and an effort needs one): set its \"id\", e.g. \"opencode-go/deepseek-v4.1-flash\"")
		}
	}
	return out
}

// difficulty: each level of agents.difficulty is hard, medium or simple and names models the config knows (each may name
// an effort, T-695).
func (v *validator) difficulty() {
	keys := make([]string, 0, len(v.d.Agents.Difficulty))
	for k := range v.d.Agents.Difficulty {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := DifficultyDefault[k]; !ok {
			v.fail(".agents.difficulty"+key(k), "is not a level: use hard, medium or simple")
			continue
		}
		for i, t := range v.d.Agents.Difficulty[k] {
			v.modelRef(".agents.difficulty"+key(k)+idx(i), t, true)
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

// namesKind: c names kind k, as agents.kinds.<k>, an agents.models entry's kind or the kind of a model it uses.
func (c *Config) namesKind(k string) bool {
	if _, ok := c.Data.Agents.Kinds.M[k]; ok {
		return true
	}
	for _, name := range c.Data.Agents.Models.Keys {
		if c.Data.Agents.Models.M[name].Kind == k {
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
	for _, k := range c.Data.Agents.Roles.Keys {
		refs = append(refs, c.Data.Agents.Roles.M[k].Model)
	}
	var out []string
	for _, ref := range refs {
		out = append(out, func() string { name, _ := ParseModelRef(ref); return name }())
	}
	return Unique(out...)
}
