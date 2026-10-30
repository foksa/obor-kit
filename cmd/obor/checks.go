package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"obor/internal/checks"
	"obor/internal/project"
)

// nightlyCmd is `obor nightly` (NIGHTLY) and `obor milestone-check` (MILESTONE).
func nightlyCmd(name, label string, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(out, "FAIL usage: obor "+name)
		return 1
	}
	return checksRun(func(o checks.Options) checks.Result { return checks.Nightly(o, label) })
}

// checksRun loads the project and runs body with the process's environment; INT/TERM/HUP become its exit code.
func checksRun(body func(checks.Options) checks.Result) int {
	_, cfg, rc := load()
	if rc != 0 {
		fmt.Fprintln(out, "FAIL no usable .obor/config.json (obor config validate)")
		return 1
	}
	out.Flush() // progress lines go straight to stdout while the checks run
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	interrupted := make(chan int, 1)
	go func() {
		codes := map[os.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129}
		interrupted <- codes[<-sigs]
	}()
	res := body(checks.Options{
		Cfg: cfg, Env: project.OSEnv, Environ: os.Environ(), Out: os.Stdout, PID: os.Getpid(), Interrupted: interrupted,
	})
	if res.Line != "" {
		fmt.Fprintln(out, res.Line)
	}
	return res.Code
}
