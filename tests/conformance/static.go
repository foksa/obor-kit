//go:build conformance

// The static tier of the agent kinds (deep dive §6): help text, binary strings and on-disk files of the installed CLIs.
// No session, no model.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"obor/internal/agents"
	_ "obor/internal/agents/claude"
	_ "obor/internal/agents/codex"
)

const (
	claudeGo   = "internal/agents/claude/claude.go"
	codexGo    = "internal/agents/codex/codex.go"
	opencodeGo = "kinds/opencode/kind/kind.go" // a plugin kind's binary
)

// kindOf is the registered agents.Kind name (the driver under test).
func kindOf(name string) agents.Kind {
	k, ok := agents.Lookup(name)
	if !ok {
		panic("conformance: no agent kind " + name)
	}
	return k
}

// output is the combined output of bin args, bounded by d.
func output(d time.Duration, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return string(b), err
}

// tool is the installed CLI of kind name: its path and version. ok false: not installed (every id in ids SKIPs,
// naming where).
func tool(name string, ids map[string]string) (path, version string, ok bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		for _, id := range names(ids) {
			report("SKIP", id, name+" is not installed", ids[id])
		}
		return "", "", false
	}
	v, _ := output(15*time.Second, path, "--version")
	version = versionOf(v)
	return path, version, true
}

// binaryHas reports which needles occur in the file at path (symlinks followed), read in chunks.
func binaryHas(path string, needles []string) (map[string]bool, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	found := map[string]bool{}
	longest := 0
	for _, n := range needles {
		longest = max(longest, len(n))
	}
	buf := make([]byte, 0, 4<<20)
	chunk := make([]byte, 4<<20)
	for {
		n, err := f.Read(chunk)
		buf = append(buf, chunk[:n]...)
		for _, needle := range needles {
			if !found[needle] && bytes.Contains(buf, []byte(needle)) {
				found[needle] = true
			}
		}
		if err == io.EOF {
			return found, nil
		}
		if err != nil {
			return found, err
		}
		if keep := longest - 1; len(buf) > keep {
			buf = append(buf[:0], buf[len(buf)-keep:]...)
		}
	}
}

// missing is the needles not in found, in their order.
func missing(needles []string, found map[string]bool) []string {
	var out []string
	for _, n := range needles {
		if !found[n] {
			out = append(out, n)
		}
	}
	return out
}

// helpOption is the text of the option flag in a --help listing: its line and the indented lines after it, up to the
// next option, joined by spaces; "" when no line starts with the flag.
func helpOption(help, flag string) string {
	lines := strings.Split(help, "\n")
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, flag+" ") && !strings.HasPrefix(t, flag+",") && t != flag {
			continue
		}
		text := t
		for _, next := range lines[i+1:] {
			n := strings.TrimSpace(next)
			if n == "" || strings.HasPrefix(n, "-") {
				break
			}
			text += " " + n
		}
		return text
	}
	return ""
}

// hasOption: some line of help lists flag as an option (first word, or after "-x, ").
func hasOption(help, flag string) bool {
	re := regexp.MustCompile(`(?m)^\s*(-\w, )?` + regexp.QuoteMeta(flag) + `\b`)
	return re.MatchString(help)
}

// hasSubcommand: some line of help lists sub as a subcommand (first word on an indented line, maybe "a, b").
func hasSubcommand(help, sub string) bool {
	re := regexp.MustCompile(`(?m)^\s+(\w[\w-]*, )?` + regexp.QuoteMeta(sub) + `(,|\s)`)
	return re.MatchString(help)
}

func claudeStatic() {
	ids := map[string]string{"CC-S1": at(claudeGo, "var efforts") + ", CAP §2.1", "CC-S2": "hooks/hooks.json, CAP §2.3",
		"CC-S3": ".claude-plugin/plugin.json, hooks/hooks.json"}
	path, _, ok := tool("claude", ids)
	if !ok {
		return
	}
	help, err := output(30*time.Second, path, "--help")
	if err != nil {
		report("FAIL", "CC-S1", "claude --help failed: "+firstLine(help+err.Error()), ids["CC-S1"])
	} else {
		ccS1(help, ids["CC-S1"])
	}
	ccS2(path, ids["CC-S2"])
	ccS3(path, ids["CC-S3"])
}

// ccS1: --effort lists exactly the driver's efforts; the flags the driver and the guard use are there.
func ccS1(help, where string) {
	want := kindOf("claude").Efforts()
	effort := helpOption(help, "--effort")
	m := regexp.MustCompile(`\(([^)]*)\)`).FindStringSubmatch(effort)
	var got []string
	if m != nil {
		for _, e := range strings.Split(m[1], ",") {
			got = append(got, strings.TrimSpace(e))
		}
	}
	if !slices.Equal(got, want) {
		report("FAIL", "CC-S1", fmt.Sprintf("--effort lists %q, the driver takes %q", got, want), where)
	} else {
		report("PASS", "CC-S1", "--effort lists exactly "+strings.Join(want, " "), where)
	}
	flags := []string{"--mcp-config", "--strict-mcp-config", "--settings", "--setting-sources", "--resume", "--session-id", "--print", "--permission-mode", "--plugin-dir"}
	var gone []string
	for _, f := range flags {
		if !hasOption(help, f) {
			gone = append(gone, f)
		}
	}
	if !regexp.MustCompile(`(?m)^\s*-p, --print\b`).MatchString(help) {
		gone = append(gone, "-p (short for --print)")
	}
	where = at(claudeGo, "func (kind) Start") + ", " + at(claudeGo, "func (kind) Headless") + ", CAP §2.1"
	if len(gone) > 0 {
		report("FAIL", "CC-S1", "claude --help no longer lists "+strings.Join(gone, ", "), where)
		return
	}
	report("PASS", "CC-S1", "claude --help lists "+strings.Join(flags, ", ")+" and -p", where)
}

// ccS2: every event hooks/hooks.json names is a hook event of the installed claude. The hooks doc is online; the
// binary's strings are the offline proxy for its event list (an event name is a quoted string in its bundle).
func ccS2(path, where string) {
	b, err := os.ReadFile("hooks/hooks.json")
	if err != nil {
		report("FAIL", "CC-S2", "hooks/hooks.json: "+err.Error(), where)
		return
	}
	var f struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Hooks) == 0 {
		report("FAIL", "CC-S2", "hooks/hooks.json names no hook events", where)
		return
	}
	events := names(f.Hooks)
	if fi, err := os.Stat(path); err != nil || fi.Size() < 1<<20 {
		report("SKIP", "CC-S2", "claude at "+path+" is no single binary to read the event names from", where)
		return
	}
	var needles []string
	for _, e := range events {
		needles = append(needles, `"`+e+`"`)
	}
	found, err := binaryHas(path, needles)
	if err != nil {
		report("FAIL", "CC-S2", "reading the claude binary: "+err.Error(), where)
		return
	}
	if gone := missing(needles, found); len(gone) > 0 {
		report("FAIL", "CC-S2", "hook events hooks/hooks.json names but the claude binary does not: "+strings.Join(gone, ", "), where)
		return
	}
	report("PASS", "CC-S2", "the claude binary names every hook event hooks/hooks.json uses: "+strings.Join(events, ", ")+
		" (binary strings, not the online hooks doc)", where)
}

// ccS3: the installed claude accepts this kit's plugin: `.claude-plugin/plugin.json` and hooks/hooks.json validate. Run
// from the repo root (toRepoRoot), so "." is the plugin root.
func ccS3(path, where string) {
	o, err := output(60*time.Second, path, "plugin", "validate", ".")
	if err != nil {
		report("FAIL", "CC-S3", "claude plugin validate: "+firstLine(o+err.Error()), where)
		return
	}
	if !strings.Contains(o, "Validation passed") {
		report("FAIL", "CC-S3", "claude plugin validate printed no \"Validation passed\": "+firstLine(o), where)
		return
	}
	report("PASS", "CC-S3", "claude plugin validate accepted the kit's plugin", where)
}

func codexStatic() {
	ids := map[string]string{
		"CX-S1": at(codexGo, "func (kind) Start") + ", CAP §3.1",
		"CX-S2": at(codexGo, "var efforts") + ", CAP §3.1",
		"CX-S3": at(codexGo, "func (kind) LastTurn") + ", CAP §3.4",
	}
	path, _, ok := tool("codex", ids)
	if !ok {
		return
	}
	cxS1(path, ids["CX-S1"])
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".codex")
	}
	cxS2(filepath.Join(home, "models_cache.json"), ids["CX-S2"])
	cxS3(filepath.Join(home, "sessions"), ids["CX-S3"])
}

// cxS1: the flags and subcommands obor and the guard use, and the -c keys obor sets, in the codex binary.
func cxS1(path, where string) {
	help, err := output(30*time.Second, path, "--help")
	if err != nil {
		report("FAIL", "CX-S1", "codex --help failed: "+firstLine(help+err.Error()), where)
		return
	}
	var gone []string
	for _, f := range []string{"-m", "-c", "--no-daemon", "--strict-config"} {
		if !hasOption(help, f) {
			gone = append(gone, f)
		}
	}
	for _, s := range []string{"resume", "exec"} {
		if !hasSubcommand(help, s) {
			gone = append(gone, "subcommand "+s)
		}
	}
	if len(gone) > 0 {
		report("FAIL", "CX-S1", "codex --help no longer lists "+strings.Join(gone, ", "), where)
	} else {
		report("PASS", "CX-S1", "codex --help lists -m, -c, --no-daemon, --strict-config, resume, exec", where)
	}
	keys := []string{"default_tools_approval_mode", "check_for_update_on_startup", "env_vars", "model_reasoning_effort"}
	found, err := binaryHas(path, keys)
	where = at(codexGo, "mcp_servers.obor.command") + ", CAP §3.2"
	switch gone := missing(keys, found); {
	case err != nil:
		report("FAIL", "CX-S1", "reading the codex binary: "+err.Error(), where)
	case len(gone) > 0:
		report("FAIL", "CX-S1", "the codex binary lacks the -c keys obor sets: "+strings.Join(gone, ", "), where)
	default:
		report("PASS", "CX-S1", "the codex binary has the -c keys obor sets: "+strings.Join(keys, ", "), where)
	}
}

// cxS2: the efforts of the built-in (listed) models in Codex's models cache ⊆ the driver's efforts ⊆ their union.
func cxS2(cache, where string) {
	b, err := os.ReadFile(cache)
	if err != nil {
		report("SKIP", "CX-S2", "no models cache to read ("+err.Error()+")", where)
		return
	}
	var f struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
			Levels     []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		report("FAIL", "CX-S2", cache+" is not the models cache this check reads: "+err.Error(), where)
		return
	}
	efforts := kindOf("codex").Efforts()
	union := map[string]bool{}
	var unknown []string
	listed := 0
	for _, m := range f.Models {
		if m.Visibility != "list" {
			continue
		}
		listed++
		for _, l := range m.Levels {
			union[l.Effort] = true
			if !slices.Contains(efforts, l.Effort) {
				unknown = append(unknown, m.Slug+":"+l.Effort)
			}
		}
	}
	var unused []string
	for _, e := range efforts {
		if !union[e] {
			unused = append(unused, e)
		}
	}
	switch {
	case listed == 0:
		report("FAIL", "CX-S2", cache+" lists no built-in models", where)
	case len(unknown) > 0:
		report("FAIL", "CX-S2", "built-in models take efforts the driver refuses: "+strings.Join(unknown, ", "), where)
	case len(unused) > 0:
		report("FAIL", "CX-S2", "the driver takes efforts no built-in model has: "+strings.Join(unused, ", "), where)
	default:
		report("PASS", "CX-S2", fmt.Sprintf("the %d built-in models' efforts are exactly the driver's %s", listed, strings.Join(efforts, " ")), where)
	}
}

// cxS3: the newest real rollout: its first line is a session for the driver, LastTurn finds a turn in it, and a
// token_count's rate_limits window has the fields the driver reads.
func cxS3(sessions, where string) {
	var newest string
	var newestAt time.Time
	_ = filepath.WalkDir(sessions, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(newestAt) {
			newest, newestAt = p, fi.ModTime()
		}
		return nil
	})
	if newest == "" {
		report("SKIP", "CX-S3", "no rollout under "+sessions, where)
		return
	}
	b, err := os.ReadFile(newest)
	if err != nil {
		report("FAIL", "CX-S3", err.Error(), where)
		return
	}
	k := kindOf("codex")
	first, _, _ := bytes.Cut(b, []byte("\n"))
	s, okSession := k.Session(first)
	_, okTurn := k.LastTurn(b)
	window := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() && !window {
		var l struct {
			Type    string `json:"type"`
			Payload struct {
				Type       string `json:"type"`
				RateLimits *struct {
					Primary *struct {
						UsedPercent *float64 `json:"used_percent"`
						ResetsAt    *int64   `json:"resets_at"`
					} `json:"primary"`
				} `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.Type != "event_msg" || l.Payload.Type != "token_count" {
			continue
		}
		if r := l.Payload.RateLimits; r != nil && r.Primary != nil && r.Primary.UsedPercent != nil && r.Primary.ResetsAt != nil {
			window = true
		}
	}
	name := filepath.Base(newest)
	var bad []string
	if !okSession {
		bad = append(bad, "line 1 is no session_meta Session reads")
	}
	if !okTurn {
		bad = append(bad, "LastTurn finds no turn")
	}
	if !window {
		bad = append(bad, "no token_count rate_limits.primary {used_percent, resets_at}")
	}
	if len(bad) > 0 {
		report("FAIL", "CX-S3", "newest rollout "+name+": "+strings.Join(bad, "; "), where)
		return
	}
	report("PASS", "CX-S3", "newest rollout "+name+": session "+s.ID+", a last turn, a rate_limits window", where)
}

func opencodeStatic() {
	ids := map[string]string{
		"OC-S1": at(opencodeGo, "func start(") + ", " + at(opencodeGo, `"headless"`) + ", CAP §4.1",
		"OC-S2": "opencode/obor.js, CAP §4.3",
	}
	path, _, ok := tool("opencode", ids)
	if !ok {
		return
	}
	help, err := output(30*time.Second, path, "--help")
	if err != nil {
		report("FAIL", "OC-S1", "opencode --help failed: "+firstLine(help+err.Error()), ids["OC-S1"])
	} else {
		ocS1(help, ids["OC-S1"])
	}
	ocS2(ids["OC-S2"])
}

// ocS1: the TUI's flags obor uses, still no --model (the driver's StartEnv path for the model), and `run` (the guard).
func ocS1(help, where string) {
	flags, _, _ := strings.Cut(help, "GLOBAL FLAGS") // the TUI command's own flags
	var gone []string
	for _, f := range []string{"--standalone", "--auto", "--prompt", "--session"} {
		if !hasOption(flags, f) {
			gone = append(gone, f)
		}
	}
	if !hasSubcommand(help, "run") {
		gone = append(gone, "subcommand run")
	}
	if len(gone) > 0 {
		report("FAIL", "OC-S1", "opencode --help no longer lists "+strings.Join(gone, ", "), where)
	} else {
		report("PASS", "OC-S1", "opencode --help lists --standalone, --auto, --prompt, --session and run", where)
	}
	where = at(opencodeGo, "func start(") + ", CAP §4.1"
	if hasOption(flags, "--model") || hasOption(flags, "-m") {
		report("WARN", "OC-S1", "the TUI command takes --model now: the start env's plugin path for the model can retire", where)
		return
	}
	report("PASS", "OC-S1", "the TUI command still takes no --model (the model goes through the start's env and obor.js)", where)
}

// ocS2: node parses obor.js and its unit tests pass.
func ocS2(where string) {
	node, err := exec.LookPath("node")
	if err != nil {
		report("SKIP", "OC-S2", "node is not installed", where)
		return
	}
	if o, err := output(30*time.Second, node, "--check", "opencode/obor.js"); err != nil {
		report("FAIL", "OC-S2", "node --check opencode/obor.js: "+firstLine(o+err.Error()), where)
		return
	}
	o, err := output(120*time.Second, node, "--test", "opencode/obor.test.js")
	if err != nil {
		report("FAIL", "OC-S2", "node --test opencode/obor.test.js: "+lastLines(o, 3), where)
		return
	}
	report("PASS", "OC-S2", "node --check opencode/obor.js and node --test opencode/obor.test.js: "+testCount(o), where)
}

// lastLines is the last n non-empty lines of s, joined by " / ".
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-n):], " / ")
}

// testCount is node --test's "pass N" summary line ("" when there is none).
func testCount(o string) string {
	for _, l := range strings.Split(o, "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "ℹ")); strings.HasPrefix(l, "pass ") {
			return l
		}
	}
	return "passed"
}
