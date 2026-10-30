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
