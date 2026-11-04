//go:build conformance

// Command conformance checks the facts obor's drivers depend on in the tools they drive (deep dive §6). It is a
// program behind the conformance build tag, outside `go test ./...`: run it with
// `make conformance [KIND=herdr|claude|codex|opencode] [TIER=static|live] [TURN=1]`. Each check prints one line,
// `PASS|FAIL|SKIP|WARN <id> <fact> (<driver file:line>, CAP §x)`, and the last line sums them. The exit status is 1
// when any check FAILs. A tool that is not installed, or a check that cannot run (no auth, no TURN), SKIPs with why.
// Every run records the versions it checked in .obor/state/conformance.json (report.go).
//
// Tiers: static (static.go and herdrStatic here: schema, help text, binary strings, files; no session, no model) and
// live (live.go: one throwaway herdr session per kind, the agents started interactively in its panes; model calls only
// with TURN=1, one "Reply with exactly OK" per kind).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"obor/internal/herdr"
)

// out is this run's output.
var out = newRun(os.Stdout)

// report prints one check line.
func report(status, id, fact, where string) { out.report(status, id, fact, where) }

// at is "file:line" of the first line in file (relative to the repo root) containing needle; the bare file when none.
func at(file, needle string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return file
	}
	for i, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, needle) {
			return file + ":" + strconv.Itoa(i+1)
		}
	}
	return file
}

// kinds are the kinds the suite checks, in the order a run without KIND takes them.
var kinds = []string{"herdr", "claude", "codex", "opencode"}

func main() {
	kind, tier, turn := os.Getenv("KIND"), os.Getenv("TIER"), os.Getenv("TURN") == "1"
	if tier == "" {
		tier = "static"
	}
	if err := toRepoRoot(); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL conformance:", err)
		os.Exit(2)
	}
	run := kinds
	if kind != "" {
		run = nil
		for _, k := range kinds {
			if k == kind {
				run = []string{k}
			}
		}
		if run == nil {
			report("SKIP", "kind", "no checks for kind "+kind+" (kinds: "+strings.Join(kinds, " ")+")", "tests/conformance")
		}
	}
	switch tier {
	case "static":
		for _, k := range run {
			static[k]()
		}
	case "live":
		live(run, turn)
	default:
		report("SKIP", "tier", "no tier "+tier+" (tiers: static, live)", "tests/conformance")
	}
	fmt.Println(out.summary())
	if err := out.save(versionsFile, tier, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "conformance: versions file not written:", err)
	}
	if out.failed() {
		os.Exit(1)
	}
}

// static is each kind's static tier.
var static = map[string]func(){"herdr": herdrStatic, "claude": claudeStatic, "codex": codexStatic, "opencode": opencodeStatic}

// toRepoRoot moves to the directory holding go.mod, so the driver files named in the lines resolve.
func toRepoRoot() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return os.Chdir(d)
		}
		if filepath.Dir(d) == d {
			return fmt.Errorf("no go.mod above %s", dir)
		}
	}
}

// schema is the part of `herdr api schema --json` the checks read.
type schema struct {
	Protocol int `json:"protocol"`
	Schemas  struct {
		Request struct {
			OneOf []struct {
				Properties struct {
					Method struct {
						Const string `json:"const"`
					} `json:"method"`
					Params struct {
						Ref string `json:"$ref"`
					} `json:"params"`
				} `json:"properties"`
			} `json:"oneOf"`
			Defs map[string]def `json:"$defs"`
		} `json:"request"`
		Success struct {
			Defs map[string]def `json:"$defs"`
		} `json:"success_response"`
	} `json:"schemas"`
}

// def is a JSON schema object: its property names and required ones (oneOf: the variants).
type def struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	OneOf      []def                      `json:"oneOf"`
}

func (d def) requires(name string) bool {
	for _, r := range d.Required {
		if r == name {
			return true
		}
	}
	return false
}

func herdrStatic() {
	const sock = "internal/herdr/socket.go"
	path, err := exec.LookPath("herdr")
	if err != nil {
		out.begin("herdr", "")
		report("SKIP", "H-S1", "herdr is not installed", sock+", CAP §5.1")
		report("SKIP", "H-S2", "herdr is not installed", at(sock, "func checkProtocol")+", CAP §5.1")
		return
	}
	version := herdrVersion(path)
	out.begin("herdr", version)
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(path, "api", "schema", "--json")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		report("FAIL", "H-S1", "herdr api schema --json failed: "+strings.TrimSpace(firstLine(stderr.String()+err.Error())), sock+", CAP §5.1")
		return
	}
	var s schema
	if err := json.Unmarshal(stdout.Bytes(), &s); err != nil || len(s.Schemas.Request.OneOf) == 0 {
		report("FAIL", "H-S1", "herdr api schema --json is not the schema obor reads (no request methods)", sock+", CAP §5.1")
		return
	}
	methodParams(s, sock)
	subscriptions(s, sock)
	protocol(s, version)
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func herdrVersion(path string) string {
	b, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimPrefix(strings.TrimSpace(string(b)), "herdr ")
}

func names[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// methodParams is H-S1 for requests: each method in herdr.Sent is in the schema and takes every param obor sends.
func methodParams(s schema, sock string) {
	params := map[string]string{} // method → its params def name
	for _, m := range s.Schemas.Request.OneOf {
		params[m.Properties.Method.Const] = strings.TrimPrefix(m.Properties.Params.Ref, "#/schemas/request/$defs/")
	}
	for _, method := range names(herdr.Sent) {
		where := at("internal/herdr/contract.go", `"`+method+`":`) + ", CAP §5.1"
		ref, ok := params[method]
		if !ok {
			report("FAIL", "H-S1", "method "+method+" is not in herdr's schema", where)
			continue
		}
		d := s.Schemas.Request.Defs[ref]
		var missing []string
		for _, p := range herdr.Sent[method] {
			if _, ok := d.Properties[p]; !ok {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			report("FAIL", "H-S1", fmt.Sprintf("method %s has no param %s in %s", method, strings.Join(missing, ", "), ref), where)
			continue
		}
		report("PASS", "H-S1", fmt.Sprintf("method %s takes %s", method, paramList(herdr.Sent[method])), where)
	}
}

func paramList(p []string) string {
	if len(p) == 0 {
		return "no params"
	}
	return strings.Join(p, ", ")
}

// subscriptions is H-S1 for events.subscribe: each type obor subscribes to exists with its params, and
// pane.agent_status_changed still requires pane_id (the stream subscribes one pane at a time).
func subscriptions(s schema, sock string) {
	variants := map[string]def{}
	if sub, ok := s.Schemas.Request.Defs["Subscription"]; ok {
		for _, v := range sub.OneOf {
			var ty struct {
				Const string `json:"const"`
			}
			_ = json.Unmarshal(v.Properties["type"], &ty)
			variants[ty.Const] = v
		}
	}
	for _, typ := range names(herdr.SentSubscriptions) {
		where := at("internal/herdr/contract.go", `"`+typ+`":`) + ", CAP §5.1"
		v, ok := variants[typ]
		if !ok {
			report("FAIL", "H-S1", "subscription type "+typ+" is not in herdr's schema", where)
			continue
		}
		var missing []string
		for _, p := range herdr.SentSubscriptions[typ] {
			if _, ok := v.Properties[p]; !ok {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			report("FAIL", "H-S1", fmt.Sprintf("subscription %s has no param %s", typ, strings.Join(missing, ", ")), where)
			continue
		}
		if typ == "pane.agent_status_changed" && !v.requires("pane_id") {
			report("FAIL", "H-S1", "subscription pane.agent_status_changed no longer requires pane_id: the per-pane subscribe is outdated", where)
			continue
		}
		report("PASS", "H-S1", fmt.Sprintf("subscription %s takes %s", typ, paramList(herdr.SentSubscriptions[typ])), where)
	}
}

// protocol is H-S2: the schema's protocol against obor's, and the capabilities herdr declares in ping's answer. A
// newer protocol is a WARN (herdr ignores unknown fields on both sides; H-S1 guards what obor sends), an older one a FAIL.
func protocol(s schema, version string) {
	where := at("internal/herdr/socket.go", "func checkProtocol") + ", CAP §5.1"
	var caps []string
	if c, ok := s.Schemas.Success.Defs["ServerCapabilities"]; ok {
		caps = names(c.Properties)
	}
	fact := fmt.Sprintf("herdr %s speaks protocol %d (obor needs >= %d); capabilities: %s", version, s.Protocol, herdr.Protocol, paramList(caps))
	switch {
	case s.Protocol < herdr.Protocol:
		report("FAIL", "H-S2", fact, where)
	case s.Protocol > herdr.Protocol:
		report("WARN", "H-S2", fact+"; newer than obor's, accepted: H-S1 guards what obor sends, read herdr's release notes for the rest", where)
	default:
		report("PASS", "H-S2", fact, where)
	}
}
