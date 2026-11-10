package launch

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"obor/internal/agents"
	"obor/internal/project"
)

// versions.go: how the agent CLIs name their own versions (T-1176), for serve's read of the tools it drives
// (internal/app/versions.go). Nothing here is part of a start.

// versionTimeout bounds one CLI's --version (claude 23 ms, codex 48 ms, opencode 80 ms when measured, 2026-11-10).
const versionTimeout = 3 * time.Second

// versionBytes is how much of a CLI's answer is kept; versionChars how long a version line may be.
const (
	versionBytes = 512
	versionChars = 80
)

// CLIVersions is how each of kinds' CLI names its own version: the first line of `<kind> --version`, trimmed
// ("2.1.322 (Claude Code)", "codex-cli 0.167.0"), by kind, for the CLIs on env's PATH. A kind has no entry when its
// CLI is not on PATH, may not start on this machine (agents.StartRefusal: a quarantined install is not run), fails,
// prints nothing, or does not answer in versionTimeout: not read is not a version. The line is compared as text,
// never parsed.
func CLIVersions(ctx context.Context, env project.Env, kinds []string) map[string]string {
	return cliVersions(ctx, env, kinds, versionTimeout)
}

// cliVersions is CLIVersions with each CLI bounded by timeout; the kinds run one after another.
func cliVersions(ctx context.Context, env project.Env, kinds []string, timeout time.Duration) map[string]string {
	if env == nil {
		env = project.OSEnv
	}
	path, _ := env("PATH")
	host := project.InstallHost(env)
	out := map[string]string{}
	for _, kind := range kinds {
		bin, err := project.LookPath(path, kind)
		if err != nil || agents.StartRefusal(kind, host) != "" {
			continue
		}
		if v := cliVersion(ctx, bin, timeout); v != "" {
			out[kind] = v
		}
	}
	return out
}

// cliVersion is the first non-blank line of `bin --version`, trimmed and cut to versionChars ("": it failed, printed
// nothing or did not answer in timeout). The CLI runs as its own process group with no stdin; its stderr is dropped.
func cliVersion(ctx context.Context, bin string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	var got head
	cmd.Stdout = &got
	cleanup := project.Group(cmd)
	err := cmd.Run()
	cleanup()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(got), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > versionChars {
				line = string(r[:versionChars])
			}
			return line
		}
	}
	return ""
}

// head is a writer that keeps the first versionBytes written to it and drops the rest.
type head []byte

func (h *head) Write(p []byte) (int, error) {
	if room := versionBytes - len(*h); room > 0 {
		*h = append(*h, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
