package config

import (
	"fmt"
	"os/exec"
	"regexp"
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
	v.types()
	v.lanes()
	v.laneArgs()
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

// lanes: every lane type has a kind with a bridge (agents.Lookup: a leftover cursor or a typo is not one); no lane is
// named like a role worktree.
func (v *validator) lanes() {
	for _, k := range v.d.LaneTypes.Keys {
		kind := v.d.LaneTypes.M[k].Kind
		if kind == "" {
			v.fail(".lane_types"+key(k)+".kind", "required (claude, codex, …)")
			continue
		}
		if _, ok := agents.Lookup(kind); !ok {
			v.fail(".lane_types"+key(k)+".kind", Compact(kind)+" is not a supported kind: use one of "+
				strings.Join(agents.Names(), ", "))
		}
	}
	worktrees := v.c.RoleWorktrees()
	for _, k := range v.d.Lanes.Keys {
		for _, wt := range worktrees {
			if wt == k {
				v.fail(".lanes"+key(k), "name collides with role worktree "+Compact(wt))
				break
			}
		}
	}
}

// laneArgs: no lane type's args carry a bare `--`. obor adds the first prompt after its own options, so a `--` in the
// config would turn --settings and --resume into positionals (RV-47-verify-3 N2).
func (v *validator) laneArgs() {
	for _, k := range v.d.LaneTypes.Keys {
		for _, a := range v.d.LaneTypes.M[k].Args {
			if a == "--" {
				v.fail(".lane_types"+key(k)+".args", "`--` is not allowed: obor adds the first prompt itself")
				break
			}
		}
	}
}

// types: lanes and agents entries name a lane type (T-145); a role or lead that names one names a known one, with an
// effort its kind takes when it names one (`opus:high`, T-695).
func (v *validator) types() {
	check := func(path, t string, required bool) {
		if _, ok := v.d.LaneTypes.M[t]; !ok && (required || t != "") {
			v.fail(path, Compact(t)+" is not a key of lane_types")
		}
	}
	for _, k := range v.d.Lanes.Keys {
		check(".lanes"+key(k), v.d.Lanes.M[k].Type, true)
	}
	for _, k := range v.d.Agents.Keys {
		check(".agents"+key(k)+".type", v.d.Agents.M[k].Type, true)
	}
	for _, role := range roles.All() {
		if e := v.d.Roles.entry(role.Name); e != nil {
			v.typeRef(".roles."+role.Name+".type", e.Type)
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
	if r := v.d.Roles; r.Lead != nil {
		v.nonNegative(".roles.lead.handoff_tokens", r.Lead.HandoffTokens)
	}
	if r := v.d.Roles; r.Integrator != nil {
		v.nonNegative(".roles.integrator.handoff_tokens", r.Integrator.HandoffTokens)
	}
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
	roleAgents := Unique(append(names, v.d.Agents.Keys...)...)
	for _, lane := range v.c.Lanes() {
		state := ""
		if v.stateAgent != nil {
			state = v.stateAgent(lane.Name)
		}
		for _, alias := range v.c.LaneAgentAliases(lane.Name, state) {
			for _, agent := range roleAgents {
				if alias == agent {
					v.fail(".lanes"+key(lane.Name), "agent "+Compact(alias)+" collides with a role agent")
				}
			}
		}
		v.bareLaneName(lane.Name, state, roleAgents)
	}
}

// bareLaneName: a role agent or another lane's configured agent named like lane's lowercase name (o-02) is no name
// of lane (lookups take no bare lane name, T-302) but reads like one: WARN. The lane's own row or configured
// agent of that name is its real name.
func (v *validator) bareLaneName(lane, state string, roleAgents []string) {
	lower := strings.ToLower(lane)
	if state == lower || v.d.Lanes.M[lane].Agent == lower {
		return
	}
	agents := append([]string(nil), roleAgents...)
	for _, other := range v.d.Lanes.Keys {
		if other != lane {
			agents = append(agents, v.d.Lanes.M[other].Agent)
		}
	}
	for _, agent := range Unique(agents...) {
		if agent == lower {
			v.warns = append(v.warns, "WARN .lanes"+key(lane)+": agent "+Compact(agent)+
				" is named like the lane but is not its agent (lookups take no bare lane name)")
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
	for _, k := range c.Data.LaneTypes.Keys {
		if c.Data.LaneTypes.M[k].Jobs != nil { // v0.11.0, T-437
			out = append(out, "WARN lane_types."+k+".jobs is obsolete: every kind fetches its jobs (T-437), remove it")
		}
	}
	for _, k := range c.Data.LaneTypes.Keys { // T-705: opencode's own default model may refuse requests, and effort needs one (T-703)
		lt := c.Data.LaneTypes.M[k]
		if kd, known := agents.Lookup(lt.Kind); known && kd.Caps().ModelArg && lt.Model() == "" {
			out = append(out, "WARN lane_types."+k+": opencode type without --model (opencode's own default may not work, "+
				"and an effort needs one): add \"--model\", \"opencode-go/deepseek-v4.1-flash\" to its args")
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

// difficulty: each level of .difficulty is hard, medium or simple and names lane types the config has (each may name
// an effort, T-695) (the built-in
// default skips the types a project lacks; a project's own list is checked).
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
			v.typeRef(".difficulty"+key(k)+idx(i), t)
		}
	}
}

// typeRef: a type reference `<type>[:<effort>]` (T-695) at path names a key of lane_types and, with an effort, one
// its kind takes (config.CheckTypeRef); "" passes (a role without a type).
func (v *validator) typeRef(path, ref string) {
	if ref == "" {
		return
	}
	if typ, _ := ParseTypeRef(ref); v.d.LaneTypes.M[typ].Kind == "" {
		if _, ok := v.d.LaneTypes.M[typ]; !ok {
			v.fail(path, Compact(typ)+" is not a key of lane_types")
		}
		return // a type without a kind fails at .lane_types
	}
	if err := v.c.CheckTypeRef(ref); err != nil {
		v.fail(path, err.Error())
	}
}
