package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"obor/internal/project"
)

// notifyFixture is a notifier whose sends are recorded; failSend names the projects whose send fails.
type notifyFixture struct {
	n        *notifier
	out      bytes.Buffer
	sent     []string
	failSend map[string]bool
}

func newNotifyFixture(own string, names ...string) *notifyFixture {
	f := &notifyFixture{failSend: map[string]bool{}}
	var ps []project.Project
	for _, n := range names {
		ps = append(ps, project.Project{Name: n, Root: "/w/" + n})
	}
	f.n = &notifier{Out: &f.out, Version: "v0.17.0", Notes: "- **Fast:** faster.", Projects: ps, Own: "/w/" + own,
		Send: func(_ context.Context, p project.Project, text string) error {
			if f.failSend[p.Name] {
				return errors.New("serve down")
			}
			f.sent = append(f.sent, p.Name+": "+text)
			return nil
		}}
	return f
}

// T-715: every registered project's lead but the kit's gets exactly "obor v<version> is installed. <notes>".
func TestNotifyTellsAllLeadsButTheKit(t *testing.T) {
	t.Parallel()
	f := newNotifyFixture("kit", "alpha", "kit", "beta")
	if !f.n.run(context.Background()) {
		t.Fatalf("notify failed:\n%s", f.out.String())
	}
	text := "obor v0.17.0 is installed. - **Fast:** faster."
	if want := []string{"alpha: " + text, "beta: " + text}; fmt.Sprint(f.sent) != fmt.Sprint(want) {
		t.Errorf("sent = %q, want %q", f.sent, want)
	}
	for _, want := range []string{"alpha: lead told", "beta: lead told", "kit: not told"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, f.out.String())
		}
	}
}

// T-715: a failing send is reported and the others still go out; the run fails.
func TestNotifyFailingSendDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	f := newNotifyFixture("kit", "alpha", "beta", "gamma")
	f.failSend["alpha"] = true
	if f.n.run(context.Background()) {
		t.Errorf("notify says it went through:\n%s", f.out.String())
	}
	if len(f.sent) != 2 || !strings.HasPrefix(f.sent[0], "beta: ") || !strings.HasPrefix(f.sent[1], "gamma: ") {
		t.Errorf("sent = %q, want beta and gamma", f.sent)
	}
	if !strings.Contains(f.out.String(), "FAIL alpha: send: serve down") {
		t.Errorf("no FAIL line for alpha:\n%s", f.out.String())
	}
}

// T-715: the flags: no --notify, --notify without a file, an unknown flag and a notes file that is missing or empty are
// refused before anything is sent.
func TestProjectsNotifyFlags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := dir + "/empty.md"
	writeFile(t, empty, "\n")
	for _, args := range [][]string{{"--notify"}, {"--bogus"}, {"--notify", dir + "/none.md"}, {"--notify", empty}} {
		rc, stdout, _ := run(t, dir, []string{"OBOR_STATE_HOME=" + dir}, append([]string{"projects"}, args...)...)
		if rc != 1 || !strings.HasPrefix(stdout, "FAIL ") {
			t.Errorf("obor projects %v: exit %d, %q; want a FAIL, exit 1", args, rc, stdout)
		}
	}
	// A real notes file outside a project: refused for want of a sender.
	notes := dir + "/notes.md"
	writeFile(t, notes, "- **Fast:** faster.\n")
	rc, stdout, _ := run(t, dir, []string{"OBOR_STATE_HOME=" + dir}, "projects", "--notify", notes)
	if rc != 1 || !strings.Contains(stdout, "needs the sending project") {
		t.Errorf("--notify outside a project: exit %d, %q", rc, stdout)
	}
}
