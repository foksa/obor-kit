package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"obor/internal/roles"
)

// The migration (T-765, obor setup migrate): a config of the shape before T-765 — lane_types, lanes as an object of
// typed lanes, roles.<role> objects — rewritten once to model refs and a lanes count. The rules:
//
//	refs      every lane type a difficulty level, a role or an agent names becomes a model ref: its kind and model id
//	          name a built-in model (opus_high = claude --model opus --effort high → opus:high), else the type keeps
//	          its name and goes to models (kind, model); the effort its args set follows the name (a ref's own
//	          :<effort> wins). A type nothing names is dropped.
//	kinds     the args every named type of a kind has beyond the kind's base args, its model and its effort go to
//	          kinds.<kind>.args; the others are dropped (listed). prompt_extra: the most common one of the kind's named
//	          types (the others listed as dropped).
//	dropped   max (no caps), settings, roles.<role>.agent and .worktree, a lane's type and agent, the agents key (T-777:
//	          an entry of advisor, researcher or triage moves its type to that role; the rest are dropped): each listed.
//	moved     roles.lead.handoff_tokens → handoff_tokens, roles.integrator.handoff_tokens → integrator_handoff_tokens.
//	lanes     the count of lanes; Lanes is their old names in config order (the caller renames them to L-01 … L-NN).
//	agents    a role's or lane's agent that is not the new default (<prefix>-<role>, <prefix>-l-NN) is renamed to it,
//	          in guards.allow_push too; Agents lists each old → new (the caller relabels their panes).

// Migration is Migrate's result: the new config.json and local.json (nil: there was none), the old lane names in
// config order, the agents renamed, and the notes (what was dropped or changed, one line each).
type Migration struct {
	Config, Local []byte
	Lanes         []string
	Agents        []AgentRename
	Notes         []string
}

// AgentRename is an agent's name before and after the migration.
type AgentRename struct{ Old, New string }

// oldLaneType is lane_types.<name> as the shape before T-765 had it.
type oldLaneType struct {
	Kind        string         `json:"kind"`
	Args        []string       `json:"args"`
	Max         int            `json:"max"`
	PromptExtra string         `json:"prompt_extra"`
	Settings    map[string]any `json:"settings"`
}

// migrated is one named lane type after the migration: the model it is, the effort its args set, the args beyond the
// base, model and effort (as units: a flag with its value), its kind and prompt_extra.
type migrated struct {
	name, model, effort, kind, promptExtra string
	extra                                  [][]string
}

// Migrate rewrites config (config.json's bytes) and local (local.json's; nil: none) from the shape before T-765, or a
// config that still sets the removed agents key (T-777). The lane types are read merged (local over config), so a ref
// in either file resolves; kinds and models go to config.json. An error: a file is no JSON object, or neither has
// anything to migrate.
func Migrate(config, local []byte) (Migration, error) {
	if _, ok := object(config); !ok {
		return Migration{}, fmt.Errorf("config.json is not a JSON object")
	}
	if local != nil {
		if _, ok := object(local); !ok {
			return Migration{}, fmt.Errorf("local.json is not a JSON object")
		}
	}
	if OldShape(config) == "" && (local == nil || OldShape(local) == "") &&
		!HasAgents(config) && !HasAgents(local) {
		return Migration{}, fmt.Errorf("the config has the new shape already")
	}
	merged := config
	if local != nil {
		merged = merge(config, local)
	}
	configured := configuredRoles(merged)
	var m Migration
	types, err := oldTypes(merged)
	if err != nil {
		return Migration{}, err
	}
	named := namedTypes(merged, types)
	table := map[string]*migrated{}
	var models Ordered[ModelEntry]
	for _, name := range types.Keys {
		if !slices.Contains(named, name) {
			m.note("lane_types.%s: no level, role or agent names it: dropped", name)
			continue
		}
		lt := types.M[name]
		mt := migrateType(name, lt)
		table[name] = mt
		if b, ok := BuiltinModelNamed(mt.model); !ok || b.Kind != mt.kind || b.ID != modelID(lt) {
			if models.M == nil {
				models.M = map[string]ModelEntry{}
			}
			models.Keys = append(models.Keys, mt.model)
			models.M[mt.model] = ModelEntry{Kind: mt.kind, Model: modelID(lt)}
		}
		if lt.Max > 0 {
			m.note("lane_types.%s.max %d: dropped (no caps)", name, lt.Max)
		}
		if len(lt.Settings) > 0 {
			m.note("lane_types.%s.settings %s: dropped", name, Compact(lt.Settings))
		}
	}
	kinds := m.kindsOf(named, table)
	m.Lanes = mergedLanes(config, local)
	m.Agents = agentRenames(merged, m.Lanes)
	for i, doc := range [][]byte{config, local} {
		if doc == nil {
			continue
		}
		file := []string{"config.json", "local.json"}[i]
		out, lanes, err := m.rewrite(file, doc, table, configured)
		if err != nil {
			return Migration{}, fmt.Errorf("%s: %v", file, err)
		}
		if i == 0 {
			if len(kinds.Keys) > 0 {
				if out, err = EditJSON(out, []string{"kinds"}, json.RawMessage(Compact(kinds))); err != nil {
					return Migration{}, err
				}
			}
			if len(models.Keys) > 0 {
				if out, err = EditJSON(out, []string{"models"}, json.RawMessage(Compact(models))); err != nil {
					return Migration{}, err
				}
			}
		} else if lanes != nil { // local.json's lanes merged key by key over config.json's: its count is the merged one
			if out, err = EditJSON(out, []string{"lanes"}, len(m.Lanes)); err != nil {
				return Migration{}, err
			}
		}
		if out, err = renamePush(out, m.Agents); err != nil {
			return Migration{}, fmt.Errorf("%s: %v", file, err)
		}
		var ind bytes.Buffer
		if err := json.Indent(&ind, out, "", "  "); err != nil {
			return Migration{}, err
		}
		out = append(ind.Bytes(), '\n')
		if i == 0 {
			m.Config = out
		} else {
			m.Local = out
		}
	}
	return m, nil
}

func (m *Migration) note(format string, a ...any) {
	m.Notes = append(m.Notes, fmt.Sprintf(format, a...))
}

// oldTypes is merged's lane_types in config order.
func oldTypes(merged []byte) (Ordered[oldLaneType], error) {
	var types Ordered[oldLaneType]
	raw, err := MemberAt(merged, []string{"lane_types"})
	if err != nil || raw == nil {
		return Ordered[oldLaneType]{M: map[string]oldLaneType{}}, err
	}
	var order Ordered[json.RawMessage]
	if err := json.Unmarshal(raw, &order); err != nil {
		return types, fmt.Errorf("lane_types: %v", err)
	}
	types.M = map[string]oldLaneType{}
	for _, k := range order.Keys {
		var lt oldLaneType
		if err := json.Unmarshal(order.M[k], &lt); err != nil {
			return types, fmt.Errorf("lane_types.%s: %v", k, err)
		}
		types.Keys = append(types.Keys, k)
		types.M[k] = lt
	}
	return types, nil
}

// namedTypes are the lane types of types that a difficulty level, a role (roles.<role>.type) or an agent
// (agents.<name>.type) of merged names, in lane_types order.
func namedTypes(merged []byte, types Ordered[oldLaneType]) []string {
	var refs []string
	var levels map[string][]string
	if raw, _ := MemberAt(merged, []string{"difficulty"}); raw != nil {
		_ = json.Unmarshal(raw, &levels)
	}
	for _, list := range levels {
		refs = append(refs, list...)
	}
	for _, key := range []string{"roles", "agents"} {
		raw, _ := MemberAt(merged, []string{key})
		ms, _ := object(raw)
		for _, e := range ms {
			if s, ok := stringMember(e.Val, "type"); ok {
				refs = append(refs, s)
			}
		}
	}
	var out []string
	for _, k := range types.Keys {
		if slices.ContainsFunc(refs, func(ref string) bool { name, _ := ParseModelRef(ref); return name == k }) {
			out = append(out, k)
		}
	}
	return out
}

// stringMember is the string member key of the object doc; ok false: none, or doc is no object.
func stringMember(doc json.RawMessage, key string) (string, bool) {
	raw, err := MemberAt(doc, []string{key})
	if err != nil || raw == nil {
		return "", false
	}
	var s string
	return s, json.Unmarshal(raw, &s) == nil
}

// migrateType is lane type name as a model: a built-in one when its kind and model id are a built-in's (the type's
// own name first), else its own name (a models entry).
func migrateType(name string, lt oldLaneType) *migrated {
	id := modelID(lt)
	mt := &migrated{name: name, model: name, kind: lt.Kind, effort: argsEffort(lt.Args), promptExtra: lt.PromptExtra}
	if b, ok := BuiltinModelNamed(name); !ok || b.Kind != lt.Kind || b.ID != id {
		for _, b := range BuiltinModels {
			if id != "" && b.Kind == lt.Kind && b.ID == id {
				mt.model = b.Name
				break
			}
		}
	}
	base := units(KindBaseArgs[lt.Kind])
	for _, u := range units(lt.Args) {
		if !isModelUnit(u) && !isEffortUnit(u) && !slices.ContainsFunc(base, func(b []string) bool { return slices.Equal(b, u) }) {
			mt.extra = append(mt.extra, u)
		}
	}
	return mt
}

// kindsOf is the kinds entries of the named types (table): per kind the args units every one of them has (in the
// first one's order), and the most common prompt_extra; what a type loses is noted.
func (m *Migration) kindsOf(named []string, table map[string]*migrated) Ordered[KindEntry] {
	var out Ordered[KindEntry]
	out.M = map[string]KindEntry{}
	for _, name := range named {
		kind := table[name].kind
		if _, done := out.M[kind]; done {
			continue
		}
		var of []*migrated
		for _, n := range named {
			if table[n].kind == kind {
				of = append(of, table[n])
			}
		}
		var shared [][]string
		for _, u := range of[0].extra {
			if !slices.ContainsFunc(of, func(t *migrated) bool { return !hasUnit(t.extra, u) }) {
				shared = append(shared, u)
			}
		}
		for _, t := range of {
			for _, u := range t.extra {
				if !hasUnit(shared, u) {
					m.note("lane_types.%s.args %s: dropped (not every %s type has it)", t.name, strings.Join(u, " "), kind)
				}
			}
		}
		count := map[string]int{}
		extra := ""
		for _, t := range of {
			if t.promptExtra != "" {
				count[t.promptExtra]++
				if count[t.promptExtra] > count[extra] {
					extra = t.promptExtra
				}
			}
		}
		for _, t := range of {
			if t.promptExtra != "" && t.promptExtra != extra {
				m.note("lane_types.%s.prompt_extra dropped (kinds.%s.prompt_extra is another type's): %s", t.name, kind,
					Compact(t.promptExtra))
			}
		}
		var args []string
		for _, u := range shared {
			args = append(args, u...)
		}
		if len(args) > 0 || extra != "" {
			out.Keys = append(out.Keys, kind)
			out.M[kind] = KindEntry{Args: args, PromptExtra: extra}
		}
	}
	return out
}

// HasAgents reports whether doc sets the removed agents key (T-777): the one thing obor setup migrate still rewrites
// in an otherwise new-shape config.
func HasAgents(doc []byte) bool {
	raw, _ := MemberAt(doc, []string{"agents"})
	return raw != nil
}

// rewrite is doc (one config file) in the new shape: lane_types and agents gone, refs rewritten (table), roles as
// refs, the handoff thresholds moved, lanes as a count (lanes: the old names in order; nil: doc sets no lanes).
func (m *Migration) rewrite(file string, doc []byte, table map[string]*migrated, configured map[string]bool) ([]byte, []string, error) {
	out, err := EditJSON(doc, []string{"lane_types"}, nil)
	if err != nil {
		return nil, nil, err
	}
	ref := func(old string) string {
		name, effort := ParseModelRef(old)
		mt, ok := table[name]
		if !ok {
			return old
		}
		if effort == "" {
			effort = mt.effort
		}
		return ModelRef(mt.model, effort)
	}
	if raw, _ := MemberAt(out, []string{"difficulty"}); raw != nil {
		var levels Ordered[[]string]
		if err := json.Unmarshal(raw, &levels); err != nil {
			return nil, nil, fmt.Errorf("difficulty: %v", err)
		}
		for _, k := range levels.Keys {
			var refs []string
			for _, r := range levels.M[k] {
				if !slices.Contains(refs, ref(r)) {
					refs = append(refs, ref(r))
				}
			}
			levels.M[k] = refs
		}
		if out, err = EditJSON(out, []string{"difficulty"}, json.RawMessage(Compact(levels))); err != nil {
			return nil, nil, err
		}
	}
	if raw, _ := MemberAt(out, []string{"agents"}); raw != nil {
		if out, err = m.rewriteAgents(file, out, ref, configured); err != nil {
			return nil, nil, err
		}
	}
	if out, err = m.rewriteRoles(file, out, ref); err != nil {
		return nil, nil, err
	}
	raw, _ := MemberAt(out, []string{"lanes"})
	lanes, isObject := object(raw)
	if !isObject {
		return out, nil, nil
	}
	var names []string
	for _, l := range lanes {
		names = append(names, l.Key)
		var typ, agent string
		if json.Unmarshal(l.Val, &typ) != nil {
			typ, _ = stringMember(l.Val, "type")
			agent, _ = stringMember(l.Val, "agent")
		}
		if typ != "" {
			m.note("%s lanes.%s: type %s dropped (every lane runs the model its dispatch picks)", file, l.Key, typ)
		}
		if agent != "" {
			m.note("%s lanes.%s.agent %s dropped (lane agents are <prefix>-l-NN)", file, l.Key, agent)
		}
	}
	out, err = EditJSON(out, []string{"lanes"}, len(names))
	return out, names, err
}

// agentRoles are the roles an old agents entry belongs to (T-777): advisor, researcher and triage, the agent kinds the
// owner made roles. An entry keyed by the role, or whose role field names it, moves to that role; every other entry is
// dropped.
var agentRoles = []string{"advisor", "researcher", "triage"}

// rewriteAgents is doc without the agents key (T-777): an entry of a role above moves its type to roles.<role> (when
// the role is not configured already, in either config file), every other entry is dropped with a note.
func (m *Migration) rewriteAgents(file string, doc []byte, ref func(string) string, configured map[string]bool) ([]byte, error) {
	raw, _ := MemberAt(doc, []string{"agents"})
	ms, _ := object(raw)
	out, err := EditJSON(doc, []string{"agents"}, nil)
	if err != nil {
		return nil, err
	}
	for _, e := range ms {
		role := agentRole(e)
		typ, _ := stringMember(e.Val, "type")
		switch {
		case role == "":
			m.note("%s agents.%s: dropped (advisor, researcher and triage are roles: roles.advisor, "+
				"roles.researcher, roles.triage)", file, e.Key)
		case configured[role]:
			m.note("%s agents.%s: dropped (roles.%s is configured)", file, e.Key, role)
		default:
			configured[role] = true
			if out, err = EditJSON(out, []string{"roles", role}, ref(typ)); err != nil {
				return nil, err
			}
			m.note("%s agents.%s: moved to roles.%s", file, e.Key, role)
			if cwd, ok := stringMember(e.Val, "cwd"); ok && cwd != "" {
				m.note("%s agents.%s.cwd %s: dropped", file, e.Key, cwd)
			}
		}
	}
	return out, nil
}

// configuredRoles is every roles key of doc (the merged config), whether it holds a ref or an old role object.
func configuredRoles(doc []byte) map[string]bool {
	raw, _ := MemberAt(doc, []string{"roles"})
	ms, _ := object(raw)
	out := map[string]bool{}
	for _, r := range ms {
		out[r.Key] = true
	}
	return out
}

// agentRole is the role an old agents entry e belongs to ("" none): its key when that names a role, else its role
// field when that does.
func agentRole(e member) string {
	if r, ok := stringMember(e.Val, "role"); ok && slices.Contains(agentRoles, r) {
		return r
	}
	if slices.Contains(agentRoles, e.Key) {
		return e.Key
	}
	return ""
}

// rewriteRoles is doc with each roles.<role> object as its model ref (ref of its type; none: ""), its agent and
// worktree noted as dropped and its handoff_tokens moved to the top level.
func (m *Migration) rewriteRoles(file string, doc []byte, ref func(string) string) ([]byte, error) {
	raw, _ := MemberAt(doc, []string{"roles"})
	ms, _ := object(raw)
	out := doc
	var err error
	for _, e := range ms {
		if v := bytes.TrimSpace(e.Val); len(v) == 0 || v[0] != '{' {
			continue
		}
		typ, _ := stringMember(e.Val, "type")
		if wt, ok := stringMember(e.Val, "worktree"); ok && wt != "" {
			m.note("%s roles.%s.worktree %s dropped (roles run in the main checkout)", file, e.Key, wt)
		}
		if n, _ := MemberAt(e.Val, []string{"handoff_tokens"}); n != nil {
			to := map[string]string{"lead": "handoff_tokens", "integrator": "integrator_handoff_tokens"}[e.Key]
			if to == "" {
				m.note("%s roles.%s.handoff_tokens dropped (only the lead's and the integrator's are read)", file, e.Key)
			} else if out, err = EditJSON(out, []string{to}, n); err != nil {
				return nil, err
			}
		}
		if out, err = EditJSON(out, []string{"roles", e.Key}, ref(typ)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// agentRenames are the agents of merged's roles and lanes (lanes: their names in order) whose name is not the new
// default: a role's configured agent → <prefix>-<role>, a lane's agent (configured, else <prefix>-<lane lower>) →
// <prefix>-l-NN.
func agentRenames(merged []byte, lanes []string) []AgentRename {
	prefix, _ := stringMember(merged, "prefix")
	var out []AgentRename
	add := func(old, new string) {
		if old != "" && new != "" && old != new {
			out = append(out, AgentRename{old, new})
		}
	}
	raw, _ := MemberAt(merged, []string{"roles"})
	ms, _ := object(raw)
	for _, e := range ms {
		if role, ok := roles.Lookup(e.Key); ok {
			agent, _ := stringMember(e.Val, "agent")
			add(agent, role.AgentName(prefix, ""))
		}
	}
	worker, _ := roles.Lookup("worker")
	for i, lane := range lanes {
		old, _ := stringMember(json.RawMessage(mustMember(merged, "lanes", lane)), "agent")
		if old == "" {
			old = worker.AgentName(prefix, lane)
		}
		add(old, worker.AgentName(prefix, LaneName(i+1)))
	}
	return out
}

// mustMember is the member of doc at path, nil when none.
func mustMember(doc []byte, path ...string) json.RawMessage {
	raw, _ := MemberAt(doc, path)
	return raw
}

// renamePush is doc with each name of guards.allow_push that agents renames as its new name.
func renamePush(doc []byte, agents []AgentRename) ([]byte, error) {
	raw, _ := MemberAt(doc, []string{"guards", "allow_push"})
	var list []string
	if raw == nil || json.Unmarshal(raw, &list) != nil {
		return doc, nil
	}
	changed := false
	for i, a := range list {
		for _, r := range agents {
			if a == r.Old {
				list[i], changed = r.New, true
			}
		}
	}
	if !changed {
		return doc, nil
	}
	return EditJSON(doc, []string{"guards", "allow_push"}, list)
}

// mergedLanes is the lane names of config's lanes with local's merged over them (a key local adds comes last).
func mergedLanes(config, local []byte) []string {
	merged := config
	if local != nil {
		merged = merge(config, local)
	}
	raw, _ := MemberAt(merged, []string{"lanes"})
	ms, _ := object(raw)
	var out []string
	for _, l := range ms {
		out = append(out, l.Key)
	}
	return out
}

// modelID is the model id lane type lt's args name (--model M, -m M, --model=M, -m=M); "" for none.
func modelID(lt oldLaneType) string {
	for _, u := range units(lt.Args) {
		if isModelUnit(u) {
			if len(u) == 2 {
				return u[1]
			}
			_, v, _ := strings.Cut(u[0], "=")
			return v
		}
	}
	return ""
}

// argsEffort is the effort args set: Claude's --effort E, Codex's -c model_reasoning_effort=E; "" for none.
func argsEffort(args []string) string {
	for _, u := range units(args) {
		if isEffortUnit(u) {
			_, v, _ := strings.Cut(u[len(u)-1], "=")
			if len(u) == 2 && u[0] == "--effort" {
				v = u[1]
			}
			return v
		}
	}
	return ""
}

// units are args as flags with their values: a flag without "=" followed by a word that is no flag is one unit of
// two, anything else a unit of one.
func units(args []string) [][]string {
	var out [][]string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			out = append(out, []string{a, args[i+1]})
			i++
			continue
		}
		out = append(out, []string{a})
	}
	return out
}

func isModelUnit(u []string) bool {
	return len(u) == 2 && (u[0] == "--model" || u[0] == "-m") ||
		len(u) == 1 && (strings.HasPrefix(u[0], "--model=") || strings.HasPrefix(u[0], "-m="))
}

func isEffortUnit(u []string) bool {
	return len(u) == 2 && u[0] == "--effort" || len(u) == 1 && strings.HasPrefix(u[0], "--effort=") ||
		len(u) == 2 && (u[0] == "-c" || u[0] == "--config") && strings.HasPrefix(u[1], "model_reasoning_effort=")
}

func hasUnit(list [][]string, u []string) bool {
	return slices.ContainsFunc(list, func(x []string) bool { return slices.Equal(x, u) })
}
