package view

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app"
	"obor/internal/app/event"
	"obor/internal/app/port"
	"obor/internal/config"
)

// Setup is the project's setup as the web page shows it (T-750, T-765): what steers the work — each difficulty
// level's model refs with what runs them, the lanes, the role agents and the pause — and how the dispatches actually
// spread, by difficulty level and chosen model. It is its own read model, outside Project: obor status needs none of
// it.
type Setup struct {
	Difficulty   []SetupLevel    `json:"difficulty"`   // hard, medium, simple
	Types        []SetupType     `json:"types"`        // every model ref a level names, then the roles' not among them
	Models       []SetupModel    `json:"models"`       // every model the config knows: built-in, then models' own
	Kinds        []SetupKind     `json:"kinds"`        // every agent kind: what the config adds to its starts
	Lanes        []SetupLane     `json:"lanes"`        // every configured lane
	Roles        []SetupRole     `json:"roles"`        // every role the config names (the worker is the lanes)
	Paused       *Pause          `json:"paused"`       // the pause in force (nil: none)
	Distribution []DispatchCount `json:"distribution"` // one cell per level and chosen model
	// ConfigRev is the revision of .obor/config.json this setup was read from (config.Rev, T-760; "": no file): the
	// page's edits name it, and serve refuses them once the file is another one.
	ConfigRev string `json:"config_rev"`
}

// SetupLevel is one difficulty level and its model refs in dispatch order, as configured: a ref keeps its effort
// ("opus:high" stays as written).
type SetupLevel struct {
	Level string   `json:"level"`
	Types []string `json:"types"`
}

// SetupType is one model ref the setup names (Ref, its :<effort> as written) and what runs it: the model's name, its
// kind, the model id the kind starts ("": the kind's own choice) and the ref's effort ("": the kind's default).
type SetupType struct {
	Ref    string `json:"ref"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

// SetupModel is one model the config knows (T-765): its name, kind and model id, whether it is a built-in one
// (models.<name> may override it), and the efforts its kind takes.
type SetupModel struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Model   string   `json:"model"`
	Builtin bool     `json:"builtin"`
	Efforts []string `json:"efforts"`
	// Paused is the model's pause in force (obor pause --model, T-766; nil: none): dispatch skips the model.
	Paused *Pause `json:"paused"`
}

// SetupKind is one agent kind (T-766): the args kinds.<kind>.args appends to its every start and whether
// kinds.<kind>.prompt_extra adds a line to its work jobs.
type SetupKind struct {
	Name        string   `json:"name"`
	Args        []string `json:"args"`
	PromptExtra bool     `json:"prompt_extra"`
}

// SetupLane is one configured lane: its name, the model it runs now (its dispatch's; "": none), its agent and its
// state now (free, working, done, missing …), and whether it holds a task serve would refuse to drop (Locked: the
// config-set rule, T-896). Locked is computed here from serve's Free lanes, so an older serve, which names none,
// locks every lane (T-921).
type SetupLane struct {
	Lane   string `json:"lane"`
	Type   string `json:"type"`
	Agent  string `json:"agent"`
	State  string `json:"state"`
	Locked bool   `json:"locked,omitempty"`
}

// SetupRole is one role agent the config names: the role, its agent, the model ref it starts with ("" when the config
// names none) and its status as serve sees it ("" when no pane runs it).
type SetupRole struct {
	Role   string `json:"role"`
	Agent  string `json:"agent"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// DispatchCount is the dispatches that placed a task on a lane, by the difficulty level they chose by and the type
// reference they placed: Recent are those of the last 7 days, All those the log still holds (the page's "kept": serve's
// nightly prune deletes an ended task's records 30 days after its end, T-1089). Level is OverrideLevel for a
// dispatch with --model and no level ("" is neither), Type "" a dispatch the store names no model for: a record from
// before T-721 (the page labels it "unknown"; T-752, T-865).
type DispatchCount struct {
	Level  string `json:"level"`
	Type   string `json:"type"`
	Recent int    `json:"recent"`
	All    int    `json:"all"`
}

// OverrideLevel is the distribution row of a dispatch that chose its models (--model) itself, with no level: the
// spec's difficulty said nothing of it.
const OverrideLevel = "override"

// distributionWindow is how far back the distribution's Recent counts: the last 7 days.
const distributionWindow = 7 * 24 * time.Hour

// distributionLevels is the distribution's row order: the levels a spec picks, then --model's override, then a plain
// dispatch with neither (a level unknown to this binary sorts after them, so it still shows).
var distributionLevels = []string{"hard", "medium", "simple", OverrideLevel, ""}

// LoadSetup is the setup page's read, narrow (T-880): one config read, serve's answer, the lane table (git: a lane's
// state) and the store's dispatch records. It runs no ps and builds none of the overview's other facts (the queue, the
// events, the full check, each task). An error is the config's, as Load's.
func LoadSetup(ctx context.Context, root string) (Setup, error) {
	return reading{}.setup(ctx, root)
}

func (r reading) setup(ctx context.Context, root string) (Setup, error) {
	// the revision first: a write between the two reads leaves it older than what is shown, so an edit is refused
	// rather than made on a page that hides a change
	rev := configRev(root)
	in := r.open(ctx, root)
	defer in.close()
	if in.err != nil {
		return Setup{}, in.err
	}
	v := Project{}
	ps := serveOf(&v, in.ss, in.serr, in.p, nil)
	return setup(in.cfg, in.p.States(), v, in.db, rev, ps.Free, time.Now()), nil
}

// configRev is .obor/config.json's revision under root (config.Rev; "": no file): the page's edits name it, and serve
// refuses them once the file is another one (T-760).
func configRev(root string) string {
	doc, err := os.ReadFile(filepath.Join(root, ".obor", "config.json"))
	if err != nil {
		return ""
	}
	return config.Rev(doc)
}

// setup is Setup for one config snapshot (cfg), the lane table's rows (lanes), the view's role rows and pauses (v), the
// store (db), the config.json revision the page read (rev) and serve's lanes that hold no task (free: configset's
// rule, T-896; none from an older serve: every lane counts as locked, T-921).
func setup(cfg *config.Config, lanes []port.Lane, v Project, db *sql.DB, rev string, free []string, now time.Time) Setup {
	s := Setup{Difficulty: []SetupLevel{}, Types: []SetupType{}, Models: []SetupModel{}, Kinds: []SetupKind{}, Lanes: []SetupLane{},
		Roles: []SetupRole{}, Distribution: []DispatchCount{}, ConfigRev: rev}
	for _, level := range []string{"hard", "medium", "simple"} {
		s.Difficulty = append(s.Difficulty, SetupLevel{Level: level, Types: append([]string{}, cfg.DifficultyList(level)...)})
	}
	states := map[string]port.Lane{}
	for _, l := range lanes {
		states[l.Name] = l
	}
	for _, l := range cfg.Lanes() { // a working lane's view type is its dispatch's model (T-721)
		s.Lanes = append(s.Lanes, SetupLane{Lane: l.Name, Type: states[l.Name].Type, Agent: cfg.AgentName("worker", l.Name),
			State: states[l.Name].State, Locked: !slices.Contains(free, l.Name)})
	}
	status := map[string]string{}
	for _, r := range v.Roles {
		status[r.Role] = r.Status
	}
	if v.Lead != nil {
		status["lead"] = v.Lead.Status
	}
	for _, rc := range cfg.RoleAgents() { // the worker's rows are the lanes; a custom role is listed like any other
		s.Roles = append(s.Roles, SetupRole{Role: rc.Role.Name, Agent: rc.Agent,
			Type: config.ModelRef(rc.Type, rc.Effort), Status: status[rc.Role.Name]})
	}
	s.Types = setupTypes(cfg, s.Difficulty, s.Roles)
	for _, name := range cfg.ModelNames() {
		m, _ := cfg.Model(name)
		efforts := []string{}
		if k, ok := agents.Lookup(m.Kind); ok {
			efforts = append(efforts, k.Efforts()...)
		}
		_, builtin := config.BuiltinModelNamed(name)
		s.Models = append(s.Models, SetupModel{Name: name, Kind: m.Kind, Model: m.ID, Builtin: builtin, Efforts: efforts,
			Paused: v.ModelPauses[name]})
	}
	for _, kind := range agents.Names() {
		k := cfg.Kind(kind)
		s.Kinds = append(s.Kinds, SetupKind{Name: kind, Args: append([]string{}, k.Args...), PromptExtra: k.PromptExtra != ""})
	}
	s.Paused = v.Paused
	s.Distribution = distribution(db, now)
	return s
}

// setupTypes is the setup's model list: one entry per model ref the levels name, in level order (a ref two levels
// name is one entry), then the roles' refs no level names. A ref naming no model of the config is skipped (obor config
// validate names it).
func setupTypes(cfg *config.Config, levels []SetupLevel, roles []SetupRole) []SetupType {
	out := []SetupType{}
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		if t, ok := setupType(cfg, ref); ok {
			out = append(out, t)
		}
	}
	for _, level := range levels {
		for _, ref := range level.Types {
			add(ref)
		}
	}
	for _, role := range roles {
		add(role.Type)
	}
	return out
}

// setupType is ref's entry as what runs it: the model it names, its kind, its model id and the ref's effort; ok false
// when the config has no such model.
func setupType(cfg *config.Config, ref string) (SetupType, bool) {
	name, effort := config.ParseModelRef(ref)
	m, ok := cfg.Model(name)
	if !ok {
		return SetupType{}, false
	}
	return SetupType{Ref: ref, Name: name, Kind: m.Kind, Model: m.ID, Effort: effort}, true
}

// distribution is db's dispatch records counted by level and chosen model, for the last 7 days and for every record
// the log still holds (T-1089: an ended task's go 30 days after its end).
// Only a dispatch that placed a task on a lane counts: a waiting dispatch places nothing, and the dispatch that
// places it later counts once; a redo is a dispatch of its own. A record with no type (a dispatch from before T-721)
// is unknown (T-752, T-865: a lane row or a later record of a lane gone from the config names nothing for it).
func distribution(db *sql.DB, now time.Time) []DispatchCount {
	if db == nil {
		return []DispatchCount{}
	}
	recs, err := event.Facts(db, event.StepDispatched)
	if err != nil {
		return []DispatchCount{}
	}
	type cell struct{ level, typ string }
	recent, all := map[cell]int{}, map[cell]int{}
	cutoff := now.Add(-distributionWindow)
	for _, r := range recs {
		d := app.Describe(r)
		if d.Lane == "" { // not a dispatch that placed a task on a lane (or unreadable)
			continue
		}
		level := d.Level
		if level == "" && len(d.Types) > 0 {
			level = OverrideLevel
		}
		c := cell{level, d.Type}
		all[c]++
		if at := event.RecordTime(r); !at.IsZero() && !at.Before(cutoff) {
			recent[c]++
		}
	}
	out := make([]DispatchCount, 0, len(all))
	for c, n := range all {
		out = append(out, DispatchCount{Level: c.level, Type: c.typ, Recent: recent[c], All: n})
	}
	slices.SortFunc(out, func(a, b DispatchCount) int {
		if r := levelRank(a.Level) - levelRank(b.Level); r != 0 {
			return r
		}
		return strings.Compare(a.Type, b.Type)
	})
	return out
}

// levelRank is level's row in the distribution: hard, medium, simple, override, then a plain dispatch; a level this
// binary does not know sorts after them all.
func levelRank(level string) int {
	if i := slices.Index(distributionLevels, level); i >= 0 {
		return i
	}
	return len(distributionLevels)
}
