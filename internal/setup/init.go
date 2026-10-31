package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
	"obor/internal/run"
)

// initArgs parses [--name N] [--prefix P].
func initArgs(args []string) (name, prefix string, err error) {
	for len(args) > 0 {
		if len(args) < 2 || args[1] == "" {
			return "", "", errUsage
		}
		switch args[0] {
		case "--name":
			name = args[1]
		case "--prefix":
			prefix = args[1]
		default:
			return "", "", errUsage
		}
		args = args[2:]
	}
	return name, prefix, nil
}

// agentsDir is paths.agents (default docs/agents).
func (s *Setup) agentsDir() string {
	if a := s.cfg.Paths().Get("agents"); a != "" {
		return a
	}
	return "docs/agents"
}

// initProject writes what init writes (config, .gitignore lines, the docs; each only when missing) and loads the
// config. name "": the root's base; prefix "": defaultPrefix of the name.
func (s *Setup) initProject(name, prefix string) error {
	if name == "" {
		name = filepath.Base(s.root)
	}
	if prefix == "" {
		prefix = defaultPrefix(name)
	}
	if !config.PrefixRe.MatchString(prefix) {
		return project.Failf("bad prefix %s (must match %s)", prefix, config.PrefixRe)
	}
	if err := s.writeConfig(name, prefix); err != nil {
		return err
	}
	if err := s.load(); err != nil {
		return err
	}
	if err := s.gitignore(); err != nil {
		return err
	}
	agents := s.agentsDir()
	tpl := filepath.Join(s.Kit, "templates")
	for _, c := range [][2]string{{"BACKLOG.md", "BACKLOG.md"}, {"decisions.md", "docs/decisions.md"},
		{"task-spec.md", agents + "/task-spec.md"}, {"agents/roster.md", agents + "/roster.md"}} {
		if err := s.copyTemplate(filepath.Join(tpl, c[0]), c[1], false); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(filepath.Join(tpl, "agents"))
	for _, e := range entries {
		// README.md is the kit's overview, read in place; roster.md (copied above) is the project's own table.
		if n := e.Name(); strings.HasSuffix(n, ".md") && n != "README.md" && n != "roster.md" {
			if err := s.copyTemplate(filepath.Join(tpl, "agents", n), agents+"/"+n, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// defaultPrefix is the name lower-cased, letters and digits only, no leading digits, at most 4 characters; else obor.
func defaultPrefix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9' && b.Len() > 0) {
			b.WriteRune(r)
		}
	}
	if p := b.String(); p != "" {
		return p[:min(4, len(p))]
	}
	return "obor"
}

// writeConfig writes .obor/config.json from the kit's example unless it exists.
func (s *Setup) writeConfig(name, prefix string) error {
	path := filepath.Join(s.root, ".obor", "config.json")
	if _, err := os.Stat(path); err == nil {
		s.say("skip .obor/config.json (exists)")
		return nil
	}
	example, err := os.ReadFile(filepath.Join(s.Kit, "templates", "config.example.json"))
	if err != nil {
		return project.Failf("cannot read %s", filepath.Join(s.Kit, "templates", "config.example.json"))
	}
	str := func(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
	for _, kv := range []struct {
		key string
		val json.RawMessage
	}{
		{"name", str(name)}, {"prefix", str(prefix)}, {"lanes_dir", str("../" + filepath.Base(s.root) + "-wt")},
		{"lanes", json.RawMessage(`{}`)},
		{"commands", json.RawMessage(`{"lane_setup":null,"check":null,"baseline":null,"gate":null}`)},
		{"lane_links", json.RawMessage(`[]`)}, {"protected_files", json.RawMessage(`[]`)},
	} {
		if example, err = config.EditJSON(example, []string{kv.key}, kv.val); err != nil {
			return project.Failf("cannot read templates/config.example.json: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return project.Failf("cannot create %s", filepath.Dir(path))
	}
	if err := writeJSON(path, example); err != nil {
		return project.Failf("cannot write %s", path)
	}
	s.wrote = true
	s.say("created .obor/config.json (name %s, prefix %s, no lanes)", name, prefix)
	return nil
}

// gitignore appends the lines obor needs, each once (.obor/drafts/: the lead's spec drafts, T-440). lane_links too:
// obor serve cleans the scratch with git clean, which keeps only ignored files.
func (s *Setup) gitignore() error {
	path := filepath.Join(s.root, ".gitignore")
	text, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, l := range strings.Split(string(text), "\n") {
		have[l] = true
	}
	lines := append([]string{".obor/state/", ".obor/drafts/", ".obor/local.json", ".task-run"},
		s.cfg.Data.LaneLinks...)
	var add []string
	for _, l := range lines {
		if l != "" && !have[l] {
			have[l] = true
			add = append(add, l)
		}
	}
	if len(add) == 0 {
		return nil
	}
	if len(text) > 0 && text[len(text)-1] != '\n' {
		text = append(text, '\n')
	}
	text = append(text, strings.Join(add, "\n")+"\n"...)
	if err := os.WriteFile(path, text, 0o644); err != nil {
		return project.Failf("cannot write %s", path)
	}
	s.wrote = true
	for _, l := range add {
		s.say(".gitignore += %s", l)
	}
	return nil
}

// gitInit makes dir a repository when it is in none: branch is branches.main from a config already there, else main
// (T-629).
func (s *Setup) gitInit() error {
	branch := "main"
	if cfg, err := config.Load(s.Dir); err == nil && cfg.Data.Branches.Main != "" {
		branch = cfg.Data.Branches.Main
	}
	if _, err := project.Git(s.Dir, "init", "-q", "-b", branch); err != nil {
		return project.Failf("git init -b %s in %s", branch, s.Dir)
	}
	s.say("git init -b %s in %s", branch, s.Dir)
	return nil
}

// initCommit commits a repository with no commit yet: init's own files as "obor init", the commit the lanes branch from
// (T-629). A repository with a commit is left as it is. Untracked files init did not write: FAIL, init never commits
// the user's files. committed is what it committed, nil when it made no commit.
func (s *Setup) initCommit() (committed []string, err error) {
	if _, err := project.Git(s.root, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
		return nil, nil
	}
	status, err := project.Git(s.root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, project.Failf("cannot read git status in %s", s.root)
	}
	var other []string
	for _, e := range strings.Split(status, "\x00") {
		p, ok := strings.CutPrefix(e, "?? ")
		if !ok {
			continue
		}
		if s.initFile(p) {
			committed = append(committed, p)
		} else {
			other = append(other, p)
		}
	}
	if len(other) > 0 {
		branch, _ := project.Git(s.root, "symbolic-ref", "--short", "HEAD")
		branch = strings.TrimSpace(branch)
		if branch == "" {
			branch = "main"
		}
		return nil, project.Failf("%s has no commit yet and %s has files obor did not write: commit what belongs in the "+
			"project, then obor init again", branch, s.root)
	}
	if len(committed) == 0 {
		return nil, nil
	}
	if _, err := project.Git(s.root, append([]string{"add", "--"}, committed...)...); err != nil {
		return nil, project.Failf("git add %s", strings.Join(committed, " "))
	}
	args := append([]string{"-C", s.root, "commit", "-q", "-m", "obor init", "--"}, committed...)
	cmd := exec.CommandContext(context.Background(), "git", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cleanup := run.Group(cmd)
	defer cleanup()
	if err := cmd.Run(); err != nil {
		return nil, project.Failf("git commit -m obor init: %s", lastLine(out.String(), err))
	}
	return committed, nil
}

// initFile reports whether p (relative to the root) is a file init writes: its config and .gitignore, its docs.
func (s *Setup) initFile(p string) bool {
	switch p {
	case ".obor/config.json", ".gitignore", "BACKLOG.md", "docs/decisions.md":
		return true
	}
	return strings.HasPrefix(p, s.agentsDir()+"/")
}

// lastLine is the last non-empty line of out, else err.
func lastLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return err.Error()
}

// copyTemplate writes the template file to rel under the root, placeholders filled, unless rel exists. stub: only the
// project's short role doc (T-204): the kit text's title, a pointer line to it and its "## This project" section.
func (s *Setup) copyTemplate(src, rel string, stub bool) error {
	dest := filepath.Join(s.root, rel)
	if _, err := os.Stat(dest); err == nil {
		s.say("skip %s (exists)", rel)
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return project.Failf("cannot read %s", src)
	}
	text := string(b)
	if stub {
		title, _, _ := strings.Cut(text, "\n")
		text = title + " — {{project}} specifics\n\nRole text: `templates/agents/" + filepath.Base(src) +
			"` in the obor kit, read in place (the session start names its path). This file\nholds only what differs " +
			"in this project.\n\n" + section(text, "## This project")
	}
	d := s.cfg.Data
	text = strings.NewReplacer("{{project}}", d.Name, "{{prefix}}", d.Prefix, "{{lanes_dir}}", d.LanesDir).Replace(text)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return project.Failf("cannot create %s", filepath.Dir(dest))
	}
	if err := os.WriteFile(dest, []byte(strings.TrimRight(text, "\n")+"\n"), 0o644); err != nil {
		return project.Failf("cannot write %s", dest)
	}
	s.wrote = true
	s.say("created %s", rel)
	return nil
}

// section is the markdown section starting at the line heading, up to the next "## " heading.
func section(text, heading string) string {
	var out []string
	on := false
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "## ") {
			on = l == heading
		}
		if on {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// writeJSON writes the JSON document indented by 2 (as jq --indent 2), atomically.
func writeJSON(path string, doc []byte) error {
	var b bytes.Buffer
	if err := json.Indent(&b, doc, "", "  "); err != nil {
		return err
	}
	b.WriteByte('\n')
	return project.WriteAtomic(path, b.Bytes())
}
