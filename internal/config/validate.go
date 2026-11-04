package config

import (
	"fmt"
	"os/exec"
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
	v.removed()
	v.models()
	v.lanes()
	v.difficulty()
	v.classes()
	v.integrator()
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

// removed refuses a key that was removed with a replacement the reader must use: agents.<name> (T-777: advisor,
// researcher and triage are roles, migrated by obor setup migrate), never a WARN because there is no working shape to
// keep. The WARN-only removals are in warnings.
func (v *validator) removed() {
	if v.d.Agents != nil {
		v.fail(".agents", "no longer a config key: advisor, researcher and triage are roles "+
			"(roles.advisor, roles.researcher, roles.triage); obor setup migrate moves a matching entry and drops the rest")
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
	for _, role := range roles.All() {
		if ref := v.d.Roles.entry(role.Name); ref != nil {
			v.modelRef(".roles."+role.Name, *ref, false)
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
	if v.d.Roles.entry("integrator") == nil {
		v.fail(".roles.integrator", "required: serve dispatches and merges nothing without an integrator")
	}
}

// optional checks the optional settings: handoff_tokens (T-175, integrator T-215), guards.allow_push (T-105).
func (v *validator) optional() {
	v.nonNegative(".handoff_tokens", v.d.HandoffTokens)
	v.nonNegative(".integrator_handoff_tokens", v.d.IntegratorHandoffTokens)
	if g := v.d.Guards; g != nil {
		v.agentNames(".guards.allow_push", g.AllowPush, false)
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

// agentNames: a list (when required) of non-empty names.
func (v *validator) agentNames(path string, names []string, required bool) {
	bad := required && names == nil
	for _, name := range names {
		bad = bad || name == ""
	}
	if bad {
		v.fail(path, "must be a list of herdr agent names")
	}
}

// laneAgents: no name of a lane's worker is a role's agent (T-189, T-191).
func (v *validator) laneAgents() {
	names := []string{}
	if rc, _ := v.c.Role("worker"); rc.Configured {
		names = append(names, rc.Agent)
	}
	for _, rc := range v.c.roleAgents() {
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
// key (the table in validate_test.go: TestRemovedKeysWithinTwoVersions).
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
	removed("obor", c.Data.Obor != nil)                                          // v0.17.0, T-597: one kit per machine
	removed("cache.warn_tokens", c.Data.Cache.WarnTokens != nil)                 // v0.12.0, T-463
	removed("heartbeat", c.Data.Heartbeat != nil)                                // v0.12.0, T-465: the heartbeat, its nudges and obor notify went
	removed("stop_hook", c.Data.StopHook != nil)                                 // v0.12.0, T-465
	removed("notify", c.Data.Notify != nil)                                      // v0.12.0, T-465
	removed("inbox", c.Data.Inbox != nil)                                        // v0.12.0, T-466: obor tell, the inbox and the obor mod went
	removed("merge.full_every", c.Data.Merge.FullEvery != nil)                   // v0.12.0, T-465: its one reader was the heartbeat's full-check due
	if on, ok := c.Data.MCP.Jobs.(bool); c.Data.MCP.Jobs != nil && !(ok && on) { // true is how obor runs: no warning
		out = append(out, "WARN mcp.jobs "+Compact(c.Data.MCP.Jobs)+" is obsolete: every agent fetches its jobs "+
			"(T-437), remove it")
	}
	for _, name := range c.ModelNames() { // T-705: opencode's own default model may refuse requests, and effort needs one (T-703)
		m, _ := c.Model(name)
		if kd, known := agents.Lookup(m.Kind); known && kd.Caps().ModelArg && m.ID == "" && c.modelUsed(name) {
			out = append(out, "WARN models."+name+": opencode model without a model id (opencode's own default may not "+
				"work, and an effort needs one): set its \"model\", e.g. \"opencode-go/deepseek-v4.1-flash\"")
		}
	}
	removed("roles.leads", c.Data.Roles.Leads != nil)              // v0.12.0, T-467: one lead per project
	removed("done_file", c.Data.DoneFile != nil)                   // v0.12.0, T-470: task_done replaced the done file
	removed("spec.files_heading", c.Data.Spec.FilesHeading != nil) // v0.13.0, T-496: the per-file guard went
	removed("spec.enforce_files", c.Data.Spec.EnforceFiles != nil) // v0.13.0, T-496
	removed("paths.status", c.Data.Paths["status"] != "")          // v0.12.0, T-470: no STATUS file
	removed("paths.queue", c.Data.Paths["queue"] != "")            // v0.12.0, T-470: the queue went (obor dispatch --after)
	if c.Data.Paths["tasks"] != "" {
		out = append(out, "WARN paths.tasks: no longer written; keep a custom path while its unfinished tasks have specs there (docs/tasks needs no key), then remove it")
	}
	leads, _ := c.Data.Leads.(map[string]any)
	removed("leads.cross_ttl", leads["cross_ttl"] != nil) // v0.12.0, T-470: leads.cross_ttl had no reader
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
	for _, role := range roles.All() {
		if ref := c.Data.Roles.entry(role.Name); ref != nil {
			refs = append(refs, *ref)
		}
	}
	var out []string
	for _, ref := range refs {
		out = append(out, func() string { name, _ := ParseModelRef(ref); return name }())
	}
	return Unique(out...)
}
