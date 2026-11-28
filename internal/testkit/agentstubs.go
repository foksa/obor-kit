package testkit

// The agent CLI stubs. obor reads the version of each agent kind by running `<kind> --version` on the environment's PATH
// (launch.CLIVersions), and a test that starts a serve with the developer's PATH would run the installed claude, codex,
// opencode and agy: a CLI killed mid-run leaves files in TMPDIR (opencode extracts its own binary into it), and a
// test's behaviour would depend on what the developer has installed. Isolate puts a stub for every agent kind obor
// drives first on PATH, so a test, and a binary a test starts, finds the stub and never a real CLI.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// agentStubs is Isolate's PATH prefix: one stub per agent kind (Kinds' names) in dir, made once per test binary and
// removed by Run.
var agentStubs struct {
	once sync.Once
	dir  string
	err  error
}

// agentStubDir is the stub dir, made on first use.
func agentStubDir() string {
	agentStubs.once.Do(func() {
		dir, err := os.MkdirTemp("", "hk-agent")
		if err != nil {
			agentStubs.err = err
			return
		}
		agentStubs.dir = dir
		for _, name := range Kinds().Names() {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(agentStub), 0o755); err != nil {
				agentStubs.err = err
				return
			}
		}
	})
	if agentStubs.err != nil {
		panic("testkit: write the agent CLI stubs: " + agentStubs.err.Error())
	}
	return agentStubs.dir
}

// agentStub is a stub agent CLI: it answers nothing and fails, saying why on stderr, so a version read of it has no
// version and a start of it fails at once. A test that needs an answer provides its own executable ahead of the stub.
const agentStub = `#!/bin/sh
echo "obor test: a test ran the agent CLI ${0##*/}: a test's PATH holds stubs, not installed CLIs" >&2
exit 1
`

// stubAgentCLIs puts agentStubDir first on PATH; Isolate calls it, and an Isolate run again keeps the one prefix.
func stubAgentCLIs() {
	dir := agentStubDir()
	if path := os.Getenv("PATH"); !strings.HasPrefix(path, dir+string(os.PathListSeparator)) {
		os.Setenv("PATH", dir+string(os.PathListSeparator)+path)
	}
}
