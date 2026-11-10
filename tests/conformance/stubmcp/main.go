// Command stubmcp is the conformance suite's stand-in for `obor` (deep dive §6): the live tier builds it into a
// scratch kit as bin/obor-stub, behind a bin/obor shell wrapper, so a driver's real start argv (claude, codex, opencode)
// starts it where it would start obor. It writes what it saw and, as an MCP server, answers enough to come up:
//
//	obor-stub mcp [label…]   record {pid, ppid, cwd, argv, env}; then a stdio MCP server: initialize, tools/list, ping
//	obor-stub hook <name>    record the same plus the hook's stdin; exit 0, no output
//
// Records go to <the binary's dir>/../out/<mode>-<name>-<pid>.json: a Codex MCP server gets a stripped environment, so
// the output path cannot come from a variable.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stubmcp:", err)
		os.Exit(1)
	}
	out := filepath.Join(filepath.Dir(exe), "..", "out")
	if len(os.Args) > 2 && os.Args[1] == "hook" {
		stdin, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err := write(out, newRecord("hook", os.Args[2], string(stdin))); err != nil {
			fmt.Fprintln(os.Stderr, "stubmcp:", err)
		}
		return
	}
	if err := write(out, newRecord("mcp", strings.Join(os.Args[min(2, len(os.Args)):], "-"), "")); err != nil {
		fmt.Fprintln(os.Stderr, "stubmcp:", err)
	}
	if err := serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "stubmcp:", err)
		os.Exit(1)
	}
}

// Record is what one run of the stub saw. The live tier reads it back (tests/conformance).
type Record struct {
	Mode  string            `json:"mode"` // mcp or hook
	Name  string            `json:"name"` // the hook's name; an MCP server's labels (the args after mcp, joined by -)
	PID   int               `json:"pid"`
	PPID  int               `json:"ppid"`
	Cwd   string            `json:"cwd"`
	Argv  []string          `json:"argv"`
	Env   map[string]string `json:"env"`
	Stdin string            `json:"stdin,omitempty"`
}

func newRecord(mode, name, stdin string) Record {
	cwd, _ := os.Getwd()
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return Record{Mode: mode, Name: name, PID: os.Getpid(), PPID: os.Getppid(), Cwd: cwd, Argv: os.Args, Env: env, Stdin: stdin}
}

// write puts r in dir as <mode>-<name>-<pid>.json, through a temporary file so a reader never sees half of it.
func write(dir string, r Record) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	name := r.Mode
	if r.Name != "" {
		name += "-" + safe(r.Name)
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", name, r.PID))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// safe is s with every byte but letters, digits, - and _ as _: a file name part.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// serve answers newline-delimited JSON-RPC from in on out until in ends.
func serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<16), 16<<20)
	for sc.Scan() {
		if b := reply(sc.Bytes()); b != nil {
			if _, err := out.Write(append(b, '\n')); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// reply is the answer to one JSON-RPC line; nil for a notification (no id) or a line that is not JSON-RPC.
func reply(line []byte) []byte {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &req) != nil || len(req.ID) == 0 || string(req.ID) == "null" {
		return nil
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	switch req.Method {
	case "initialize":
		version := req.Params.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		resp["result"] = map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "obor-conformance-stub", "version": "1"},
		}
	case "tools/list":
		resp["result"] = map[string]any{"tools": []any{map[string]any{
			"name":        "ping",
			"description": "conformance stub: answers pong",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		}}}
	case "ping":
		resp["result"] = map[string]any{}
	default:
		resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}
	b, _ := json.Marshal(resp)
	return b
}
