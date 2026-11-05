package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	herdrun "obor/internal/run"
)

// runCmd is `obor run <name> [args…]` (internal/run). -h / --help first: usage; right after the name: usage and the
// command's configured line, nothing run; anything else (also after --) goes to the command.
func runCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(out, "FAIL "+help.Usage("run"))
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(out, help.Usage("run"))
		return 0
	}
	cwd, _ := os.Getwd()
	repo, err := project.Find(cwd)
	var cfg *config.Config
	if err == nil {
		cfg, err = config.Load(repo.Root)
	}
	if err != nil {
		fmt.Fprintln(out, "FAIL no usable .obor/config.json (obor config validate)")
		return 2
	}
	c, ok := herdrun.Command(cfg, args[0])
	if ok && len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		fmt.Fprintln(out, help.Usage("run"))
		fmt.Fprintln(out, herdrun.Line(c))
		return 0
	}
	out.Flush() // the command writes to stdout itself
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	interrupted := make(chan int, 1)
	go func() {
		codes := map[os.Signal]int{syscall.SIGINT: 130, syscall.SIGTERM: 143, syscall.SIGHUP: 129}
		interrupted <- codes[<-sigs]
	}()
	return herdrun.Run(herdrun.Options{Cfg: cfg, Env: project.OSEnv, Environ: os.Environ(), Dir: cwd, Name: args[0],
		Args: args[1:], Stdout: os.Stdout, Stderr: os.Stderr, PID: os.Getpid(), Interrupted: interrupted})
}
