// socketprobe (T-182): can a third-party client subscribe to herdr events? Connects to a herdr API socket, sends one
// request line (default: events.subscribe with the given subscriptions), and prints every line it receives with the
// wall clock in ms, until -for elapses. Run it ONLY against a throwaway session's socket (`herdr --session
// hk-probe-…`; AGENTS.md "Tests stub herdr", T-150): it refuses the default session's socket.
//
//	go run ./tests/go/socketprobe -socket <path> -sub '[{"type":"pane.exited"}]' -for 60s
//	go run ./tests/go/socketprobe -socket <path> -req '{"id":"1","method":"pane.list","params":{}}'
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	socket := flag.String("socket", "", "herdr API socket of a throwaway session")
	sub := flag.String("sub", `[{"type":"pane.created"},{"type":"pane.exited"},{"type":"pane.agent_detected"}]`,
		"subscriptions (JSON list) for events.subscribe")
	req := flag.String("req", "", "send this raw request line instead of events.subscribe")
	then := flag.String("then", "", "a second raw request line, sent on the same connection after the first reply")
	thenAfter := flag.Duration("then-after", 0, "wait this long after the first reply before sending -then")
	dur := flag.Duration("for", 30*time.Second, "how long to listen")
	flag.Parse()
	home, _ := os.UserHomeDir()
	if *socket == "" || filepath.Clean(*socket) == filepath.Join(home, ".config", "herdr", "herdr.sock") {
		fmt.Fprintln(os.Stderr, "socketprobe: -socket <throwaway session socket> required (never the default session)")
		os.Exit(2)
	}
	line := *req
	if line == "" {
		var subs []any
		if err := json.Unmarshal([]byte(*sub), &subs); err != nil {
			fmt.Fprintln(os.Stderr, "socketprobe: -sub:", err)
			os.Exit(2)
		}
		b, _ := json.Marshal(map[string]any{"id": "probe-1", "method": "events.subscribe",
			"params": map[string]any{"subscriptions": subs}})
		line = string(b)
	}
	start := time.Now()
	conn, err := net.Dial("unix", *socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "socketprobe: dial:", err)
		os.Exit(1)
	}
	defer conn.Close()
	fmt.Printf("%d connect %dms\n", time.Now().UnixMilli(), time.Since(start).Milliseconds())
	if _, err := conn.Write([]byte(strings.TrimSpace(line) + "\n")); err != nil {
		fmt.Fprintln(os.Stderr, "socketprobe: write:", err)
		os.Exit(1)
	}
	fmt.Printf("%d sent %s\n", time.Now().UnixMilli(), line)
	conn.SetReadDeadline(time.Now().Add(*dur))
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	first := true
	for sc.Scan() {
		fmt.Printf("%d recv %s\n", time.Now().UnixMilli(), sc.Text())
		if first && *then != "" {
			first = false
			go func() {
				time.Sleep(*thenAfter)
				_, err := conn.Write([]byte(strings.TrimSpace(*then) + "\n"))
				fmt.Printf("%d sent %s (%v)\n", time.Now().UnixMilli(), *then, err)
			}()
		}
	}
	fmt.Printf("%d end %v\n", time.Now().UnixMilli(), sc.Err())
}
