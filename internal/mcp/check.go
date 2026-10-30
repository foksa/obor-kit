package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"obor/internal/project"
)

// checkWait bounds the whole --check handshake: a server that answers nothing is a FAIL line, never a hang.
const checkWait = 10 * time.Second

// Check runs initialize and tools/list against a server on its own pipes and returns the doctor line: the role and the
// tools it lists, as a client would see them (`obor mcp --check`). A broken handshake is the error.
func Check(env project.Env) (string, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() {
		_ = Serve(context.Background(), Options{Env: env, In: inR, Out: outW, Err: io.Discard})
		outW.Close() // a server that ends early ends the client's read too
	}()
	defer inW.Close()
	defer outR.Close()
	enc, dec := json.NewEncoder(inW), bufio.NewReader(outR)
	type handshake struct {
		init initResult
		list listResult
		err  error
	}
	ch := make(chan handshake, 1)
	go func() {
		var h handshake
		h.err = ask(enc, dec, 1, "initialize", &h.init)
		if h.err == nil {
			h.err = ask(enc, dec, 2, "tools/list", &h.list)
		}
		ch <- h
	}()
	var h handshake
	select {
	case h = <-ch:
	case <-time.After(checkWait):
		return "", fmt.Errorf("the server answered nothing in %s", checkWait)
	}
	if h.err != nil {
		return "", h.err
	}
	if h.init.ServerInfo.Name != "obor" {
		return "", fmt.Errorf("the server calls itself %q", h.init.ServerInfo.Name)
	}
	role := env.Get("OBOR_ROLE")
	if role == "" {
		role = "-"
	}
	names := make([]string, len(h.list.Tools))
	for i, t := range h.list.Tools {
		names[i] = t.Name
	}
	if len(names) == 0 {
		return fmt.Sprintf("MCP OK (%s: no tools)", role), nil
	}
	return fmt.Sprintf("MCP OK (%s: %s)", role, strings.Join(names, ", ")), nil
}

// ask sends one request and decodes its result into out; an error answer is the error.
func ask(enc *json.Encoder, dec *bufio.Reader, id int, method string, out any) error {
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method}); err != nil {
		return err
	}
	line, err := dec.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("%s: no answer (%v)", method, err)
	}
	var res struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(line, &res); err != nil {
		return fmt.Errorf("%s: unreadable answer (%v)", method, err)
	}
	if res.Error != nil {
		return fmt.Errorf("%s: %s", method, res.Error.Message)
	}
	if len(res.Result) == 0 {
		return errors.New(method + ": no result")
	}
	return json.Unmarshal(res.Result, out)
}
