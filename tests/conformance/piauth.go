// The pi live tier's authentication: pi reaches its model's provider through the credential its own auth check
// resolves — a stored one under its agent directory (auth.json), the provider's environment key, or an OAuth or auth
// token variable — and the throwaway session's clean environment carries none. piAuthOf decides with that check, run in
// the environment the pane will get, and returns the variables the pane needs past the clean environment; the tier
// SKIPs with piAuthOf's error when the check is not ready. The check is non-secret and no line this file's callers
// print carries a credential: a fact names the check's answer, and an error names the sources it looked at.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// piAuth is how a pi start reaches its model's provider: how it resolves (a fact line: the check's answer, never a
// credential) and the caller's variables the pane needs past the clean environment.
type piAuth struct {
	How  string
	Vars map[string]string
}

// piStatus is pi's own auth check's answer for a provider (its --json fields; no credential).
type piStatus struct {
	Status   string `json:"status"`
	AuthType string `json:"authType"`
	Reason   string `json:"reason"`
}

// piAuthCheck runs pi's non-secret check of provider in env — the environment the pane will get — and returns its
// answer: status is "ready" when pi can authenticate the provider from there.
type piAuthCheck func(provider string, env []string) (piStatus, error)

// piAgentDir is pi's agent directory, absolute: PI_CODING_AGENT_DIR, or <home>/.pi/agent (pi's configuration doc); ""
// when neither names one.
func piAgentDir(home string, getenv func(string) string) string {
	if dir := getenv("PI_CODING_AGENT_DIR"); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".pi", "agent")
}

// piEnvKeys are the environment names pi reads for provider's credential (pi's providers doc); none for a provider
// this tier knows no variable of. The tier never prints their values.
func piEnvKeys(provider string) []string {
	if provider == "anthropic" {
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"}
	}
	return nil
}

// piAuthOf resolves how pi authenticates model's provider: the variables the pane needs (its agent directory, and every
// credential variable of the provider the caller has), then pi's own check of that environment. base is the
// environment the pane inherits before those variables (the lab's clean environment). An error is the tier's SKIP
// line: the check's answer and the sources looked at, never a credential.
func piAuthOf(agentDir, model string, base []string, getenv func(string) string, check piAuthCheck) (piAuth, error) {
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return piAuth{}, fmt.Errorf("the model %q names no provider (pi takes provider/id)", model)
	}
	if agentDir == "" {
		return piAuth{}, fmt.Errorf("no pi agent directory (no home directory and no PI_CODING_AGENT_DIR)")
	}
	keys := piEnvKeys(provider)
	vars := map[string]string{"PI_CODING_AGENT_DIR": agentDir}
	for _, k := range keys {
		if v := getenv(k); v != "" {
			vars[k] = v
		}
	}
	st, err := check(provider, envPlus(base, vars))
	switch {
	case err != nil:
		return piAuth{}, fmt.Errorf("pi's own auth check for %s failed: %v", provider, err)
	case st.Status != "ready":
		return piAuth{}, fmt.Errorf("no %s credential pi accepts: its own auth check says %s%s; the sources are an "+
			"auth.json entry under %s and %s", provider, statusOf(st), reasonOf(st), agentDir, sourcesOf(provider, keys, vars))
	}
	how := "pi's own auth check: ready"
	if st.AuthType != "" {
		how += " (" + st.AuthType + " for " + provider + ")"
	}
	return piAuth{How: how, Vars: vars}, nil
}

// statusOf is the check's status, worded for a skip line.
func statusOf(st piStatus) string {
	if st.Status == "" {
		return "no status"
	}
	return st.Status
}

// reasonOf is the check's reason in parentheses, "" without one.
func reasonOf(st piStatus) string {
	if st.Reason == "" {
		return ""
	}
	return " (" + st.Reason + ")"
}

// sourcesOf is the credential sources a skip line names: the provider's environment variables, each marked set or not
// in vars, or that this tier knows none.
func sourcesOf(provider string, keys []string, vars map[string]string) string {
	if len(keys) == 0 {
		return "no environment variable known for " + provider
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, ok := vars[k]; ok {
			parts = append(parts, k+" is set")
		} else {
			parts = append(parts, k+" is not set")
		}
	}
	return strings.Join(parts, ", ")
}

// envPlus is base plus vars, their names sorted: the environment pi's check reads, which the pane will get.
func envPlus(base []string, vars map[string]string) []string {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := append([]string{}, base...)
	for _, k := range keys {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// piAuthCheckTimeout bounds pi's own check: it reads the local credential store and refreshes nothing.
const piAuthCheckTimeout = 30 * time.Second

// piAuthCheckRun runs `pi auth check --provider <provider> --no-refresh --json` from env and decodes its answer. The
// command's own exit status is part of the answer (not_ready exits 1); an answer it cannot decode is the error, never
// the output, which may hold a credential.
func piAuthCheckRun(provider string, env []string) (piStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), piAuthCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pi", "auth", "check", "--provider", provider, "--no-refresh", "--json")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil && out.Len() == 0 {
		return piStatus{}, fmt.Errorf("`pi auth check` for %s: %v", provider, err)
	}
	var st piStatus
	if json.Unmarshal(out.Bytes(), &st) != nil || st.Status == "" {
		return piStatus{}, fmt.Errorf("`pi auth check` for %s printed no status", provider)
	}
	return st, nil
}
