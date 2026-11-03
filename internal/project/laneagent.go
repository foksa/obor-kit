package project

import (
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

// RunningType is the model an agent of info runs now (T-739): a lane agent's is the model its lane's open dispatch
// runs (the row holds a task; a dispatch recorded before T-721 names none); no lane, no row or no task in it:
// info.Type. The session hook's kind, the plugin marker and a restore's resume read a lane's kind through it, so a
// lane runs the kind its dispatch chose.
func RunningType(cfg *config.Config, info config.AgentInfo) string {
	if info.Lane == "" {
		return info.Type
	}
	if row, ok, _ := store.ReadLaneAt(cfg.Root, info.Lane); ok && row.Task != "" {
		typ, _ := config.ParseModelRef(row.Type)
		return typ
	}
	return info.Type
}
