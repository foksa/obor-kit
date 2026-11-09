package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"obor/internal/app/wire"
	"obor/internal/checks"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
)

// nightlyCmd is `obor nightly` (NIGHTLY: one run now), or `obor nightly on HH:MM | off` (nightlySwitch).
func nightlyCmd(args []string) int {
	switch {
	case len(args) == 0:
		return checksRun(func(o checks.Options) checks.Result { return checks.Nightly(o) })
	case len(args) == 2 && args[0] == "on", len(args) == 1 && args[0] == "off":
		return nightlySwitch(args)
	}
	fmt.Fprintln(out, "FAIL "+help.Usage("nightly"))
	return 1
}

// nightlySwitch is `obor nightly on HH:MM | off` (T-781), the lead's or the owner's switch of serve's nightly: a
// config_set of nightly.at to the time (on: every night at it) or null (off), one commit on main; serve checks
// the time (config.Edit) and the caller. NIGHTLY ON at HH:MM | NIGHTLY OFF, "… already" when it was so (no commit),
// or FAIL: serve down or its refusal (exit 1).
func nightlySwitch(args []string) int {
	val, line := json.RawMessage("null"), "NIGHTLY OFF"
	if args[0] == "on" {
		b, _ := json.Marshal(args[1]) // a string: cannot fail
		val, line = b, "NIGHTLY ON at "+args[1]
	}
	nightly := []wire.ConfigChange{{Key: "nightly.at", Value: val}}
	return requestReply(context.Background(), project.OSEnv, func(_ *config.Config, id wire.Envelope) (wire.Req, error) {
		return &wire.ConfigSetReq{Envelope: id, Changes: nightly}, nil
	}, func(reply string) string {
		if strings.HasPrefix(reply, "UNCHANGED ") {
			return line + " already"
		}
		return line
	})
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
