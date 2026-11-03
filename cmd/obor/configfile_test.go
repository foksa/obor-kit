package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"obor/internal/testkit"
)

// T-753: configFile is serve's setup edit on the main checkout: Read refuses a config with uncommitted changes (named)
// and a checkout off main; Commit writes the file and commits that one file on main with the message, a staged
// change of another file left out; a failed commit puts the file back.
func TestConfigFileCommitsOnlyTheConfig(t *testing.T) {
	t.Parallel()
	p := testkit.New(t)
	p.Config()
	p.Git(p.Root, "config", "user.name", "Owner")
	p.Git(p.Root, "config", "user.email", "owner@example.com")
	f := configFile{root: p.Root, main: "main"}
	ctx := context.Background()
	doc, refused, err := f.Read(ctx)
	if err != nil || refused != "" || !strings.Contains(string(doc), `"prefix": "pj"`) {
		t.Fatalf("Read of a clean config: %q, %v (%d bytes)", refused, err, len(doc))
	}

	p.Write("other.txt", "staged\n")
	p.Git(p.Root, "add", "other.txt")
	next := strings.Replace(string(doc), `"prefix": "pj"`, `"prefix": "pj",
  "stop_idle_workers": false`, 1)
	sha, err := f.Commit(ctx, []byte(next), "config: x → y (owner, web)")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if head := p.Git(p.Root, "rev-parse", "HEAD"); head != sha {
		t.Errorf("Commit's sha %s, HEAD %s", sha, head)
	}
	if msg := p.Git(p.Root, "log", "-1", "--format=%s"); msg != "config: x → y (owner, web)" {
		t.Errorf("the commit's message %q", msg)
	}
	if files := p.Git(p.Root, "show", "--name-only", "--format=", "HEAD"); files != ".obor/config.json" {
		t.Errorf("the commit has %q, want only .obor/config.json", files)
	}
	if staged := p.Git(p.Root, "diff", "--cached", "--name-only"); staged != "other.txt" {
		t.Errorf("staged after the commit: %q, want other.txt still staged", staged)
	}
	if got := p.Read(".obor/config.json"); got != next {
		t.Errorf("the file after Commit:\n%s", got)
	}

	// A hand edit not committed: refused, naming the file.
	p.Write(".obor/config.json", next+" ")
	if _, refused, err := f.Read(ctx); err != nil || !strings.Contains(refused, ".obor/config.json has uncommitted changes") {
		t.Errorf("Read of a hand-edited config: %q, %v; want refused", refused, err)
	}
	p.Git(p.Root, "checkout", "-q", "--", ".obor/config.json")

	// A commit that fails (a hook refuses it) leaves the file as it was.
	p.Write(".git/hooks/pre-commit", "#!/bin/sh\necho no >&2\nexit 1\n")
	if err := os.Chmod(p.Path(".git/hooks/pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Commit(ctx, []byte(doc), "config: back"); err == nil {
		t.Fatal("Commit past a refusing pre-commit hook: no error")
	}
	if got := p.Read(".obor/config.json"); got != next {
		t.Errorf("the file after a failed commit:\n%s\nwant it as it was", got)
	}

	// Off the main branch: refused.
	p.Git(p.Root, "checkout", "-q", "-b", "side")
	if _, refused, err := f.Read(ctx); err != nil || !strings.Contains(refused, "is on side, not main") {
		t.Errorf("Read off main: %q, %v; want refused", refused, err)
	}
}
