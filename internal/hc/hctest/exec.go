package hctest

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// exec.go: machine.exec as obor sends it, `/usr/bin/env` with no terminal, answered with the core's environment.

// cleanEnv is the environment of a core started from a scrubbed shell (docs/research/hc-probes.md P4).
var cleanEnv = map[string]string{"HOME": "/Users/probe", "PATH": "/usr/bin:/bin", "USER": "probe", "SHELL": "/bin/zsh"}

// SetEnv makes env the core's environment: what machine.exec of env prints (nil: a scrubbed core's).
func (c *Core) SetEnv(env map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.env = maps.Clone(env)
}

// exec answers machine.exec (under c.mu): argv /usr/bin/env prints the core's environment, one NAME=VALUE line each,
// exit 0; any other program is a spawn failure, as hc answers a program it cannot start.
func (c *Core) exec(raw json.RawMessage) (any, string, string) {
	var p struct {
		Argv []string `json:"argv"`
	}
	_ = json.Unmarshal(raw, &p)
	if !slices.Equal(p.Argv, []string{"/usr/bin/env"}) {
		return nil, "spawn_failed", "hctest runs only /usr/bin/env"
	}
	env := c.env
	if env == nil {
		env = cleanEnv
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(env)) {
		b.WriteString(k + "=" + env[k] + "\n")
	}
	// stdout is base64 in hc's answer: encoding/json encodes a []byte so.
	return map[string]any{"code": 0, "signal": nil, "stdout": []byte(b.String()), "stderr": []byte{}, "truncated": false,
		"timed_out": false, "ms": 3}, "", ""
}
