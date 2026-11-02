// Package roles is the single source for role names and their runtime defaults.
package roles

import (
	"obor/internal/agents"
	"regexp"
	"strings"
)

var compactLane = regexp.MustCompile(`^([a-z]+)-?0*([0-9]+)$`)

type CwdRule string
type WriteProfile string

const (
	CwdProject        CwdRule      = "project"
	CwdLane           CwdRule      = "lane"
	CwdRoleWorktree   CwdRule      = "role-worktree"
	WriteNone         WriteProfile = "none"
	WriteAll          WriteProfile = "all"
	WriteSpec         WriteProfile = "spec"
	WriteReviewsOnly  WriteProfile = "reviews"
	WriteResearchOnly WriteProfile = "research"
)

// Role records the defaults shared by setup, config validation, and the guard.
// EnvValue is empty for roles that setup deliberately does not export.
type Role struct {
	Name         string
	EnvValue     string
	AgentSuffix  string
	Worktree     string
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
	{Name: "reviewer", EnvValue: "reviewer", AgentSuffix: "rev", Worktree: "review", Cwd: CwdRoleWorktree, Write: WriteReviewsOnly, GitReadOnly: true},
	{Name: "deep_dive", EnvValue: "deep_dive", AgentSuffix: "deep-dive", Worktree: "deep-dive", Cwd: CwdRoleWorktree, Write: WriteReviewsOnly, Optional: true, GitReadOnly: true},
	{Name: "triage", AgentSuffix: "triage", Cwd: CwdProject, Write: WriteNone, Optional: true, GitReadOnly: true},
	{Name: "advisor", EnvValue: "advisor", AgentSuffix: "advisor", Cwd: CwdProject, Write: WriteNone, Optional: true, GitReadOnly: true},
	{Name: "researcher", EnvValue: "researcher", AgentSuffix: "researcher", Cwd: CwdProject, Write: WriteResearchOnly, Optional: true, GitReadOnly: true},
}

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

// AgentName applies a configured roles.<name>.agent override, otherwise the
// table default. A worker's suffix comes from its lane name.
func (r Role) AgentName(prefix, lane, override string) string {
	if override != "" {
		return override
	}
	suffix := r.AgentSuffix
	if r.Name == "worker" {
		suffix = strings.ToLower(lane)
	}
	if suffix == "" || prefix == "" {
		return ""
	}
	return prefix + "-" + suffix
}

// LaneAgentAliases returns every name that can identify a lane's worker, first
// match wins: the lane row's agent, the configured one, <prefix>-<lane lower>,
// <prefix>-<compact lane> (o-01 → o1); empty and repeated names dropped. The bare lane lower (o-01) is no name
// (T-302): agent names are machine-wide, and another project's agent may be named like a lane here.
func LaneAgentAliases(lane, prefix, stateAgent, configuredAgent string) []string {
	lower := strings.ToLower(lane)
	names := []string{stateAgent, configuredAgent, prefix + "-" + lower}
	if match := compactLane.FindStringSubmatch(lower); len(match) == 3 {
		number := strings.TrimLeft(match[2], "0")
		if number == "" {
			number = "0"
		}
		names = append(names, prefix+"-"+match[1]+number)
	}
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
