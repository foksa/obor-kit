// The pi live tier's authentication: pi reaches its model's provider through a stored credential — an auth.json entry
// for the provider under its agent directory — or through the provider's environment key, and the throwaway session's
// clean environment carries neither. piAuthOf resolves which one a start would use and the variables its pane needs;
// the tier SKIPs with piAuthOf's error when neither is there. No line this file's callers print carries a credential:
// a fact names the source, and an error names the places looked in.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// piAuth is how a pi start reaches its model's provider: how it resolves (a fact line: the source, never a credential)
// and the caller's variables the pane needs past the clean environment.
type piAuth struct {
	How  string
	Vars map[string]string
}

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

// piEnvKey is the environment key pi reads for provider when auth.json has no entry for it (pi's providers doc); the
// tier's model is Anthropic's.
func piEnvKey(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	}
	return ""
}

// piAuthOf resolves how pi reaches model's provider: an auth.json entry for the provider under agentDir, or the
// provider's environment key. Vars carries the agent directory and the caller's provider key when set, which the clean
// environment leaves out. An error is the tier's SKIP line: what is missing, never a credential.
func piAuthOf(agentDir, model string, getenv func(string) string) (piAuth, error) {
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return piAuth{}, fmt.Errorf("the model %q names no provider (pi takes provider/id)", model)
	}
	if agentDir == "" {
		return piAuth{}, fmt.Errorf("no pi agent directory (no home directory and no PI_CODING_AGENT_DIR)")
	}
	key := piEnvKey(provider)
	vars := map[string]string{"PI_CODING_AGENT_DIR": agentDir}
	if key != "" {
		if v := getenv(key); v != "" {
			vars[key] = v
		}
	}
	if piAuthEntry(agentDir, provider) {
		return piAuth{How: "the auth.json entry for " + provider + " under " + agentDir, Vars: vars}, nil
	}
	switch {
	case key == "":
		return piAuth{}, fmt.Errorf("no %s credential: no auth.json entry under %s, and this tier knows no environment key for %s",
			provider, agentDir, provider)
	case vars[key] == "":
		return piAuth{}, fmt.Errorf("no %s credential: no auth.json entry under %s, and %s is not set", provider, agentDir, key)
	}
	return piAuth{How: key, Vars: vars}, nil
}

// piAuthEntry: agentDir's auth.json holds a credential for provider (an entry that is no empty value or null).
func piAuthEntry(agentDir, provider string) bool {
	b, err := os.ReadFile(filepath.Join(agentDir, "auth.json"))
	if err != nil {
		return false
	}
	var auth map[string]json.RawMessage
	if json.Unmarshal(b, &auth) != nil {
		return false
	}
	entry := strings.TrimSpace(string(auth[provider]))
	return entry != "" && entry != "null"
}
