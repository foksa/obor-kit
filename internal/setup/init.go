package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"obor/internal/config"
	"obor/internal/project"
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

// reviewsDir is paths.reviews (default docs/reviews): where the reviewer writes its open reviews (T-731).
func (s *Setup) reviewsDir() string {
	if r := s.cfg.Paths().Reviews; r != "" {
		return r
	}
	return "docs/reviews"
}

// reviewsIgnore is the .gitignore line for the reviews dir, a directory line: a broad `git add -A` never stages an open
// review, which `obor review close` refuses (T-731).
func (s *Setup) reviewsIgnore() string { return strings.Trim(s.reviewsDir(), "/") + "/" }

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
	for _, t := range s.initTemplates() {
		if err := s.copyTemplate(t.src, t.rel, t.stub); err != nil {
			return err
		}
	}
	return nil
}

// initTemplate is one doc init copies: the kit's template src to rel under the root, stub as copyTemplate's.
type initTemplate struct {
	src, rel string
	stub     bool
}

// initTemplates are the docs init copies: BACKLOG.md, docs/decisions.md and the agents dir's (README.md is the kit's
// overview, read in place).
func (s *Setup) initTemplates() []initTemplate {
	agents := s.agentsDir()
	tpl := filepath.Join(s.Kit, "templates")
	var out []initTemplate
	for _, c := range [][2]string{{"BACKLOG.md", "BACKLOG.md"}, {"decisions.md", "docs/decisions.md"},
		{"task-spec.md", agents + "/task-spec.md"}} {
		out = append(out, initTemplate{filepath.Join(tpl, c[0]), c[1], false})
	}
	entries, _ := os.ReadDir(filepath.Join(tpl, "agents"))
	for _, e := range entries {
		if n := e.Name(); strings.HasSuffix(n, ".md") && n != "README.md" {
			out = append(out, initTemplate{filepath.Join(tpl, "agents", n), agents + "/" + n, true})
		}
	}
	return out
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
	doc, err := s.configJSON(name, prefix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return project.Failf("cannot create %s", filepath.Dir(path))
	}
	if err := project.WriteAtomic(path, doc); err != nil {
		return project.Failf("cannot write %s", path)
	}
	s.wrote = true
	s.say("created .obor/config.json (name %s, prefix %s, no lanes)", name, prefix)
	return nil
}

// configJSON is the .obor/config.json init writes: the kit's example with name, prefix, the lanes dir beside the root
// and no lanes, commands, lane links or protected files; the example's difficulty and kinds go (initDifficulty writes
// the levels from the kinds init detected, RV-88 #1), but for each kit kind's command (config.KitKinds: OpenCode's
// binary, T-1046).
func (s *Setup) configJSON(name, prefix string) ([]byte, error) {
	example, err := os.ReadFile(filepath.Join(s.Kit, "templates", "config.example.json"))
	if err != nil {
		return nil, project.Failf("cannot read %s", filepath.Join(s.Kit, "templates", "config.example.json"))
	}
	str := func(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
	for _, kv := range []struct {
		key string
		val json.RawMessage
	}{
		{"name", str(name)}, {"prefix", str(prefix)}, {"lanes_dir", str("../" + filepath.Base(s.root) + "-wt")},
		{"lanes", json.RawMessage(`0`)},
		{"commands", json.RawMessage(`{"lane_setup":null,"check":null,"check_milestone":null,"baseline":null,"gate":null}`)},
		{"lane_links", json.RawMessage(`[]`)}, {"protected_files", json.RawMessage(`[]`)},
	} {
		if example, err = config.EditJSON(example, []string{kv.key}, kv.val); err != nil {
			return nil, project.Failf("cannot read templates/config.example.json: %v", err)
		}
	}
	for _, key := range []string{"difficulty", "kinds"} {
		if example, err = config.EditJSON(example, []string{key}, nil); err != nil {
			return nil, project.Failf("cannot read templates/config.example.json: %v", err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(config.KitKinds)) {
		if example, err = config.EditJSON(example, []string{"kinds", name, "command"}, config.KitKinds[name]); err != nil {
			return nil, project.Failf("cannot read templates/config.example.json: %v", err)
		}
	}
	doc, err := indentJSON(example)
	if err != nil {
		return nil, project.Failf("cannot read templates/config.example.json: %v", err)
	}
	return doc, nil
}

// gitignore appends the lines obor needs, each once (.obor/drafts/: the lead's spec drafts, T-440; paths.reviews: an
// open review, T-731). lane_links too: obor serve cleans the scratch with git clean, which keeps only ignored files.
func (s *Setup) gitignore() error { return s.ignoreLines(s.gitignoreLines()...) }

// ignoreLines appends lines to .gitignore, each once. A line already there with or without its trailing slash (git
// ignores the directory either way) is not added again (T-731).
func (s *Setup) ignoreLines(lines ...string) error {
	path := filepath.Join(s.root, ".gitignore")
	text, _ := os.ReadFile(path)
	have := map[string]bool{}
	for _, l := range strings.Split(string(text), "\n") {
		have[strings.TrimRight(l, "/")] = true
	}
	var add []string
	for _, l := range lines {
		if k := strings.TrimRight(l, "/"); l != "" && !have[k] {
			have[k] = true
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

// gitignoreLines are the lines init puts in .gitignore.
func (s *Setup) gitignoreLines() []string {
	return append([]string{".obor/state/", ".obor/drafts/", ".obor/local.json", ".task-run", s.reviewsIgnore()},
		s.cfg.Data.LaneLinks...)
}

// initBranch is the branch gitInit makes: branches.main from a config in dir, else main.
func (s *Setup) initBranch() string {
	if cfg, err := config.Load(s.Dir); err == nil && cfg.Data.Branches.Main != "" {
		return cfg.Data.Branches.Main
	}
	return "main"
}

// gitInit makes dir a repository when it is in none: branch is branches.main from a config already there, else main
// (T-629).
func (s *Setup) gitInit() error {
	branch := s.initBranch()
	if _, err := project.Git(s.Dir, "init", "-q", "-b", branch); err != nil {
		return project.Failf("git init -b %s in %s", branch, s.Dir)
	}
	s.say("git init -b %s in %s", branch, s.Dir)
	return nil
}

// autoCommit reports whether this init makes the "obor init" commit (T-631): root's branch has no commit and root
// holds nothing but .git, or only files init writes, each byte-equal to init's text (a rerun after an interrupted
// init). No commit and anything else in root: FAIL, before init writes anything (init never commits or touches the
// user's files). A repository with a commit: false. repo: root is a git repository (else a plain directory).
func (s *Setup) autoCommit(root string, repo bool) (bool, error) {
	if repo {
		if _, err := project.Git(root, "rev-parse", "-q", "--verify", "HEAD"); err == nil {
			return false, nil
		}
	}
	s.root = root
	want, err := s.initFiles()
	if err != nil {
		return false, err
	}
	if !onlyFiles(root, want) {
		branch := s.initBranch()
		if repo {
			if b, err := project.Git(root, "symbolic-ref", "--short", "HEAD"); err == nil && strings.TrimSpace(b) != "" {
				branch = strings.TrimSpace(b)
			}
		}
		return false, project.Failf("%s has no commit yet and %s has files: commit what belongs in the project, then "+
			"obor init again", branch, root)
	}
	return true, nil
}

// initFiles is what init writes into an empty root, by path relative to it, for the config there (.obor/config.json,
// .gitignore, the docs); empty when root has no config yet (init writes it first). It loads that config into s.cfg.
// It must follow configJSON's inputs (today name, prefix, the root's base name): a config needing more changes it.
func (s *Setup) initFiles() (map[string][]byte, error) {
	want := map[string][]byte{}
	if !s.configExists() {
		return want, nil
	}
	cfg, err := config.Load(s.root)
	if err != nil {
		return want, nil // not init's config: a user's file
	}
	s.cfg = cfg
	if want[".obor/config.json"], err = s.configJSON(cfg.Data.Name, cfg.Data.Prefix); err != nil {
		return nil, err
	}
	want[".gitignore"] = []byte(strings.Join(s.gitignoreLines(), "\n") + "\n")
	for _, t := range s.initTemplates() {
		if want[t.rel], err = s.renderTemplate(t.src, t.stub); err != nil {
			return nil, err
		}
	}
	return want, nil
}

// onlyFiles reports whether root holds nothing but .git, the directories of want's paths and want's files with
// exactly want's bytes (each optional).
func onlyFiles(root string, want map[string][]byte) bool {
	errStray := errors.New("stray")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == ".git":
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case d.IsDir():
			for w := range want {
				if strings.HasPrefix(w, rel+"/") {
					return nil
				}
			}
			return errStray
		}
		b, ok := want[rel]
		if !ok || !d.Type().IsRegular() {
			return errStray
		}
		if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, b) {
			return errStray
		}
		return nil
	})
	return err == nil
}

// initCommit makes the "obor init" commit when auto (autoCommit): init's own files, the commit the lanes branch from
// (T-629). committed is what it committed, nil when it made no commit.
func (s *Setup) initCommit(auto bool) (committed []string, err error) {
	if !auto {
		return nil, nil
	}
	want, err := s.initFiles()
	if err != nil {
		return nil, err
	}
	for rel := range want {
		if _, err := os.Stat(filepath.Join(s.root, rel)); err == nil {
			committed = append(committed, rel)
		}
	}
	if len(committed) == 0 {
		return nil, nil
	}
	sort.Strings(committed)
	if _, err := project.Git(s.root, append([]string{"add", "--"}, committed...)...); err != nil {
		return nil, project.Failf("git add %s", strings.Join(committed, " "))
	}
	args := append([]string{"-C", s.root, "commit", "-q", "-m", "obor init", "--"}, committed...)
	cmd := exec.CommandContext(context.Background(), "git", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cleanup := project.Group(cmd)
	defer cleanup()
	if err := cmd.Run(); err != nil {
		return nil, project.Failf("git commit -m obor init: %s", lastLine(out.String(), err))
	}
	return committed, nil
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

// copyTemplate writes the template file to rel under the root (renderTemplate), unless rel exists.
func (s *Setup) copyTemplate(src, rel string, stub bool) error {
	dest := filepath.Join(s.root, rel)
	if _, err := os.Stat(dest); err == nil {
		s.say("skip %s (exists)", rel)
		return nil
	}
	text, err := s.renderTemplate(src, stub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return project.Failf("cannot create %s", filepath.Dir(dest))
	}
	if err := os.WriteFile(dest, text, 0o644); err != nil {
		return project.Failf("cannot write %s", dest)
	}
	s.wrote = true
	s.say("created %s", rel)
	return nil
}

// renderTemplate is the text copyTemplate writes for the template src: placeholders filled from the loaded config;
// stub: only the project's short role doc (T-204): the kit text's title, a pointer line to it and its "## This
// project" section.
func (s *Setup) renderTemplate(src string, stub bool) ([]byte, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, project.Failf("cannot read %s", src)
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
	return []byte(strings.TrimRight(text, "\n") + "\n"), nil
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

// indentJSON is the JSON document indented by 2 (as jq --indent 2), with a final newline.
func indentJSON(doc []byte) ([]byte, error) {
	var b bytes.Buffer
	if err := json.Indent(&b, doc, "", "  "); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}

// writeJSON writes the JSON document indented by 2 (indentJSON), atomically.
func writeJSON(path string, doc []byte) error {
	b, err := indentJSON(doc)
	if err != nil {
		return err
	}
	return project.WriteAtomic(path, b)
}
