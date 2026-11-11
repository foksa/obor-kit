package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/project"
)

const (
	evidenceLines = 15  // screen lines
	evidenceWidth = 200 // chars per line
)

// Evidenced is a Fail that carries the evidence of why the pane did not do as asked (the agent's status, the screen's
// tail, the processes) apart from its one-line message. A caller that shows evidence prints it before the FAIL line.
type Evidenced struct {
	Fail     project.Fail
	Evidence string
}

func (e *Evidenced) Error() string { return string(e.Fail) }
func (e *Evidenced) Unwrap() error { return e.Fail }

// Evidence is the evidence err carries ("" for any other error).
func Evidence(err error) string {
	var e *Evidenced
	if errors.As(err, &e) {
		return e.Evidence
	}
	return ""
}

// giveUp is a one-line Fail with its evidence, which is also appended to .obor/state/launch.log. The evidence never
// fails the Fail: whatever herdr cannot say is said in its place.
func (l *Launcher) giveUp(ctx context.Context, t *target, format string, a ...any) error {
	msg := fmt.Sprintf(format, a...)
	ev := l.evidence(ctx, t)
	l.logEvidence(t, msg, ev)
	return &Evidenced{Fail: project.Fail(msg), Evidence: ev}
}

// evidence is three labelled blocks: agent status, screen tail, processes.
func (l *Launcher) evidence(ctx context.Context, t *target) string {
	var b strings.Builder

	status := "(unavailable: no agent in the pane)"
	if a, err := l.H.Pane(ctx, t.pane); err != nil {
		status = fmt.Sprintf("(unavailable: %v)", err)
	} else if a.Agent != "" {
		status = a.Status
		if status == "" {
			status = "unknown"
		}
	}
	fmt.Fprintf(&b, "agent status: %s\n", clip(status))

	fmt.Fprintf(&b, "pane screen (last %d non-empty lines):\n", evidenceLines)
	if screen, err := l.H.Screen(ctx, t.pane, 60); err != nil {
		fmt.Fprintf(&b, "  (screen unavailable: %v)\n", err)
	} else {
		var lines []string
		for _, ln := range strings.Split(screen, "\n") {
			if strings.TrimSpace(ln) != "" {
				lines = append(lines, strings.TrimRight(ln, " \t\r"))
			}
		}
		lines = lines[max(0, len(lines)-evidenceLines):]
		if len(lines) == 0 {
			b.WriteString("  (empty)\n")
		}
		for _, ln := range lines {
			fmt.Fprintf(&b, "  %s\n", clip(ln))
		}
	}

	b.WriteString("pane processes:\n")
	pi, err := l.H.ProcessInfo(ctx, t.pane)
	if err != nil {
		fmt.Fprintf(&b, "  (process info unavailable: %v)", err)
		return b.String()
	}
	fg := pi.Foreground()
	if fg == "" {
		fg = "unknown"
	}
	where := "foreground"
	if pi.AtShell() {
		where = "foreground (the shell)"
	}
	fmt.Fprintf(&b, "  %s: %s (process group %d, shell %d)\n", where, fg, pi.FgPGID, pi.ShellPID)
	for _, pr := range pi.Processes {
		cmd := pr.Cmdline
		if cmd == "" {
			cmd = pr.Program()
		}
		fmt.Fprintf(&b, "  in the group: %d %s\n", pr.PID, clip(cmd))
	}
	return strings.TrimRight(b.String(), "\n")
}

// logEvidence appends one block to <root>/.obor/state/launch.log; a log that cannot be written is not worth failing for.
func (l *Launcher) logEvidence(t *target, msg, ev string) {
	if l.Cfg == nil {
		return
	}
	dir := project.StateDir(l.Cfg.Root)
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "launch.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s pane %s: %s\n%s\n\n", time.Now().UTC().Format(time.RFC3339), t.agent, t.pane,
		strings.ReplaceAll(msg, "\n", " "), ev)
}

func clip(s string) string {
	if r := []rune(s); len(r) > evidenceWidth {
		return string(r[:evidenceWidth]) + "..."
	}
	return s
}
