package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"obor/internal/app/port"
	"obor/internal/project"
)

// fakePane is one herdr pane of an upgrade: its foreground process (nil: the shell has it), whether Ctrl-C ends that
// process, and what was done to it.
type fakePane struct {
	argv     []string
	ignoreC  bool
	keys     []string
	typed    []string
	shellPID int
}

// fakeHerdr is herdr as the upgrade sees it.
type fakeHerdr struct {
	panes    []string // ids, in order
	by       map[string]*fakePane
	listErr  error
	typeFail map[string]bool
}

func (f *fakeHerdr) Panes(context.Context) ([]port.PaneInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []port.PaneInfo
	for _, id := range f.panes {
		out = append(out, port.PaneInfo{ID: id})
	}
	return out, nil
}

func (f *fakeHerdr) ProcessInfo(_ context.Context, id string) (port.ProcessInfo, error) {
	p := f.by[id]
	pi := port.ProcessInfo{Pane: id, ShellPID: 1, FgPGID: 1}
	if p.argv != nil {
		pi.FgPGID = 7
		pi.Processes = []port.Process{{PID: 7, Argv0: p.argv[0], Argv: p.argv, Cmdline: strings.Join(p.argv, " ")}}
	}
	return pi, nil
}

func (f *fakeHerdr) SendKeys(_ context.Context, id string, keys ...string) error {
	p := f.by[id]
	p.keys = append(p.keys, keys...)
	if !p.ignoreC {
		p.argv = nil
	}
	return nil
}

func (f *fakeHerdr) Type(_ context.Context, id, text string) error {
	if f.typeFail[id] {
		return errors.New("herdr refused")
	}
	f.by[id].typed = append(f.by[id].typed, text)
	return nil
}

const (
	newBin = "/home/me/.obor/bin/obor-0.17.0-darwin-arm64"
	oldBin = "/home/me/.obor/bin/obor-0.16.0-darwin-arm64"
)

func projectsOf(names ...string) []project.Project {
	var ps []project.Project
	for _, n := range names {
		ps = append(ps, project.Project{Name: n, Root: "/w/" + n})
	}
	return ps
}

// upgradeFixture is an upgrader whose serve restarts and sends are recorded; failRestart and failSend name the
// projects whose step fails.
type upgradeFixture struct {
	u                     *upgrader
	out                   bytes.Buffer
	restarted, sent       []string
	failRestart, failSend map[string]bool
}

func newUpgradeFixture(h *fakeHerdr, names ...string) *upgradeFixture {
	f := &upgradeFixture{failRestart: map[string]bool{}, failSend: map[string]bool{}}
	f.u = &upgrader{Out: &f.out, Version: "v0.17.0", Binary: newBin, Projects: projectsOf(names...),
		Wait: 50 * time.Millisecond, Poll: time.Millisecond,
		Restart: func(_ context.Context, p project.Project) (string, error) {
			if f.failRestart[p.Name] {
				return "", errors.New("launchd said no")
			}
			f.restarted = append(f.restarted, p.Name)
			return "restarted", nil
		},
		Send: func(_ context.Context, p project.Project, text string) error {
			if f.failSend[p.Name] {
				return errors.New("serve down")
			}
			f.sent = append(f.sent, p.Name+": "+text)
			return nil
		}}
	if h != nil {
		f.u.H = h
	}
	return f
}

// T-715: a monitor and a web on an older binary get Ctrl-C and the same command again; one on the installed binary, an
// unrelated program and a pane at its shell are not touched.
func TestUpgradeRestartsOnlyMonitorAndWebOfAnOlderBinary(t *testing.T) {
	t.Parallel()
	h := &fakeHerdr{
		panes: []string{"p-old-monitor", "p-old-web", "p-new-monitor", "p-vim", "p-shell", "p-old-serve", "p-bare"},
		by: map[string]*fakePane{
			"p-old-monitor": {argv: []string{oldBin, "monitor"}},
			"p-old-web":     {argv: []string{oldBin, "web", "--listen", "127.0.0.1:8080"}},
			"p-new-monitor": {argv: []string{newBin, "monitor"}},
			"p-vim":         {argv: []string{"/usr/bin/vim", "monitor"}},
			"p-shell":       {},
			"p-old-serve":   {argv: []string{oldBin, "serve"}},
			"p-bare":        {argv: []string{"obor", "monitor"}},
		}}
	f := newUpgradeFixture(h, "alpha")
	if len(h.by) != len(h.panes) || h.by["p-old-monitor"].argv == nil {
		t.Fatalf("precondition: the fake has the panes")
	}
	if !f.u.run(context.Background()) {
		t.Fatalf("the upgrade failed:\n%s", f.out.String())
	}
	want := map[string][]string{"p-old-monitor": {"obor monitor"}, "p-old-web": {"obor web --listen 127.0.0.1:8080"}}
	for id, p := range h.by {
		typed, keys := want[id], []string(nil)
		if typed != nil {
			keys = []string{"ctrl+c"}
		}
		if fmt.Sprint(p.typed) != fmt.Sprint(typed) || fmt.Sprint(p.keys) != fmt.Sprint(keys) {
			t.Errorf("pane %s got keys %v and typed %v, want %v and %v", id, p.keys, p.typed, keys, typed)
		}
	}
	for _, id := range []string{"p-new-monitor", "p-bare"} {
		if !strings.Contains(f.out.String(), id) {
			t.Errorf("no line says why pane %s was left:\n%s", id, f.out.String())
		}
	}
	if fmt.Sprint(f.restarted) != "[alpha]" {
		t.Errorf("serve restarts = %v, want [alpha]", f.restarted)
	}
}

// T-715: a pane that keeps running after Ctrl-C is reported and nothing is typed into it; the other panes still go.
func TestUpgradeStuckPaneIsReportedAndTheRestGoOn(t *testing.T) {
	t.Parallel()
	h := &fakeHerdr{panes: []string{"p-stuck", "p-ok"}, by: map[string]*fakePane{
		"p-stuck": {argv: []string{oldBin, "monitor"}, ignoreC: true},
		"p-ok":    {argv: []string{oldBin, "monitor"}}}}
	f := newUpgradeFixture(h, "alpha")
	if f.u.run(context.Background()) {
		t.Errorf("the upgrade says it went through:\n%s", f.out.String())
	}
	if len(h.by["p-stuck"].typed) != 0 || fmt.Sprint(h.by["p-stuck"].keys) != "[ctrl+c]" {
		t.Errorf("the stuck pane got keys %v, typed %v", h.by["p-stuck"].keys, h.by["p-stuck"].typed)
	}
	if fmt.Sprint(h.by["p-ok"].typed) != "[obor monitor]" {
		t.Errorf("the pane after the stuck one was not restarted: %v", h.by["p-ok"].typed)
	}
	if !strings.Contains(f.out.String(), "FAIL pane p-stuck") {
		t.Errorf("no FAIL line for the stuck pane:\n%s", f.out.String())
	}
}

// T-715: a project whose serve restart fails is reported; the others restart, their panes are handled and (with notes)
// their leads told; the failed project's lead is not told its serve is restarted.
func TestUpgradeFailingProjectDoesNotStopTheOthers(t *testing.T) {
	t.Parallel()
	h := &fakeHerdr{panes: []string{"p"}, by: map[string]*fakePane{"p": {argv: []string{oldBin, "monitor"}}}}
	f := newUpgradeFixture(h, "alpha", "beta", "gamma")
	f.failRestart["alpha"] = true
	f.failSend["gamma"] = true
	f.u.Notify, f.u.Notes = true, "Fast."
	if f.u.run(context.Background()) {
		t.Errorf("the upgrade says it went through:\n%s", f.out.String())
	}
	if fmt.Sprint(f.restarted) != "[beta gamma]" {
		t.Errorf("serve restarts = %v, want [beta gamma]", f.restarted)
	}
	if fmt.Sprint(h.by["p"].typed) != "[obor monitor]" {
		t.Errorf("the monitor pane was not restarted after a failed project: %v", h.by["p"].typed)
	}
	for _, want := range []string{"FAIL alpha: serve restart: launchd said no", "FAIL gamma: send: serve down",
		"alpha: not told", "beta: lead told"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("the output lacks %q:\n%s", want, f.out.String())
		}
	}
	if len(f.sent) != 1 || !strings.HasPrefix(f.sent[0], "beta: ") {
		t.Errorf("sent = %v, want only beta (alpha's serve did not restart, gamma's send failed)", f.sent)
	}
}

// T-715: --notify tells every project's lead but the caller's own project's, with the version and the notes.
func TestUpgradeNotifyTellsAllLeadsButTheKit(t *testing.T) {
	t.Parallel()
	f := newUpgradeFixture(nil, "alpha", "kit", "beta")
	f.u.Own, f.u.Notify, f.u.Notes = "/w/kit", true, "- **Fast:** faster."
	if !f.u.run(context.Background()) {
		t.Fatalf("the upgrade failed:\n%s", f.out.String())
	}
	text := "obor v0.17.0 is installed and your serve is restarted. - **Fast:** faster."
	if fmt.Sprint(f.sent) != fmt.Sprint([]string{"alpha: " + text, "beta: " + text}) {
		t.Errorf("sent = %q", f.sent)
	}
	if fmt.Sprint(f.restarted) != "[alpha kit beta]" {
		t.Errorf("the kit's own serve is restarted too: %v", f.restarted)
	}

	// Without --notify nobody is told.
	g := newUpgradeFixture(nil, "alpha", "beta")
	if !g.u.run(context.Background()) || len(g.sent) != 0 || fmt.Sprint(g.restarted) != "[alpha beta]" {
		t.Errorf("without Notify: sent %v, restarted %v:\n%s", g.sent, g.restarted, g.out.String())
	}
}

// T-715: herdr not answering is one line; the serves are restarted and the leads told all the same.
func TestUpgradeHerdrDownIsOneLine(t *testing.T) {
	t.Parallel()
	f := newUpgradeFixture(&fakeHerdr{listErr: errors.New("no socket")}, "alpha")
	f.u.Notify, f.u.Notes = true, "n"
	if f.u.run(context.Background()) {
		t.Errorf("the upgrade says it went through:\n%s", f.out.String())
	}
	if !strings.Contains(f.out.String(), "FAIL herdr panes: no socket") || len(f.sent) != 1 || len(f.restarted) != 1 {
		t.Errorf("sent %v, restarted %v:\n%s", f.sent, f.restarted, f.out.String())
	}
}

func TestShellQuote(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"web": "web", "127.0.0.1:80": "127.0.0.1:80", "a b": "'a b'", "it's": `'it'\''s'`, "": "''"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// T-715: the flags: --notify alone, an unknown flag and a notes file that is missing or empty are refused before
// anything runs.
func TestProjectsUpgradeFlags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := dir + "/empty.md"
	writeFile(t, empty, "\n")
	for _, args := range [][]string{{"--notify", empty}, {"--bogus"}, {"--upgrade", "--notify"}, {"--upgrade", "--notify", dir + "/none.md"},
		{"--upgrade", "--notify", empty}} {
		rc, stdout, _ := run(t, dir, []string{"OBOR_STATE_HOME=" + dir}, append([]string{"projects"}, args...)...)
		if rc != 1 || !strings.HasPrefix(stdout, "FAIL ") {
			t.Errorf("obor projects %v: exit %d, %q; want a FAIL, exit 1", args, rc, stdout)
		}
	}
}
