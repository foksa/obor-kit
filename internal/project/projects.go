package project

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"time"

	"obor/internal/config"
)

// The machine's obor projects by name (T-646): ${OBOR_STATE_HOME:-$HOME/.obor}/projects.json, {"<name>": "<root>"},
// written by obor serve at its start and by obor init, so obor send and obor projects can name a project instead of
// spelling a path into it.

// Project is an obor project this machine knows: its config name and its main checkout.
type Project struct{ Name, Root string }

// ProjectsFile is the machine's list of obor projects.
func ProjectsFile(env Env) string { return filepath.Join(StateHome(env), "projects.json") }

// NameAt is the name the project at root answers to: its config's name, else the directory's base name (the name obor
// send posts as the sender). ok false when root holds no loadable obor config.
func NameAt(root string) (string, bool) {
	cfg, err := config.LoadDaemon(root)
	if err != nil {
		return "", false
	}
	if name := cfg.Data.Name; name != "" {
		return name, true
	}
	return filepath.Base(root), true
}

// Projects lists the projects this machine knows, in name order: an entry whose root no longer holds a config of that
// name is left out (and dropped at the next write).
func Projects(env Env) []Project { return live(readProjects(env)) }

// live is the entries of list whose project is there under that name, in name order.
func live(list map[string]string) []Project {
	out := []Project{}
	for name, root := range list {
		if n, ok := NameAt(root); ok && n == name {
			out = append(out, Project{Name: name, Root: root})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RootOf is the root of the project this machine knows as name: ok false when it knows none, or the entry is stale.
func RootOf(name string, env Env) (string, bool) {
	for _, p := range Projects(env) {
		if p.Name == name {
			return p.Root, true
		}
	}
	return "", false
}

// Register adds the project at root to the machine's list under its own name, dropping the entries whose project is
// gone: obor serve at its start and obor init write it. It reports whether the list changed (the entry was absent, or
// named another root, or a stale entry went). The read-modify-write holds projects.lock and the file is replaced by a
// rename, so two registers at once both land (T-646).
func Register(root string, env Env) (changed bool, err error) {
	name, ok := NameAt(root)
	if !ok {
		return false, fmt.Errorf("%s holds no obor config to register", root)
	}
	lock := NewLock(filepath.Join(StateHome(env), "projects.lock"))
	ctx, cancel := context.WithTimeout(context.Background(), registerWait)
	defer cancel()
	if err := lock.Wait(ctx, registerWait); err != nil {
		return false, fmt.Errorf("the project list: %v", err)
	}
	defer lock.Unlock()
	before := readProjects(env)
	list := map[string]string{}
	for _, p := range live(before) {
		list[p.Name] = p.Root
	}
	list[name] = Physical(root)
	if maps.Equal(before, list) {
		return false, nil
	}
	return true, writeProjects(env, list)
}

// registerWait bounds a register's wait for the list's lock: a start-up step, never a wait for minutes.
const registerWait = 10 * time.Second

// readProjects is the list as the file has it: empty when there is none, or it does not parse (the next write
// replaces it).
func readProjects(env Env) map[string]string {
	b, err := os.ReadFile(ProjectsFile(env))
	if err != nil {
		return map[string]string{}
	}
	list := map[string]string{}
	if json.Unmarshal(b, &list) != nil {
		return map[string]string{}
	}
	return list
}

// writeProjects replaces the list, atomically: a reader sees the old file or the new one, never a part.
func writeProjects(env Env, list map[string]string) error {
	path := ProjectsFile(env)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(b, '\n'))
}
