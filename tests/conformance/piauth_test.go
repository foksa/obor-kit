package main

// piAuthOf resolves the pi live tier's authentication with pi's own non-secret check: an OAuth token among the caller's
// variables is carried into the pane and is ready, and neither a variable nor a stored credential is the tier's SKIP,
// whose line names the sources and no credential. The check itself is stubbed: no unit test runs pi.

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPiAuthOf(t *testing.T) {
	const (
		model  = "anthropic/claude-haiku-4-5"
		secret = "oauth-test-secret" // cannot be a real one: this test would forward it to nothing
	)
	dir := filepath.Join(t.TempDir(), "agent")
	ready := func(provider string, env []string) (piStatus, error) {
		if provider != "anthropic" {
			t.Errorf("the check ran for %q; want the model's provider anthropic", provider)
		}
		return piStatus{Status: "ready", AuthType: "api_key"}, nil
	}
	t.Run("an OAuth token in the caller's environment is ready and reaches the pane", func(t *testing.T) {
		var checked []string
		check := func(_ string, env []string) (piStatus, error) {
			checked = env
			return ready("anthropic", env)
		}
		base := []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
		a, err := piAuthOf(dir, model, base, func(k string) string {
			if k == "ANTHROPIC_OAUTH_TOKEN" {
				return secret
			}
			return ""
		}, check)
		if err != nil {
			t.Fatalf("piAuthOf: %v; want the OAuth token to authenticate", err)
		}
		if a.Vars["ANTHROPIC_OAUTH_TOKEN"] != secret {
			t.Error("Vars does not carry the caller's OAuth token into the pane")
		}
		if a.Vars["PI_CODING_AGENT_DIR"] != dir {
			t.Errorf("Vars[PI_CODING_AGENT_DIR] = %q; want the agent dir", a.Vars["PI_CODING_AGENT_DIR"])
		}
		if _, ok := a.Vars["ANTHROPIC_API_KEY"]; ok {
			t.Error("Vars carries the API key with no key in the environment")
		}
		if !strings.Contains(a.How, "ready") || strings.Contains(a.How, secret) {
			t.Errorf("How = %q; want the check's answer and no credential", a.How)
		}
		// The check ran in the environment the pane will get: the base plus the carried variables.
		for _, want := range append(base, "ANTHROPIC_OAUTH_TOKEN="+secret, "PI_CODING_AGENT_DIR="+dir) {
			if !slices.Contains(checked, want) {
				t.Errorf("the check's environment lacks %q; want the pane's", want)
			}
		}
	})
	t.Run("nothing is the skip naming the sources", func(t *testing.T) {
		_, err := piAuthOf(dir, model, nil, func(string) string { return "" }, func(string, []string) (piStatus, error) {
			return piStatus{Status: "not_ready", Reason: "credentials_not_configured"}, nil
		})
		if err == nil {
			t.Fatal("piAuthOf authenticated with no credential")
		}
		for _, want := range []string{"no anthropic credential", "not_ready", "credentials_not_configured", dir, "auth.json",
			"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the skip line %q does not name %q", err, want)
			}
		}
	})
	t.Run("a set token the check refuses is named as set", func(t *testing.T) {
		_, err := piAuthOf(dir, model, nil, func(k string) string {
			if k == "ANTHROPIC_OAUTH_TOKEN" {
				return secret
			}
			return ""
		}, func(string, []string) (piStatus, error) {
			return piStatus{Status: "not_ready", Reason: "credentials_not_configured"}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_OAUTH_TOKEN is set") ||
			!strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") {
			t.Fatalf("piAuthOf = %v; want the skip naming the token it had and the one it lacked", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Error("the skip line carries the token's value")
		}
	})
	t.Run("a check that fails skips naming the failure", func(t *testing.T) {
		_, err := piAuthOf(dir, model, nil, func(string) string { return "" }, func(string, []string) (piStatus, error) {
			return piStatus{}, errors.New("pi is not installed")
		})
		if err == nil || !strings.Contains(err.Error(), "pi's own auth check for anthropic failed: pi is not installed") {
			t.Fatalf("piAuthOf = %v; want the skip naming the check's failure", err)
		}
	})
	t.Run("no agent directory skips naming it", func(t *testing.T) {
		_, err := piAuthOf("", model, nil, func(string) string { return "" }, ready)
		if err == nil || !strings.Contains(err.Error(), "no pi agent directory") {
			t.Fatalf("piAuthOf = %v; want a skip naming the missing agent directory", err)
		}
	})
}

func TestPiAgentDir(t *testing.T) {
	home := t.TempDir()
	if got, want := piAgentDir(home, func(string) string { return "" }), filepath.Join(home, ".pi", "agent"); got != want {
		t.Errorf("piAgentDir with no variable = %q; want the default %q", got, want)
	}
	dir := filepath.Join(home, "pi-agent")
	if got := piAgentDir(home, func(k string) string {
		if k == "PI_CODING_AGENT_DIR" {
			return dir
		}
		return ""
	}); got != dir {
		t.Errorf("piAgentDir with PI_CODING_AGENT_DIR = %q; want the variable's directory %q", got, dir)
	}
	if got := piAgentDir("", func(string) string { return "" }); got != "" {
		t.Errorf("piAgentDir with no home and no variable = %q; want none", got)
	}
}
