package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"obor/internal/app"
	"obor/internal/project"
)

// configFile is app.ConfigFile on the project's main checkout: .obor/config.json and git -C <root>.
type configFile struct{ root, main string }

var _ app.ConfigFile = configFile{}

// configPath is the config file's path in the repo.
const configPath = ".obor/config.json"

func (f configFile) Read(ctx context.Context) ([]byte, string, error) {
	st, err := project.GitCtx(ctx, f.root, "status", "--porcelain", "--", configPath)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(st) != "" {
		return nil, configPath + " has uncommitted changes in " + f.root + ": commit or discard them first", nil
	}
	b, err := project.GitCtx(ctx, f.root, "branch", "--show-current")
	if err != nil {
		return nil, "", err
	}
	if b = strings.TrimSpace(b); b != f.main {
		return nil, fmt.Sprintf("the main checkout %s is on %s, not %s", f.root, cmpDash(b), f.main), nil
	}
	doc, err := os.ReadFile(filepath.Join(f.root, configPath))
	if err != nil {
		return nil, "", err
	}
	return doc, "", nil
}

func (f configFile) Commit(ctx context.Context, doc []byte, msg string) (string, error) {
	path := filepath.Join(f.root, configPath)
	old, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := project.WriteAtomic(path, doc); err != nil {
		return "", err
	}
	if _, err := project.GitCtx(ctx, f.root, "commit", "-q", "--only", "-m", msg, "--", configPath); err != nil {
		_ = project.WriteAtomic(path, old) // the file is as it was: nothing half done
		return "", err
	}
	sha, err := project.GitCtx(ctx, f.root, "rev-parse", "HEAD")
	return strings.TrimSpace(sha), err
}

// cmpDash is s, "no branch" for "" (a detached head).
func cmpDash(s string) string {
	if s == "" {
		return "no branch"
	}
	return s
}
