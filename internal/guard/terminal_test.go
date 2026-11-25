package guard

import "testing"

// In every agent's session but the lead's, the hc verbs that create, end, type into or relabel terminals (and run or
// update the core), a bare hc (it attaches) and herdr-app do are denied, wrapped or by path too; the reads (hc ls, get,
// read, wait, schema, an option alone, any --help; herdr-app ls, status, commands) stay allowed. The lead and the
// owner's role-less session in the main checkout run them all.
func TestTerminalCLIDeniedOutsideTheLead(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	const why = "a way around dispatch"
	var cases []tc
	for _, cmd := range []string{"hc run --bg claude", "hc kill t3", "hc rm t3", "hc send t3 'do it'", "hc keys t3 enter",
		"hc interrupt t3", "hc label t3 obor.agent=x", "hc attach t3", "hc server restart --fresh", "hc server stop",
		"hc update", "hc --json run sleep 1", "/Users/x/.local/bin/hc kill t1", "env HC_SESSION=s hc send t1 x", "hc",
		"herdr-app do space.new --name x", "herdr-app do '{\"command\":\"tab.new\"}'"} {
		cases = append(cases, bash("worker", L, cmd, why))
		cases = append(cases, bash("lead", P, cmd, ""))
		cases = append(cases, bash("-", P, cmd, ""))
	}
	for _, role := range []string{"integrator", "reviewer"} {
		cases = append(cases, bash(role, P, "hc send t1 x", "only the lead drives terminals"),
			bash(role, P, "herdr-app do tab.new", "only the lead drives the window"))
	}
	for _, cmd := range []string{"hc ls", "hc ls --all", "hc get t3", "hc read t3", "hc wait t3", "hc schema", "hc --version",
		"hc -V", "hc run --help", "hc server --help", "hc send -h", "hc events", "herdr-app ls", "herdr-app status",
		"herdr-app commands", "herdr-app do --help", "herdr-app --version", "echo hc run", "grep 'hc kill' notes.md"} {
		cases = append(cases, bash("worker", L, cmd, ""))
	}
	cases = append(cases,
		bash("worker", L, "hc ls && hc kill t2", "hc kill creates, ends, types into or relabels terminals"),
		bash("worker", L, "hc", "hc (it attaches to a terminal)"),
		bash("worker", L, "herdr-app do space.close", "herdr-app do runs a window command"))
	check(t, f.Project, cases)
}

// In every agent's session but the lead's, every rex command that is no read is denied, wrapped or by path too: a bare
// rex (it attaches), the commands that make, end, type into, move or relabel sessions, windows and blocks, a call of a
// write method, and the server's and setup's changes. The reads (ls, capture, events, wait, the resources' ls and
// inspect, api describe, a call of a read method, any --help) stay allowed. The lead and the owner's role-less session
// in the main checkout run them all.
func TestRexCLIDeniedOutsideTheLead(t *testing.T) {
	t.Parallel()
	f := fixture(t)
	P, L := f.P, f.L
	const why = "a way around dispatch"
	var cases []tc
	for _, cmd := range []string{"rex", "rex --autostart=false", "rex new work", "rex run -- claude", "rex split",
		"rex send -b block:1 'do it'", "rex send-key -s s1 -b block:1 enter", "rex kill s1", "rex do layout.lua",
		"rex focus block:1", "rex zoom", "rex move block:1", "rex swap block:1 block:2", "rex resize block:1",
		"rex forward new 8080", "rex fwd open 8080", "rex login-server",
		"rex session new x", "rex session rename s1 y", "rex session kill s1", "rex session move s1",
		"rex window new", "rex window close w1", "rex window rename w1 x", "rex window focus w1",
		"rex block new", "rex block send -b block:1 x", "rex block send-key enter", "rex block close block:1",
		"rex block rename block:1 x", "rex block call write '{\"data\":\"ls\\n\"}'", "rex block call -b block:1 clear",
		"rex api call session.create '{\"label\":\"x\"}'", "rex api call block.event @ev.json", "rex api call",
		"rex api call -s work session.destroy", "rex api call com.superlogical.terminal.write '{}'",
		"rex server stop", "rex server run --listen unix:///tmp/s.sock", "rex server start", "rex server install",
		"rex config reload", "rex home set h1", "rex hosts add h1", "rex terminfo setup", "rex skill setup",
		"rex session", "rex -S unix:///tmp/s.sock send x", "rex --server unix:///tmp/s.sock kill s1",
		"/Applications/Rex\\ Beta.app/Contents/Helpers/Rex\\ Server.rex/Contents/MacOS/rex send x",
		"env XDG_RUNTIME_DIR=/tmp/x rex send x"} {
		cases = append(cases, bash("worker", L, cmd, why))
		cases = append(cases, bash("lead", P, cmd, ""))
		cases = append(cases, bash("-", P, cmd, ""))
	}
	for _, role := range []string{"integrator", "reviewer"} {
		cases = append(cases, bash(role, P, "rex send -b block:1 x", "only the lead drives terminals"),
			bash(role, P, "rex ls", ""))
	}
	for _, cmd := range []string{"rex ls", "rex list --json", "rex capture -b block:1", "rex events --all --json",
		"rex wait block:1", "rex actions", "rex keymap", "rex whoami", "rex version", "rex help send", "rex completion zsh",
		"rex session ls", "rex session inspect s1", "rex window list", "rex block ls -s s1", "rex block inspect --json",
		"rex block types", "rex block capture -b block:1", "rex block stream -b block:1", "rex block call process",
		"rex block call -b block:1 format '{\"format\":\"text\"}'", "rex client ls", "rex api ls", "rex api describe session.view",
		"rex api call server.status", "rex api call -s work session.view", "rex --autostart=false api call session.list",
		"rex api call com.superlogical.terminal.program_status '{}'", "rex server status", "rex server logs",
		"rex config check", "rex hosts list", "rex home", "rex terminfo status", "rex skill show", "rex forward ls",
		"rex send --help", "rex server stop -h", "rex -S unix:///tmp/s.sock ls", "echo rex send x", "grep 'rex kill' notes.md"} {
		cases = append(cases, bash("worker", L, cmd, ""))
	}
	cases = append(cases,
		bash("worker", L, "rex", "rex (it attaches to a session)"),
		bash("worker", L, "rex ls && rex send -b block:1 x", "rex send creates, ends, types into or relabels sessions"),
		bash("worker", L, "rex block close block:1", "rex block close creates"),
		bash("worker", L, "rex api call session.create '{}'", "rex api call session.create creates"),
		bash("worker", L, "rex server stop", "rex server stop creates, ends"))
	check(t, f.Project, cases)
}
