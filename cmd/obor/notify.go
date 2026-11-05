package main

// obor projects --notify <file> (T-715): the release notes to every project's lead. For each project of the machine's
// list (obor projects) but the caller's own, `obor send <project> "obor v<version> is installed. <notes>"`; one line
// per project, a failing send is reported and the rest go out (exit 1 at the end). make ship runs it last, with
// docs/releases/v<version>.md at the tag.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/app/wire"
	"obor/internal/config"
	"obor/internal/help"
	"obor/internal/project"
	"obor/internal/version"
)

// notifier is one `obor projects --notify`; Send is its outside system (tests fake it).
type notifier struct {
	Out      io.Writer
	Version  string // this binary's version, as the message says it
	Notes    string // the release notes, on one line
	Projects []project.Project
	Own      string // the root of the caller's own project: its lead is not told
	Send     func(ctx context.Context, p project.Project, text string) error
}

// projectsNotify is `obor projects --notify <file>`.
func projectsNotify(args []string) int {
	if len(args) != 2 || args[0] != "--notify" {
		fmt.Fprintln(out, "FAIL "+help.Usage("projects --notify"))
		return 1
	}
	b, err := os.ReadFile(args[1])
	if err != nil || strings.TrimSpace(string(b)) == "" {
		fmt.Fprintf(out, "FAIL --notify: %s is missing or empty\n", args[1])
		return 1
	}
	env := project.OSEnv
	cwd, _ := os.Getwd()
	repo, err := project.Find(cwd)
	var own *config.Config
	if err == nil {
		own, _ = config.LoadDaemon(repo.Root)
	}
	if own == nil {
		fmt.Fprintln(out, "FAIL --notify needs the sending project: run it in the kit checkout")
		return 1
	}
	n := &notifier{Out: out, Version: version.Version, Notes: strings.Join(strings.Fields(string(b)), " "),
		Projects: project.Projects(env), Own: own.Root}
	n.Send = func(ctx context.Context, p project.Project, text string) error {
		c := caller(env, cwd, own)
		c.Root, c.Note, c.From = p.Root, text, own.Data.Name
		if c.From == "" {
			c.From = filepath.Base(own.Root)
		}
		_, err := wire.Verdict(ctx, wire.SocketPath(p.Root), wire.MethodSend, c)
		return err
	}
	if n.run(context.Background()) {
		return 0
	}
	return 1
}

// run tells the leads; it reports whether every send went through.
func (n *notifier) run(ctx context.Context) bool {
	if len(n.Projects) == 0 {
		fmt.Fprintln(n.Out, "no obor projects known yet (obor serve and obor init register one)")
	}
	ok := true
	for _, p := range n.Projects {
		if project.Physical(p.Root) == project.Physical(n.Own) {
			fmt.Fprintf(n.Out, "%s: not told (this project's own lead)\n", p.Name)
			continue
		}
		text := fmt.Sprintf("obor %s is installed. %s", n.Version, n.Notes)
		if err := n.Send(ctx, p, text); err != nil {
			ok = false
			fmt.Fprintf(n.Out, "FAIL %s: send: %v\n", p.Name, err)
			continue
		}
		fmt.Fprintf(n.Out, "%s: lead told\n", p.Name)
	}
	return ok
}
