package launch

import (
	"os"
	"strings"

	"obor/internal/agents"
)

// startArgs is the argv after the agent's own, as its kind builds it (agents.Kind.Start): the type's args, the kind's
// settings and registration of the obor MCP server (`obor mcp`) for kit, then the session id obor chose (a kind that
// names its own, T-813) or the resume form, or the job's wake as the CLI's first prompt. The prompt is one line before
// it joins the args (T-444: herdr refuses an agent.start argument with a newline).
func startArgs(k agents.Kind, s Start, kit, session string) []string {
	return k.Start(agents.Start{Kit: kit, Args: s.Args, Resume: s.Resume, Session: session, Prompt: oneLine(s.Prompt),
		Read: os.ReadFile})
}

// oneLine is text with every run of newlines (and \r) one space: herdr refuses an agent.start argument with a newline
// ("agent arguments cannot be encoded safely for the target shell", T-444), and a typed job's text may be many lines.
// The text is otherwise unchanged.
func oneLine(text string) string {
	var b strings.Builder
	gap := false
	for _, r := range text {
		if r == '\n' || r == '\r' {
			gap = true
			continue
		}
		if gap {
			b.WriteByte(' ')
			gap = false
		}
		b.WriteRune(r)
	}
	if gap {
		b.WriteByte(' ')
	}
	return b.String()
}
