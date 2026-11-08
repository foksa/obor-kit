// Package roles is the single source for the built-in roles and their runtime defaults, and the shape of a custom role
// (T-801: a roles.<name> row of the config, or one of the kit's default rows): config.Role is the one lookup of both.
package roles

import (
	"fmt"
	"obor/internal/agents"
	"regexp"
	"slices"
	"strings"
)

type CwdRule string
type WriteProfile string

const (
	CwdProject CwdRule = "project"
	CwdLane    CwdRule = "lane"
	// CwdRoleWorktree is a worktree serve owns, <lanes_dir>/<role> (T-853: the reviewer, the deep dive, the integrator),
	// detached; never a lane. Serve moves the reviewer's and the deep dive's to the head it hands them.
	CwdRoleWorktree  CwdRule      = "role-worktree"
	WriteNone        WriteProfile = "none"
	WriteAll         WriteProfile = "all"
	WriteSpec        WriteProfile = "spec"
	WriteReviewsOnly WriteProfile = "reviews"
	WriteList        WriteProfile = "list" // a custom role: only its WritePaths (none: writes nothing)
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
	// Custom is a role a config row defines (T-801), a kit default row or the project's own: main checkout, git
	// read-only, never lane-bound or dispatchable, no push, writes only WritePaths.
	Custom bool
	// WritePaths are a custom role's writes, relative to the root: "dir/" a directory, anything else one file.
	WritePaths []string
	// Verbs are a custom role's wire methods serve admits it to as it admits the lead and the owner (T-1039:
	// roles.<name>.verbs, each one of Verbs): an integration's role takes a task and briefs the lead. They add rights,
	// never remove one; a built-in role has none.
	Verbs []string
	// Seeds are the files setup role copies for a custom role when missing: a write path → its kit template under
	// templates/ (triage's docs/BUGS.md).
	Seeds map[string]string
}

var table = []Role{
	{Name: "lead", EnvValue: "lead", AgentSuffix: "lead", Cwd: CwdProject, Write: WriteAll, Prompt: "/obor:lead"},
	// The integrator runs in its own worktree (T-853), not the merge scratch, where gates run: a review job may check
	// out the candidate there; its resolve jobs still work in the scratch the job names. Serve adds it (the launcher,
	// obor setup role) but never moves it.

	{Name: "integrator", EnvValue: "integrator", AgentSuffix: "int", Cwd: CwdRoleWorktree, Write: WriteNone},
	{Name: "worker", EnvValue: "worker", AgentSuffix: "<lane>", Cwd: CwdLane, Write: WriteSpec, Stoppable: true, Dispatchable: true},
	// The reviewer and the deep dive read their own worktree (T-853; T-718 had them in the main checkout behind a merge
	// hold): serve moves it to the head it hands them, so merges never wait. Their files go to the main checkout's
	// paths.reviews (the guard's WriteReviewsOnly).
	{Name: "reviewer", EnvValue: "reviewer", AgentSuffix: "rev", Cwd: CwdRoleWorktree, Write: WriteReviewsOnly, GitReadOnly: true},
	{Name: "deep_dive", EnvValue: "deep_dive", AgentSuffix: "deep-dive", Cwd: CwdRoleWorktree, Write: WriteReviewsOnly, Optional: true, GitReadOnly: true},
	// The owner's drive agent (T-751, obor drive): in a lane, with the worker's write limits and push denial, but it
	// commits on its branch, pulls no jobs, has no Stop-hook report demand and no wrong-tab guard. Never configured
	// (no roles.driver): obor drive starts it, named Drive's way.
	{Name: Driver, EnvValue: Driver, AgentSuffix: "drive", Cwd: CwdLane, Write: WriteSpec, Optional: true},
}

// defaultRows are the custom roles the kit ships (T-801: advisor, researcher and triage left the table): a config row of
// the name sets its model (a string) or replaces its write list (an object with write). Their doc is the project's
// <paths.agents>/<name>.md, else the kit's templates/agents/<name>.md.
var defaultRows = []Role{
	Custom("advisor", []string{"docs/research/", "docs/plan/"}),
	Custom("researcher", []string{"docs/research/"}),
	seeded(Custom("triage", []string{"docs/BUGS.md"}), "docs/BUGS.md", "BUGS.md"),
}

func seeded(r Role, path, template string) Role {
	r.Seeds = map[string]string{path: template}
	return r
}

// DefaultRow is the kit's default row of a custom role name.
func DefaultRow(name string) (Role, bool) {
	for _, r := range defaultRows {
		if r.Name == name {
			return r, true
		}
	}
	return Role{}, false
}

// DefaultRows returns a copy of the kit's default rows, in their order.
func DefaultRows() []Role { return append([]Role(nil), defaultRows...) }

// Custom is the custom role name writing only write (T-801): an advisor-like role in the main checkout, its agent
// <prefix>-<name, _ → ->, OBOR_ROLE=<name>.
func Custom(name string, write []string) Role {
	return Role{Name: name, EnvValue: name, AgentSuffix: strings.ReplaceAll(name, "_", "-"), Cwd: CwdProject,
		Write: WriteList, WritePaths: write, Optional: true, GitReadOnly: true, Custom: true}
}

// Verbs are the wire methods a custom role's roles.<name>.verbs may name (T-1039): the lead's and the owner's calls an
// integration needs to take a task (spec_set, dispatch), follow it (watch) and brief the lead or a role agent (send,
// brief). The names are internal/app/wire's Method* (its test keeps the two alike); serve's admission of each reads
// HasVerb.
var Verbs = []string{"spec_set", "dispatch", "watch", "send", "brief"}

// HasVerb: r is a custom role whose verbs name method.
func (r Role) HasVerb(method string) bool {
	return r.Custom && slices.Contains(r.Verbs, method)
}

// CustomNameRE is what a custom role's name must match.
var CustomNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)

// CheckCustomName is why name cannot name a custom role (nil: it can): it must match CustomNameRE and be neither a
// table role's name nor its agent suffix (its own suffix, _ → -, neither), nor reserved (none, leads).
func CheckCustomName(name string) error {
	if !CustomNameRE.MatchString(name) {
		return fmt.Errorf("a custom role's name must match %s", CustomNameRE)
	}
	if name == "none" || name == "leads" { // the role of a session with none; the removed roles.leads (T-467)
		return fmt.Errorf("%s is reserved", name)
	}
	suffix := strings.ReplaceAll(name, "_", "-")
	for _, r := range table {
		for _, n := range []string{name, suffix} {
			if n == r.Name || n == r.AgentSuffix || n == strings.ReplaceAll(r.Name, "_", "-") {
				return fmt.Errorf("collides with the built-in role %s (its name or agent suffix)", r.Name)
			}
		}
	}
	return nil
}

// DocFile is the file name of the role's text: a custom role's <name>.md, a table role's with _ → - (deep-dive.md).
func (r Role) DocFile() string {
	if r.Custom {
		return r.Name + ".md"
	}
	return strings.ReplaceAll(r.Name, "_", "-") + ".md"
}

// Driver is the owner's drive agent's role (T-751).
const Driver = "driver"

// DriveAgent is the drive agent's name in a lane whose worker is worker: <worker>-drive, a name of its own so its pane
// (in the owner's tab) never shares a label with the lane's worker pane.
func DriveAgent(worker string) string { return worker + "-drive" }

// LaneBound: role works in a lane worktree and edits only there (the worker, the driver).
func LaneBound(role string) bool { return role == "worker" || role == Driver }

// All returns a copy of the built-in table in its stable order (config.Config.Roles adds the custom rows).
func All() []Role { return append([]Role(nil), table...) }

// Lookup returns the table entry for a role name (a built-in role; config.Config.Role also finds custom ones).
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

// LaneAgentAliases returns every name that can identify a lane's worker: <prefix>-<lane lower> (T-1031: the name is
// derived, no lane row names one). The bare lane lower (o-01) is no name (T-302): agent names are machine-wide, and
// another project's agent may be named like a lane here.
func LaneAgentAliases(lane, prefix string) []string {
	return []string{prefix + "-" + strings.ToLower(lane)}
}

// StartPrompt is the first prompt of a fresh start of kind in this role: only a kind that takes a plugin's slash
// command as its first prompt (agents.Caps.Skills) gets the role's Prompt; another kind starts without one.
func (r Role) StartPrompt(kind string) string {
	if !agents.Skills(kind) {
		return ""
	}
	return r.Prompt
}
