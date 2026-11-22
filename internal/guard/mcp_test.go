package guard

import (
	"path/filepath"
	"testing"

	"obor/internal/agents"
)

// An MCP tool call (tool_name "mcp", tool_input.mcp_server) is a lane-bound role's business only: a worker and the
// owner's drive agent call the servers the project declares in agents.ProjectMCPFile and obor's, nothing else; with
// no file (or one that does not parse) obor's server alone. The file is the main checkout's: a lane worktree's copy
// declares nothing. The lead and every other role agent keep every server (no opinion).
func TestMCPServersProjectOnlyForLaneBound(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	if f.Exists(agents.ProjectMCPFile) {
		t.Fatal("precondition: the fixture's project declares no MCP server yet")
	}
	const rule = "obor lanes use the project's MCP servers only (.agents/mcp_config.json; owner 11-22)"
	declared := `{"mcpServers": {"obor": {"command": "obor", "args": ["mcp"]}, "probe-a": {"command": "probe-a"}}}`
	// No project file yet: obor's server alone.
	check(t, f.Project, []tc{
		mcp("-", L, "obor", ""),
		mcp("worker", L, "obor", ""),
		mcp("driver", L, "obor", ""),
		mcp("-", L, "probe-a", rule),
		mcp("worker", P, "probe-a", rule), // the rule is the role's, the session need not run in its lane
		mcp("lead", P, "probe-a", ""),
		mcp("reviewer", P, "probe-a", ""),
		mcp("-", P, "probe-a", ""), // a role-less session in the main checkout
	})
	// A lane worktree's copy is not the project's file: read from the main checkout, this one declares nothing.
	f.Write(filepath.Join(L, agents.ProjectMCPFile), declared)
	check(t, f.Project, []tc{mcp("-", L, "probe-a", rule)})
	// The project's own file: its servers and obor's, nothing else.
	f.Write(agents.ProjectMCPFile, declared)
	check(t, f.Project, []tc{
		mcp("-", L, "obor", ""),
		mcp("-", L, "probe-a", ""),
		mcp("worker", P, "probe-a", ""),
		mcp("driver", L, "probe-a", ""),
		mcp("-", L, "probe-b", rule), // the user's own, or an agy plugin's
		mcp("-", L, "OBOR", rule),    // a server's name is exact: a look-alike is another server
		mcp("-", L, "", "an MCP call naming no server — "+rule),
		mcp("lead", P, "probe-b", ""),
	})
	// A file that does not parse declares nothing.
	f.Write(agents.ProjectMCPFile, `{"mcpServers": `)
	check(t, f.Project, []tc{mcp("-", L, "probe-a", rule), mcp("-", L, "obor", "")})
	// The denial is one line, pinned once.
	f.Write(agents.ProjectMCPFile, declared)
	if code, reason := runAs(f.Project, "-", mcp("-", L, "probe-b", "").input(nil)); code != 2 ||
		reason != "MCP server probe-b — "+rule {
		t.Errorf("a lane's MCP call to an undeclared server: %d %q, want the one-line denial", code, reason)
	}
}
