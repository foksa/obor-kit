package main

import (
	"fmt"

	"obor/internal/app"
	"obor/internal/help"
	"obor/internal/project"
)

// projectsCmd is `obor projects`: the obor projects this machine knows by name, the list obor serve (at its start) and
// obor init keep (T-646): "<name> <root> serve <up|down>", one per line, by name, a stale entry left out. Serve is up
// while the project's serve holds its lock.
func projectsCmd(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(out, "FAIL "+help.Usage("projects"))
		return 1
	}
	ps := project.Projects(project.OSEnv)
	if len(ps) == 0 {
		fmt.Fprintln(out, "no obor projects known yet (obor serve and obor init register one)")
		return 0
	}
	for _, p := range ps {
		fmt.Fprintf(out, "%s %s serve %s\n", p.Name, p.Root, serveState(p.Root))
	}
	return 0
}

// serveState is the project's serve: up while it holds <root>/.obor/state/serve.flock, down otherwise.
func serveState(root string) string {
	if project.NewLock(app.FlockFile(root)).Held() {
		return "up"
	}
	return "down"
}
