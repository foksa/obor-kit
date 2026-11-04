// Package roles is the single source for role names and their runtime defaults.
package roles

import (
	"obor/internal/agents"
	"strings"
)

type CwdRule string
type WriteProfile string

const (
	CwdProject        CwdRule      = "project"
	CwdLane           CwdRule      = "lane"
	WriteNone         WriteProfile = "none"
	WriteAll          WriteProfile = "all"
	WriteSpec         WriteProfile = "spec"
	WriteReviewsOnly  WriteProfile = "reviews"
	WriteResearchOnly WriteProfile = "research"
)

// Role records the defaults shared by setup, config validation, and the guard.
// EnvValue is the role's OBOR_ROLE value: every role has one (T-796: triage too), so the derived identity covers it.
type Role struct {
	Name         string
	EnvValue     string
	AgentSuffix  string
	Cwd          CwdRule
	Write        WriteProfile
	Stoppable    bool
	Dispatchable bool
	Prompt       string // the first prompt of a fresh start of a Claude agent in this role ("": none)
	Optional     bool   // counts (known agents, collisions) only when roles.<name> is configured
	GitReadOnly  bool   // runs git reads only (the guard's read list); the lead commits its files (RV-83 #2)
}

var table = []Role{
	{Name: "lead", EnvValue: "lead", AgentSuffix: "lead", Cwd: CwdProject, Write: WriteAll, Prompt: "/obor:lead"},
	{Name: "integrator", EnvValue: "integrator", AgentSuffix: "int", Cwd: CwdProject, Write: WriteNone},
	{Name: "worker", EnvValue: "worker", AgentSuffix: "<lane>", Cwd: CwdLane, Write: WriteSpec, Stoppable: true, Dispatchable: true},
	// The reviewer and the deep dive read the main checkout (T-718): serve holds its merges while they work.
	{Name: "reviewer", EnvValue: "reviewer", AgentSuffix: "rev", Cwd: CwdProject, Write: WriteReviewsOnly, GitReadOnly: true},
	{Name: "deep_dive", EnvValue: "deep_dive", AgentSuffix: "deep-dive", Cwd: CwdProject, Write: WriteReviewsOnly, Optional: true, GitReadOnly: true},
	{Name: "triage", EnvValue: "triage", AgentSuffix: "triage", Cwd: CwdProject, Write: WriteNone, Optional: true, GitReadOnly: true},
	{Name: "advisor", EnvValue: "advisor", AgentSuffix: "advisor", Cwd: CwdProject, Write: WriteNone, Optional: true, GitReadOnly: true},
	{Name: "researcher", EnvValue: "researcher", AgentSuffix: "researcher", Cwd: CwdProject, Write: WriteResearchOnly, Optional: true, GitReadOnly: true},
	// The owner's drive agent (T-751, obor drive): in a lane, with the worker's write limits and push denial, but it
	// commits on its branch, pulls no jobs, has no Stop-hook report demand and no wrong-tab guard. Never configured
	// (no roles.driver): obor drive starts it, named Drive's way.
	{Name: Driver, EnvValue: Driver, AgentSuffix: "drive", Cwd: CwdLane, Write: WriteSpec, Optional: true},
}

// Driver is the owner's drive agent's role (T-751).
const Driver = "driver"

// DriveAgent is the drive agent's name in a lane whose worker is worker: <worker>-drive, a name of its own so its pane
// (in the owner's tab) never shares a label with the lane's worker pane.
func DriveAgent(worker string) string { return worker + "-drive" }

// LaneBound: role works in a lane worktree and edits only there (the worker, the driver).
func LaneBound(role string) bool { return role == "worker" || role == Driver }

// All returns a copy in the stable setup/config order.
func All() []Role { return append([]Role(nil), table...) }

// Lookup returns the table entry for a role name.
func Lookup(name string) (Role, bool) {
	for _, role := range table {
		if role.Name == name {
			return role, true
		}
	}
	return Role{}, false
}

// LookupEnv finds a role by its OBOR_ROLE value. Roles without an exported env value are not found.
func LookupEnv(value string) (Role, bool) {
	for _, role := range table {
		if value != "" && role.EnvValue == value {
			return role, true
		}
	}
	return Role{}, false
}

// AgentName is the table default: <prefix>-<role suffix>, a worker's suffix from its lane name (T-765: a name is
// derived, never configured).
func (r Role) AgentName(prefix, lane string) string {
	suffix := r.AgentSuffix
	if r.Name == "worker" {
		suffix = strings.ToLower(lane)
	}
	if suffix == "" || prefix == "" {
		return ""
	}
	return prefix + "-" + suffix
}

// LaneAgentAliases returns every name that can identify a lane's worker, first match wins: the lane row's agent, then
// <prefix>-<lane lower>; empty and repeated names dropped. The bare lane lower (o-01) is no name (T-302): agent names
// are machine-wide, and another project's agent may be named like a lane here.
func LaneAgentAliases(lane, prefix, stateAgent string) []string {
	names := []string{stateAgent, prefix + "-" + strings.ToLower(lane)}
	seen := make(map[string]bool)
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if name != "" && !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}
	return unique
}

// StartPrompt is the first prompt of a fresh start of kind in this role: only a kind that takes a plugin's slash
// command as its first prompt (agents.Caps.Skills) gets the role's Prompt; another kind starts without one.
func (r Role) StartPrompt(kind string) string {
	if !agents.Skills(kind) {
		return ""
	}
	return r.Prompt
}
