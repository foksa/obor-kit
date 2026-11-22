//go:build conformance

// The Hook transport contract (docs/agents/kind-protocol.md "Hook transport"), one row per transport: the guard a
// transport runs ends at 0 (allow), 2 (its deny), 1, by a signal, too slowly or not at all, and the transport still
// answers its CLI as the contract says. Static: a stub guard, no CLI, no session.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"obor/kinds/agy/kind"
)

const (
	shimGo     = "bin/obor"                             // the shim's hook path
	opencodeJS = "opencode/obor.js"                     // the OpenCode plugin's guard call
	guardDeny  = "obor: denied by the conformance stub" // the stub guard's own deny line
)

// hookCases are a guard's ends, in the order each row runs them.
var hookCases = []struct{ mode, end string }{
	{"allow", "exit 0"},
	{"deny", "exit 2"},
	{"crash", "exit 1"},
	{"killed", "killed by a signal"},
	{"slow", "no answer in time"},
	{"missing", "not found"},
}

// hookAnswer is what a transport tells its CLI about one guard end: a deny with the reason it gives, or an allow (no
// opinion: the CLI's own permission check decides).
type hookAnswer struct {
	denied bool
	reason string
}

// hookTransport is one kind's hook transport: how to run it with the guard in a case's mode, and what a failure's
// reason must name.
type hookTransport struct {
	id, name, where string
	drive           func(mode string) (hookAnswer, error)
	names           map[string]string
}

// row runs the six cases against the transport and prints one line.
func (t hookTransport) row() {
	var bad []string
	for _, c := range hookCases {
		a, err := t.drive(c.mode)
		want := t.names[c.mode]
		switch {
		case err != nil:
			bad = append(bad, c.end+": "+err.Error())
		case c.mode == "allow":
			if a.denied {
				bad = append(bad, c.end+": denied "+strconv.Quote(a.reason)+"; want an allow")
			}
		case c.mode == "deny":
			if !a.denied || !strings.Contains(a.reason, guardDeny) {
				bad = append(bad, c.end+": "+described(a)+"; want the guard's own line")
			}
		case !a.denied:
			bad = append(bad, c.end+": allowed; want a deny naming the failure")
		case !strings.Contains(a.reason, want):
			bad = append(bad, c.end+": "+described(a)+"; want a deny naming "+strconv.Quote(want))
		}
	}
	if len(bad) > 0 {
		report("FAIL", t.id, t.name+": "+strings.Join(bad, " / "), t.where)
		return
	}
	report("PASS", t.id, t.name+": exit 0 allows, exit 2 denies with its own line, and exit 1, a kill, a slow answer and "+
		"a missing guard deny naming the failure", t.where)
}

// described is answer a, for a failure's line.
func described(a hookAnswer) string {
	if !a.denied {
		return "allowed"
	}
	return "denied " + strconv.Quote(a.reason)
}

// hookTransports is the table: every kind's hook transport.
func hookTransports() {
	shimHookRow()
	agyHookRow()
	opencodeHookRow()
}

// shimHookRow is the shim's row: bin/obor's hook path with a temp kit whose dist/obor is the stub guard, the event's
// cwd this checkout (an obor project, so the no-binary path has a project to deny in).
func shimHookRow() {
	dir, err := os.MkdirTemp("", "obor-conf-hook-shim")
	if err != nil {
		report("FAIL", "HT-S1", "a temp dir: "+err.Error(), shimGo)
		return
	}
	defer os.RemoveAll(dir)
	root, mode := filepath.Join(dir, "kit"), filepath.Join(dir, "mode")
	guard := filepath.Join(root, "dist", "obor")
	bin, err := os.ReadFile("bin/obor")
	if err != nil {
		report("FAIL", "HT-S1", "reading bin/obor: "+err.Error(), shimGo)
		return
	}
	stub := "#!/bin/bash\nm=$(cat " + shq(mode) + ")\n" +
		"[ \"$1\" = has ] && { [ \"$m\" = slow ] && exec /bin/sleep 30; exit 0; }\n" +
		"case \"$m\" in\n" +
		"  deny) echo " + shq(guardDeny) + " >&2; exit 2 ;;\n" +
		"  crash) echo 'obor: the stub crashed' >&2; exit 1 ;;\n" +
		"  killed) kill -9 $$ ;;\n" +
		"esac\n"
	for path, text := range map[string]string{
		filepath.Join(root, "bin", "obor"): string(bin),
		filepath.Join(root, "VERSION"):     "0.0.0-conformance\n",
		guard:                              stub,
	} {
		if err := writeScript(path, text, 0o755); err != nil {
			report("FAIL", "HT-S1", "the kit: "+err.Error(), shimGo)
			return
		}
	}
	cwd, err := tempProject(dir)
	if err != nil {
		report("FAIL", "HT-S1", "a project: "+err.Error(), shimGo)
		return
	}
	event := `{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":` + strconv.Quote(cwd) +
		`,"tool_input":{"command":"ls"}}`
	drive := func(m string) (hookAnswer, error) {
		if err := os.WriteFile(mode, []byte(m+"\n"), 0o644); err != nil {
			return hookAnswer{}, err
		}
		if m == "missing" {
			os.Remove(guard)
			defer writeScript(guard, stub, 0o755)
		}
		o, err := runCmd(time.Minute, dir, []string{"HOME=" + filepath.Join(dir, "home"), "OBOR_NO_SELF_HOST=1",
			"OBOR_HOOK_TIMEOUT=1"}, event, "bash", filepath.Join(root, "bin", "obor"), "hook", "pre-tool-use")
		if err != nil {
			return hookAnswer{}, err
		}
		switch o.code {
		case 0:
			return hookAnswer{}, nil
		case 2:
			return hookAnswer{denied: true, reason: strings.TrimSpace(o.stderr)}, nil
		}
		return hookAnswer{}, fmt.Errorf("exit %d: %s", o.code, firstLine(o.stderr))
	}
	hookTransport{id: "HT-S1", name: "the shim (" + shimGo + " hook <name>)", drive: drive,
		where: at(shimGo, "run_hook()") + ", kind-protocol.md",
		names: map[string]string{"crash": "exit 1", "killed": "exit 137", "slow": "no usable Go binary",
			"missing": "no usable Go binary"}}.row()
}

// agyHookRow is agy's row: `obor kind agy hook`'s transport (kinds/agy/kind.Hook with its Exec), the guard a stub
// script named by OBOR_EXE, the event's cwd this checkout (an obor project).
func agyHookRow() {
	dir, err := os.MkdirTemp("", "obor-conf-hook-agy")
	if err != nil {
		report("FAIL", "HT-S2", "a temp dir: "+err.Error(), agyHookGo)
		return
	}
	defer os.RemoveAll(dir)
	mode, exe := filepath.Join(dir, "mode"), filepath.Join(dir, "obor")
	if err := writeScript(exe, "#!/bin/bash\nm=$(cat "+shq(mode)+")\ncase \"$m\" in\n"+
		"  deny) echo "+shq(guardDeny)+" >&2; exit 2 ;;\n"+
		"  crash) echo 'obor: the stub crashed' >&2; exit 1 ;;\n"+
		"  killed) kill -9 $$ ;;\n"+
		"  slow) exec /bin/sleep 30 ;;\n"+
		"esac\nexit 0\n", 0o755); err != nil {
		report("FAIL", "HT-S2", "the stub guard: "+err.Error(), agyHookGo)
		return
	}
	if err := os.WriteFile(mode, []byte("allow\n"), 0o644); err != nil {
		report("FAIL", "HT-S2", err.Error(), agyHookGo)
		return
	}
	// warm the stub: a platform scans a new file before its first exec
	o, err := runCmd(10*time.Second, dir, nil, "", exe)
	if err != nil || o.code != 0 {
		report("FAIL", "HT-S2", "the stub guard did not run: "+firstLine(o.stderr), agyHookGo)
		return
	}
	cwd, err := tempProject(dir)
	if err != nil {
		report("FAIL", "HT-S2", "a project: "+err.Error(), agyHookGo)
		return
	}
	event := `{"hookEventName":"PreToolUse","conversationId":"c1","cwd":` + strconv.Quote(cwd) +
		`,"toolCall":{"name":"run_command","args":{"CommandLine":"ls"}}}`
	drive := func(m string) (hookAnswer, error) {
		if err := os.WriteFile(mode, []byte(m+"\n"), 0o644); err != nil {
			return hookAnswer{}, err
		}
		guard, bound := exe, 10*time.Second
		switch m {
		case "slow":
			bound = 300 * time.Millisecond
		case "missing":
			guard = filepath.Join(dir, "no-such-obor")
		}
		out := kind.Hook([]byte(event), dir, kind.Exec(guard, bound), kind.InProject, io.Discard)
		var a struct{ Decision, Reason string }
		if err := json.Unmarshal(out, &a); err != nil {
			return hookAnswer{}, fmt.Errorf("answer %q: %v", out, err)
		}
		switch a.Decision {
		case "ask":
			return hookAnswer{}, nil
		case "deny":
			return hookAnswer{denied: true, reason: a.Reason}, nil
		}
		return hookAnswer{}, fmt.Errorf("answer %q: no decision", out)
	}
	hookTransport{id: "HT-S2", name: "obor kind agy hook", drive: drive,
		where: at(agyHookGo, "func toolAnswer") + ", kind-protocol.md",
		names: map[string]string{"crash": "obor hook pre-tool-use exited 1", "killed": "obor hook pre-tool-use exited -1",
			"slow": "no answer within", "missing": "no such file"}}.row()
}

// opencodeHookRow is the OpenCode plugin's row: its guard call (opencode/obor.js, by node) against a stub kit's
// bin/obor (tests/conformance/opencode_guard.mjs).
func opencodeHookRow() {
	node, err := exec.LookPath("node")
	if err != nil {
		report("SKIP", "HT-S3", "node is not installed", opencodeJS+", kind-protocol.md")
		return
	}
	drive := func(mode string) (hookAnswer, error) {
		o, err := runCmd(time.Minute, "", nil, "", node, "tests/conformance/opencode_guard.mjs", mode)
		if err != nil {
			return hookAnswer{}, err
		}
		if o.code != 0 {
			return hookAnswer{}, fmt.Errorf("exit %d: %s", o.code, firstLine(o.stderr+o.stdout))
		}
		var a struct {
			Denied bool   `json:"denied"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(o.stdout), &a); err != nil {
			return hookAnswer{}, fmt.Errorf("answer %q: %v", o.stdout, err)
		}
		return hookAnswer{denied: a.Denied, reason: a.Reason}, nil
	}
	hookTransport{id: "HT-S3", name: "the OpenCode plugin's guard call", drive: drive,
		where: at(opencodeJS, "guard unavailable") + ", kind-protocol.md",
		names: map[string]string{"crash": "guard failed (exit 1", "killed": "killed by SIGKILL", "slow": "timed out",
			"missing": "guard unavailable (no "}}.row()
}

// tempProject makes an obor project (a git repository with .obor/config.json) in dir and returns its root: the
// transports run a guard only in one, and the shim's project check reads git.
func tempProject(dir string) (string, error) {
	root := filepath.Join(dir, "proj")
	if err := os.MkdirAll(filepath.Join(root, ".obor"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, ".obor", "config.json"), []byte("{}\n"), 0o644); err != nil {
		return "", err
	}
	o, err := runCmd(30*time.Second, "", nil, "", "git", "-C", root, "-c", "init.defaultBranch=main", "init", "-q")
	if err != nil {
		return "", err
	}
	if o.code != 0 {
		return "", fmt.Errorf("git init: exit %d: %s", o.code, firstLine(o.stderr))
	}
	return root, nil
}

// runResult is one process's end: its stdout, its stderr and its exit code.
type runResult struct {
	stdout, stderr string
	code           int
}

// runCmd runs bin args… in dir with env added and stdin, bounded by limit. A bare bin name is looked up on PATH, as
// exec.Command does (Cmd.Dir would otherwise resolve it there).
func runCmd(limit time.Duration, dir string, env []string, stdin string, bin string, args ...string) (runResult, error) {
	if !strings.ContainsRune(bin, '/') {
		found, err := exec.LookPath(bin)
		if err != nil {
			return runResult{code: -1}, err
		}
		bin = found
	}
	var stdout, stderr strings.Builder
	cmd := &exec.Cmd{Path: bin, Args: append([]string{bin}, args...), Dir: dir, Env: append(os.Environ(), env...),
		Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr, WaitDelay: 2 * time.Second}
	if err := cmd.Start(); err != nil {
		return runResult{code: -1}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		r := runResult{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			return r, err
		}
		return r, nil
	case <-time.After(limit):
		cmd.Process.Kill()
		<-done
		return runResult{code: -1, stdout: stdout.String(), stderr: stderr.String()},
			fmt.Errorf("no end within %s", limit)
	}
}

// writeScript writes text to path with mode, its directories made.
func writeScript(path, text string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), mode)
}
