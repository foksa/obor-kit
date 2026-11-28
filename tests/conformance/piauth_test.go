package main

// piAuthOf resolves the pi live tier's authentication: an auth.json entry for the model's provider under the agent
// directory, else the provider's environment key; neither is the tier's SKIP, whose line names both places and no
// credential. The pane needs the agent dir and the caller's key, which the clean environment leaves out.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiAuthOf(t *testing.T) {
	const (
		model  = "anthropic/claude-haiku-4-5"
		secret = "sk-ant-test-secret"
	)
	t.Run("the auth.json entry for the provider authenticates", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"anthropic":{"type":"oauth","access":"stored-secret"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		a, err := piAuthOf(dir, model, func(string) string { return "" })
		if err != nil {
			t.Fatalf("piAuthOf: %v; want the provider's entry to authenticate", err)
		}
		if !strings.Contains(a.How, "auth.json entry for anthropic") || strings.Contains(a.How, "stored-secret") {
			t.Errorf("How = %q; want the source named and no credential", a.How)
		}
		if a.Vars["PI_CODING_AGENT_DIR"] != dir {
			t.Errorf("Vars[PI_CODING_AGENT_DIR] = %q; want the agent dir the entry was found under", a.Vars["PI_CODING_AGENT_DIR"])
		}
		if _, ok := a.Vars["ANTHROPIC_API_KEY"]; ok {
			t.Error("Vars carries the provider key with no key in the environment")
		}
	})
	t.Run("another provider's entry is no entry", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":{"type":"apiKey"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := piAuthOf(dir, model, func(string) string { return "" })
		if err == nil || !strings.Contains(err.Error(), "no anthropic credential") || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
			t.Fatalf("piAuthOf = %v; want a skip naming anthropic and its environment key", err)
		}
	})
	t.Run("a null entry is no entry", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"anthropic":null}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := piAuthOf(dir, model, func(string) string { return "" }); err == nil {
			t.Fatal("a null auth.json entry authenticated; want the skip")
		}
	})
	t.Run("the environment key authenticates with no entry", func(t *testing.T) {
		dir := t.TempDir()
		env := map[string]string{"ANTHROPIC_API_KEY": secret, "PI_CODING_AGENT_DIR": dir}
		a, err := piAuthOf(dir, model, func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("piAuthOf: %v; want ANTHROPIC_API_KEY to authenticate", err)
		}
		if a.How != "ANTHROPIC_API_KEY" || strings.Contains(a.How, secret) {
			t.Errorf("How = %q; want the key's name and no credential", a.How)
		}
		if a.Vars["ANTHROPIC_API_KEY"] != secret {
			t.Error("Vars does not carry the caller's key into the pane")
		}
	})
	t.Run("another provider's entry does not shadow the environment key", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"openai":{"type":"apiKey"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		a, err := piAuthOf(dir, model, func(k string) string {
			if k == "ANTHROPIC_API_KEY" {
				return secret
			}
			return ""
		})
		if err != nil || a.How != "ANTHROPIC_API_KEY" {
			t.Fatalf("piAuthOf = %q, %v; want the provider's environment key to authenticate", a.How, err)
		}
	})
	t.Run("neither skips naming both places", func(t *testing.T) {
		dir := t.TempDir()
		_, err := piAuthOf(dir, model, func(string) string { return "" })
		if err == nil {
			t.Fatal("piAuthOf authenticated with no auth.json entry and no environment key")
		}
		for _, want := range []string{"no anthropic credential", dir, "auth.json", "ANTHROPIC_API_KEY"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the skip line %q does not name %q", err, want)
			}
		}
	})
	t.Run("no agent directory skips naming it", func(t *testing.T) {
		_, err := piAuthOf("", model, func(string) string { return "" })
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
