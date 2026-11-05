package project

import (
	"obor/internal/agents"
	"obor/internal/config"
	"obor/internal/store"
)

// LaneAgent is the agent lane's row names (the store's lanes table, serve's projection; "" when none or unreadable).
func LaneAgent(root, lane string) string {
	row, _, _ := store.ReadLaneAt(root, lane)
	return row.Agent
}

// AgentLookup is config.AgentLookup with the agents of the lanes' rows (read once).
func AgentLookup(cfg *config.Config, agent, at string) (config.AgentInfo, bool) {
	rows, _ := store.ReadLanesAt(cfg.Root)
	return cfg.AgentLookup(agent, at, func(lane string) string { return rows[lane].Agent })
}

// LaneModel is the model ref the lane's open dispatch runs (T-739, T-892): the lane row's Type, its effort kept, when
// the row holds a task (T-765: a lane is a pool, the task's dispatch picks the model; T-865: a configured lane's row
// always names its dispatch's type). One read of the row, no git: a free lane whose row keeps its last task answers
// that task's model, as dispatchType and setup.start do. No row, no task or no type: "", false.
func LaneModel(cfg *config.Config, lane string) (ref string, ok bool) {
	row, ok, _ := store.ReadLaneAt(cfg.Root, lane)
	if !ok || row.Task == "" || row.Type == "" {
		return "", false
	}
	return row.Type, true
}

// RunningKind is the kind of the model info runs now: a lane agent's is its lane's open dispatch's (LaneModel), else
// info.Type's; the kind, else agents.Default (no model, an unknown one or an unreadable store is no kind).
func RunningKind(cfg *config.Config, info config.AgentInfo) string {
	ref := info.Type
	if info.Lane != "" {
		if laneRef, ok := LaneModel(cfg, info.Lane); ok {
			ref = laneRef
		}
	}
	typ, _ := config.ParseModelRef(ref)
	if m, ok := cfg.Model(typ); ok && m.Kind != "" {
		return m.Kind
	}
	return agents.Default
}
