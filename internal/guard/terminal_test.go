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
