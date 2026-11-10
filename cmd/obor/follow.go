package main

import (
	"obor/internal/project"
	"obor/internal/selfexec"
)

// The long-running commands (serve, web) follow a new obor binary: each asks binaryWatch's Changed on
// its own cadence and then execs the kit's shim (selfExec) with its own arguments.

// binaryWatch is the watch of env's kit binary for this process: nil when it is off (the running binary is not the
// kit's candidate: a test binary, a lane's own build). A test replaces it.
var binaryWatch = func(env project.Env) func() bool {
	w := selfexec.New(project.KitRoot(env), env.Get("HOME"))
	if !w.On() {
		return nil
	}
	return w.Changed
}

// selfExec replaces this process with kit's shim and args (selfexec.Exec: returns only on failure). A test replaces it.
var selfExec = selfexec.Exec
