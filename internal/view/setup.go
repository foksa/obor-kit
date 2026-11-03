package view

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/app/event"
	"obor/internal/config"
	roletable "obor/internal/roles"
	"obor/internal/store"
)

// Setup is the project's setup as the web page shows it (T-750, T-765): what steers the work — each difficulty
// level's model refs with what runs them, the lanes, the role agents and the pause — and how the dispatches actually
// spread, by difficulty level and chosen model. It is its own read model, outside Project: obor status needs none of
// it.
type Setup struct {
	Difficulty   []SetupLevel    `json:"difficulty"`   // hard, medium, simple
	Types        []SetupType     `json:"types"`        // every model ref a level names, then the roles' not among them
	Models       []SetupModel    `json:"models"`       // every model the config knows: built-in, then models' own
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
}

// SetupLane is one configured lane: its name, the model it runs now (its dispatch's; "": none), its agent and its
// state now (free, working, done, missing …).
type SetupLane struct {
	Lane  string `json:"lane"`
	Type  string `json:"type"`
	Agent string `json:"agent"`
	State string `json:"state"`
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
// reference they placed: Recent are those of the last 7 days, All those of every record. Level is OverrideLevel for a
// dispatch with --model and no level ("" is neither), Type "" a dispatch the store names no model for: a record from
// before T-721 on a lane the config has, or one on a lane gone from it that the store's own records leave unnamed
// (T-752; the page labels it "unknown").
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

// Setup is the project's setup read model: its config's difficulty levels and their models, lanes and roles, the
// view's lane states, role statuses and pause, and the store's dispatch records (the config is read as obor web reads
// it: LoadReader, an unknown key warns, it does not fail).
func (d Detail) Setup() (Setup, error) {
	// the revision first: a write between the two reads leaves it older than what is shown, so an edit is refused
	// rather than made on a page that hides a change
	rev := ""
	if doc, err := os.ReadFile(filepath.Join(d.Root, ".obor", "config.json")); err == nil {
		rev = config.Rev(doc)
	}
	cfg, _, err := config.LoadReader(d.Root)
	if err != nil {
		return Setup{}, err
	}
	s := Setup{Difficulty: []SetupLevel{}, Types: []SetupType{}, Models: []SetupModel{}, Lanes: []SetupLane{},
		Roles: []SetupRole{}, Distribution: []DispatchCount{}, ConfigRev: rev}
	for _, level := range []string{"hard", "medium", "simple"} {
		s.Difficulty = append(s.Difficulty, SetupLevel{Level: level, Types: append([]string{}, cfg.DifficultyList(level)...)})
	}
	states := map[string]Lane{}
	for _, l := range d.Project.Lanes {
		states[l.Lane] = l
	}
	for _, l := range cfg.Lanes() { // a working lane's view type is its dispatch's model (T-721)
		s.Lanes = append(s.Lanes, SetupLane{Lane: l.Name, Type: states[l.Name].Type, Agent: cfg.AgentName("worker", l.Name),
			State: states[l.Name].State})
	}
	status := map[string]string{}
	for _, r := range d.Project.Roles {
		status[r.Role] = r.Status
	}
	if d.Project.Lead != nil {
		status["lead"] = d.Project.Lead.Status
	}
	for _, role := range roletable.All() {
		if role.Name == "worker" { // the worker's rows are the lanes
			continue
		}
		rc, _ := cfg.Role(role.Name)
		if role.Optional && !rc.Configured {
			continue
		}
		s.Roles = append(s.Roles, SetupRole{Role: role.Name, Agent: cfg.AgentName(role.Name, ""),
			Type: config.ModelRef(rc.Type, rc.Effort), Status: status[role.Name]})
	}
	s.Types = setupTypes(cfg, s.Difficulty, s.Roles)
	for _, name := range cfg.ModelNames() {
		m, _ := cfg.Model(name)
		efforts := []string{}
		if k, ok := agents.Lookup(m.Kind); ok {
			efforts = append(efforts, k.Efforts()...)
		}
		_, builtin := config.BuiltinModelNamed(name)
		s.Models = append(s.Models, SetupModel{Name: name, Kind: m.Kind, Model: m.ID, Builtin: builtin, Efforts: efforts})
	}
	s.Paused = d.Project.Paused
	db, close := d.open()
	defer close()
	s.Distribution = distribution(db, cfg, time.Now())
	return s, nil
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

// distribution is db's dispatch records counted by level and chosen model, for the last 7 days and for every record.
// Only a dispatch that placed a task on a lane counts: a waiting dispatch places nothing, and the dispatch that
// places it later counts once; a redo is a dispatch of its own. A record with no type (a dispatch from before T-721)
// on a lane the config no longer names takes the type the store's own records of that lane name (laneTypes); on a
// configured lane (every lane is pooled, T-765), or with none named, the type is unknown (T-752).
func distribution(db *sql.DB, cfg *config.Config, now time.Time) []DispatchCount {
	if db == nil {
		return []DispatchCount{}
	}
	lanes := map[string]string{}
	for _, l := range cfg.Lanes() {
		lanes[l.Name] = ""
	}
	gone := laneTypes(db, lanes)
	rows, err := db.Query(`SELECT ts, data FROM events WHERE kind = ? AND step = ?`, event.KindFact, event.StepDispatched)
	if err != nil {
		return []DispatchCount{}
	}
	defer rows.Close()
	type cell struct{ level, typ string }
	recent, all := map[cell]int{}, map[cell]int{}
	cutoff := now.Add(-distributionWindow)
	for rows.Next() {
		var ts string
		var data []byte
		if rows.Scan(&ts, &data) != nil {
			continue
		}
		var d struct {
			Lane  string   `json:"lane"`
			Level string   `json:"level"`
			Types []string `json:"types"`
			Type  string   `json:"type"`
		}
		if json.Unmarshal(data, &d) != nil || d.Lane == "" { // not a dispatch that placed a task on a lane
			continue
		}
		level, typ := d.Level, d.Type
		if level == "" && len(d.Types) > 0 {
			level = OverrideLevel
		}
		if typ == "" {
			if _, ok := lanes[d.Lane]; !ok { // its lane is gone from the config: the type its own records name, else unknown ("")
				typ = gone[d.Lane]
			}
		}
		c := cell{level, typ}
		all[c]++
		if at, err := time.Parse(time.RFC3339, ts); err == nil && !at.Before(cutoff) {
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

// laneTypes is the type each lane the config no longer names ran, from the store's own records of that lane (T-752):
// its lane row, which the lane's setup wrote (a row from before T-721 records none), and its dispatch records that
// named one (after T-721). A lane none of them names a type for is missing from the map: its dispatches are unknown,
// never guessed from the lane's name.
func laneTypes(db *sql.DB, configured map[string]string) map[string]string {
	out := map[string]string{}
	if rows, err := store.ReadLanes(db); err == nil { // a store without a lanes table: the dispatch records answer
		for _, r := range rows {
			if _, ok := configured[r.Lane]; !ok && r.Type != "" {
				out[r.Lane] = r.Type
			}
		}
	}
	rows, err := db.Query(`SELECT data FROM events WHERE kind = ? AND step = ?`, event.KindFact, event.StepDispatched)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			continue
		}
		var d struct {
			Lane string `json:"lane"`
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &d) != nil || d.Type == "" {
			continue
		}
		if _, ok := configured[d.Lane]; !ok {
			out[d.Lane] = d.Type // the lane's newest record answers, as its row does
		}
	}
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
