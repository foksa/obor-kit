//go:build conformance

// The live tier's real-server case (T-898). The lab's kit runs stubmcp where a kit runs obor: that proves how each CLI
// starts and wires an MCP server, not that obor's own `obor mcp` works with a real client. These cases point one start
// per kind at the repository's own obor (built in the scratch dir; a kit's bin/obor shim in front of it), ask the agent
// to call job_next once, and pass only when the server's rendered reply reaches the session's transcript. The lab's
// project is the same scratch checkout with a stub serve on its app socket, so no real serve is needed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"obor/internal/app/wire"
)

// The job the stub serve answers job_next with. The prompt names neither, so only the tool result can carry them.
const (
	mcpPrompt  = "Call the obor job_next tool once, then reply DONE."
	mcpJob     = "T-1/1/work"
	mcpJobHead = "JOB " + mcpJob + " (task T-1, attempt 1, step work)"
	mcpMarker  = "obor-conformance: job_next reached the real obor MCP server"
)

// realKit builds the repository's own obor as a kit in the scratch dir: bin/obor (the kit's shim) runs bin/obor-real
// (CONF_OBOR_BIN copies a binary already built, T-898's pre-SDK run; unset, ./cmd/obor is built) and opencode/ holds
// the shipped plugin, so every driver's registration points at the real `obor mcp` and the plugin's hooks run the same
// binary. The kit is built once per lab.
func (l *lab) realKit() (string, error) {
	if l.real == "" && l.realErr == nil {
		l.realErr = l.makeRealKit()
	}
	return l.real, l.realErr
}

func (l *lab) makeRealKit() error {
	dir := filepath.Join(l.root, "real")
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, filepath.Join(dir, "opencode")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	exe := filepath.Join(bin, "obor-real")
	if src := os.Getenv("CONF_OBOR_BIN"); src != "" {
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("CONF_OBOR_BIN: %v", err)
		}
		if err := os.WriteFile(exe, b, 0o755); err != nil {
			return err
		}
	} else if o, err := output(3*time.Minute, "go", "build", "-o", exe, "./cmd/obor"); err != nil {
		return fmt.Errorf("go build ./cmd/obor: %v: %s", err, o)
	}
	wrapper := "#!/bin/bash\nexec \"$(dirname \"$0\")/obor-real\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "obor"), []byte(wrapper), 0o755); err != nil {
		return err
	}
	for _, f := range []string{"package.json", "obor.js"} {
		b, err := os.ReadFile(filepath.Join("opencode", f))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "opencode", f), b, 0o644); err != nil {
			return err
		}
	}
	l.real = dir
	return nil
}

// stubServe is the lab's stand-in for obor serve on the project's app socket: the real `obor mcp` asks it, and it
// answers job_next with a JobReply naming mcpJob and mcpMarker, every other method with an empty reply (a hook's call
// is not the case's subject). asked records every method, so a FAIL can say whether a client reached the server.
type stubServe struct {
	ln      net.Listener
	mu      sync.Mutex
	methods []string
}

// startStubServe listens on the project's app socket (<main>/.obor/state/serve.sock) until the lab closes.
func (l *lab) startStubServe() error {
	if l.app != nil {
		return nil
	}
	path := wire.SocketPath(l.main)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("the stub serve's socket %s: %v", path, err)
	}
	s := &stubServe{ln: ln}
	go s.run()
	l.app = s
	l.cleanups = append(l.cleanups, func() { ln.Close() })
	return nil
}

func (s *stubServe) run() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.answer(c)
	}
}

func (s *stubServe) answer(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReaderSize(c, 1<<16).ReadBytes('\n')
	if err != nil {
		return
	}
	var req wire.Request
	if json.Unmarshal(line, &req) != nil {
		return
	}
	s.mu.Lock()
	s.methods = append(s.methods, req.Method)
	s.mu.Unlock()
	ans := wire.Answer{}
	if req.Method == wire.MethodJobNext {
		b, _ := json.Marshal(wire.JobReply{State: "job", Job: mcpJob, Task: "T-1", Attempt: 1, Step: "work", Text: mcpMarker})
		ans.Reply = string(b)
	}
	b, _ := json.Marshal(ans)
	_, _ = c.Write(append(b, '\n'))
}

// asked is every method the stub serve was asked, in order.
func (s *stubServe) asked() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

// waitTranscript waits up to d for read's text to carry every needle, returning the last text read: the kind's session
// record (a transcript file, a rollout, opencode's database) is read every 2 s, not every poll's 200 ms.
func waitTranscript(d time.Duration, read func() string, needles ...string) (string, bool) {
	deadline := time.Now().Add(d)
	for {
		text := read()
		all := true
		for _, n := range needles {
			all = all && strings.Contains(text, n)
		}
		if all {
			return text, true
		}
		if time.Now().After(deadline) {
			return text, false
		}
		time.Sleep(2 * time.Second)
	}
}

// mcpFact is a case's PASS/FAIL fact: what the stub serve was asked, the MCP tool names the session's record carries,
// and, on a failure, the end of the transcript.
func (l *lab) mcpFact(what, transcript string, ok bool) string {
	fact := fmt.Sprintf("%s; the stub serve was asked %s; MCP tools in the session record: %s", what,
		orNone(strings.Join(l.app.asked(), " ")), orNone(strings.Join(mcpToolNames(transcript), " ")))
	if !ok {
		fact += "; transcript end: " + tail(transcript, 3)
	}
	return fact
}

var mcpToolRE = regexp.MustCompile(`mcp__[A-Za-z0-9_]+`)

// mcpToolNames are the MCP tool names a session record names, each once (the client's tool list, not a call).
func mcpToolNames(text string) []string {
	seen := map[string]bool{}
	var names []string
	for _, n := range mcpToolRE.FindAllString(text, -1) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}
