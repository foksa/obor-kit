package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"obor/internal/project"
	"obor/internal/version"
)

// checkWait bounds the whole --check handshake: a server that answers nothing is a FAIL line, never a hang.
const checkWait = 10 * time.Second

// Check runs initialize and tools/list against the server, an SDK client on in-memory pipes, and returns the doctor
// line: the role and the tools it lists, as a client would see them (`obor mcp --check`). A broken handshake is the
// error.
func Check(env project.Env) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), checkWait)
	defer cancel()
	clientEnd, serverEnd := sdk.NewInMemoryTransports()
	ss, err := (&server{ctx: ctx, env: env}).sdkServer().Connect(ctx, serverEnd, nil)
	if err != nil {
		return "", err
	}
	defer ss.Close()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "obor-check", Version: version.Version}, nil).
		Connect(ctx, clientEnd, nil)
	if err != nil {
		return "", fmt.Errorf("initialize: %v", err)
	}
	defer cs.Close()
	if name := cs.InitializeResult().ServerInfo.Name; name != "obor" {
		return "", fmt.Errorf("the server calls itself %q", name)
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("tools/list: %v", err)
	}
	role := env.Get("OBOR_ROLE")
	if role == "" {
		role = "-"
	}
	names := make([]string, len(list.Tools))
	for i, t := range list.Tools {
		names[i] = t.Name
	}
	if len(names) == 0 {
		return fmt.Sprintf("MCP OK (%s: no tools)", role), nil
	}
	return fmt.Sprintf("MCP OK (%s: %s)", role, strings.Join(names, ", ")), nil
}
