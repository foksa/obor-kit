package launch_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"obor/internal/agents"
	"obor/internal/app/port"
	"obor/internal/launch"
	"obor/internal/testkit"
)

// quarantineHost is a Mac whose resolved codex install carries com.apple.quarantine with flags on
// codex-code-mode-host (flags 0: nothing quarantined; a non-darwin machine: the Mac's lines are ignored).
func quarantineHost(goos string, flags uint32) agents.InstallHost {
	const (
		link = "/c/bin/codex"
		ver  = "/c/0.167.0"
		host = ver + "/bin/codex-code-mode-host"
	)
	return agents.InstallHost{
		GOOS: goos,
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				return "", fs.ErrNotExist
			}
			return link, nil
		},
		Resolve: func(p string) (string, error) {
			if p == link {
				return ver + "/bin/codex", nil
			}
			return p, nil
		},
		Quarantine: func(p string) (uint32, bool) {
			if p == host && flags != 0 {
				return flags, true
			}
			return 0, false
		},
	}
}

// a Codex start whose resolved install carries an unapproved quarantine flag is not made: the launch refuses
// before anything is stopped or typed, with one line naming the file and `xattr -dr com.apple.quarantine <version dir>`,
// as port.Refused (serve's start boundary turns it into a needs-you). An approved file (0x01c1) or no flag starts.
func TestLaunchRefusesQuarantinedCodex(t *testing.T) {
	t.Parallel()
	const fix = "xattr -dr com.apple.quarantine /c/0.167.0"
	for _, c := range []struct {
		name                   string
		agent, pane, role, typ string
	}{
		{"worker", "pj-l-01", "w1:p4", "worker", "luna"},
		{"reviewer", "pj-rev", "w1:p7", "reviewer", ""},
	} {
		t.Run(c.name+": refused", func(t *testing.T) {
			t.Parallel()
			x := setup(t)
			x.install = quarantineHost("darwin", 0x0181)
			x.shell(c.pane, c.agent, "")
			var out bytes.Buffer
			_, err := x.launcher(&out).Launch(context.Background(),
				launch.Request{Agent: c.agent, Pane: c.pane, Fresh: true, Type: c.typ})
			var refused port.Refused
			if !errors.As(err, &refused) || !strings.Contains(refused.Line, "/c/0.167.0/bin/codex-code-mode-host") ||
				!strings.Contains(refused.Line, fix) {
				t.Fatalf("%s start: %v, want a refusal naming the file and %q", c.role, err, fix)
			}
			if x.startCall() != "" || len(x.calls("Write")) != 0 {
				t.Errorf("the refused start typed or started something: %q", x.f.Calls())
			}
			if strings.Contains(out.String(), "RESTARTED") {
				t.Errorf("a refused start said RESTARTED:\n%s", out.String())
			}
		})
		t.Run(c.name+": approved, no flag, not a Mac", func(t *testing.T) {
			t.Parallel()
			for _, ok := range []struct {
				name string
				h    agents.InstallHost
			}{
				{"approved", quarantineHost("darwin", 0x01c1)},
				{"no flag", quarantineHost("darwin", 0)},
				{"not a Mac", quarantineHost("linux", 0x0181)},
			} {
				x := setup(t)
				x.install = ok.h
				x.shell(c.pane, c.agent, "")
				out, started := x.restart(launch.Request{Agent: c.agent, Pane: c.pane, Fresh: true, Type: c.typ})
				if !started || !strings.HasPrefix(last(out), "RESTARTED "+c.agent) {
					t.Errorf("%s start, %s: %q, %v", c.role, ok.name, last(out), started)
				}
			}
		})
	}
}

// the lead's start and the owner's drive agent's are not refused — the owner is at its screen, so the Gatekeeper
// dialog can be answered: each warns with the same line and starts, in setup's form (NoStop: `obor setup role lead
// --start` for obor lead, obor drive's start) and, for the lead, as a restart.
func TestLaunchLeadWarnsQuarantinedCodex(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("agents.roles", `{"integrator":"opus","lead":"luna","reviewer":"luna"}`))
	x.install = quarantineHost("darwin", 0x0181)
	const fix = "xattr -dr com.apple.quarantine /c/0.167.0"

	for _, c := range []struct{ what, agent, pane, cwd, typ, prompt string }{
		{"obor lead", "pj-lead", "w1:p8", "", "", ""},
		{"obor drive", "pj-l-01-drive", "w1:p9", x.lane, "luna", "drive T-07"},
	} {
		x.shell(c.pane, c.agent, c.cwd)
		var out bytes.Buffer
		started, err := x.launcher(&out).Launch(context.Background(), launch.Request{Agent: c.agent, Fresh: true,
			NoStop: true, Type: c.typ, Prompt: c.prompt})
		if err != nil || started.Pane != c.pane || started.Kind != "codex" {
			t.Fatalf("%s with a quarantined codex: %+v, %v\n%s", c.what, started, err, out.String())
		}
		if !strings.Contains(out.String(), "warning: codex is quarantined:") || !strings.Contains(out.String(), fix) {
			t.Errorf("%s's start, want the quarantine warning and %q:\n%s", c.what, fix, out.String())
		}
	}

	// obor restart pj-lead: the same warning, the start made.
	x2 := setup(t, testkit.Set("agents.roles", `{"integrator":"opus","lead":"luna","reviewer":"luna"}`))
	x2.install = quarantineHost("darwin", 0x0181)
	x2.shell("w1:p8", "pj-lead", "")
	restarted, ok := x2.restart(launch.Request{Agent: "pj-lead", Pane: "w1:p8", Fresh: true})
	if !ok || !strings.HasPrefix(last(restarted), "RESTARTED pj-lead") {
		t.Fatalf("a lead restart with a quarantined codex: %q", last(restarted))
	}
	if !strings.Contains(restarted, "warning: codex is quarantined:") || !strings.Contains(restarted, fix) {
		t.Errorf("the lead's restart, want the warning and %q:\n%s", fix, restarted)
	}
}

// a NoStop start (obor setup role/worker --start) of a role with a quarantined codex is refused like a restart: only
// the lead and the driver warn. The placement is not called.
func TestLaunchNoStopRefusesQuarantinedCodex(t *testing.T) {
	t.Parallel()
	x := setup(t, testkit.Set("agents.roles", `{"integrator":"opus","lead":"luna","reviewer":"luna"}`))
	x.install = quarantineHost("darwin", 0x0181)
	var pl placer
	var out bytes.Buffer
	_, err := x.launcher(&out).Launch(context.Background(), launch.Request{Agent: "pj-rev", Fresh: true, NoStop: true,
		Place: pl.at(x, "w1:p7", "")})
	var refused port.Refused
	if !errors.As(err, &refused) || !strings.Contains(refused.Line, "xattr -dr com.apple.quarantine /c/0.167.0") {
		t.Fatalf("NoStop start with a quarantined codex: %v, want a refusal naming the fix", err)
	}
	if pl.calls != 0 || x.startCall() != "" || len(x.calls("Write")) != 0 {
		t.Errorf("placement called %d times, start %q, writes %q: want none", pl.calls, x.startCall(), x.calls("Write"))
	}
}
