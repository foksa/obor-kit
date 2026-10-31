package mcp

import (
	"encoding/json"
	"fmt"

	"obor/internal/app"
)

// The tool tables by role: the worker fetches its job and submits, the integrator fetches
// and gives verdicts, every other role gets nothing. A tool's method is its app socket method (jobs_contract.go,
// verdict.go); the tool only renders what the app answers.
var (
	jobNextTool = tool{name: toolJobNext, desc: "Your open obor job: its id, task, attempt, step and prompt text. Call " +
		"it when a wake names a job; the same job comes back until a newer one replaces it.", method: app.MethodJobNext,
		args: []arg{{name: "job", desc: "the job id the wake names ([obor job <id>]): that job when it is open for " +
			"you; omitted, your oldest open job"}}}
	taskDoneTool = tool{name: toolTaskDone, desc: "Submit your task's work, your final report its argument (the " +
		"review job quotes it). A refusal names what is missing.", method: app.MethodTaskDone,
		args: []arg{{name: "report", desc: "your final report: Summary, Self-check, Skipped, Open questions " +
			"(Concerns optional), each a Name: label", required: true}}}
	acceptTool = tool{name: toolAccept, desc: "The integrator's verdict: the candidate whose patch hash you name may be " +
		"published.", method: app.MethodAccept,
		args: []arg{{name: "patch", desc: "the candidate's patch hash (the review job names it)", required: true},
			{name: "note", desc: "a note for the merge log"}}}
	sendbackTool = tool{name: toolSendback, desc: "The integrator's verdict: back to the worker with your notes.",
		method: app.MethodSendback, args: []arg{{name: "why", desc: "why, for the worker", required: true}}}
	rejectTool = tool{name: toolReject, desc: "The integrator's verdict: drop the candidate.",
		method: app.MethodReject, args: []arg{{name: "why", desc: "why, for the log", required: true}}}
	resolvedTool = tool{name: toolResolved, desc: "The integrator's verdict: the conflict is merged in the scratch, " +
		"gate it again.", method: app.MethodResolved}
	holdTool = tool{name: toolHold, desc: "The integrator's verdict: the owner must decide; the job stays open.",
		method: app.MethodHold, args: []arg{{name: "why", desc: "the decision the owner must make", required: true}}}

	workerTools     = []tool{jobNextTool, taskDoneTool}
	integratorTools = []tool{jobNextTool, acceptTool, sendbackTool, rejectTool, resolvedTool, holdTool}
)

// tool is one tool of a role's table: its MCP name, the app socket method it sends, and its string arguments.
type tool struct {
	name   string
	desc   string
	method string
	args   []arg
}

// arg is one argument: required, or optional.
type arg struct {
	name     string
	desc     string
	required bool
}

// toolsFor is a role's table; a role without tools gets none.
func toolsFor(role string) []tool {
	switch role {
	case "worker":
		return workerTools
	case "integrator":
		return integratorTools
	}
	return nil
}

// lookup is the tool t of the role's table.
func lookup(role, name string) (tool, bool) {
	for _, t := range toolsFor(role) {
		if t.name == name {
			return t, true
		}
	}
	return tool{}, false
}

// arguments is the call's string arguments: a required one that is missing, empty or not a string is the error. An
// argument the tool does not know is ignored (a newer client's).
func (t tool) arguments(raw map[string]json.RawMessage) (map[string]string, error) {
	args := make(map[string]string, len(t.args))
	for _, a := range t.args {
		v, ok := raw[a.name]
		if ok && string(v) != "null" {
			var s string
			if err := json.Unmarshal(v, &s); err != nil {
				return nil, fmt.Errorf("the %q argument must be a string", a.name)
			}
			if s != "" || !a.required {
				args[a.name] = s
				continue
			}
		}
		if a.required {
			return nil, fmt.Errorf("the %q argument is required", a.name)
		}
	}
	return args, nil
}

// listResult is tools/list's result.
type listResult struct {
	Tools []toolInfo `json:"tools"`
}

// toolInfo is one tool as tools/list lists it.
type toolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema schema `json:"inputSchema"`
}

// schema is a tool's input schema: an object of string properties.
type schema struct {
	Type       string          `json:"type"`
	Properties map[string]prop `json:"properties"`
	Required   []string        `json:"required,omitempty"`
}

type prop struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// infos renders a table for tools/list: an empty list, never null.
func infos(tools []tool) []toolInfo {
	out := make([]toolInfo, 0, len(tools))
	for _, t := range tools {
		sc := schema{Type: "object", Properties: make(map[string]prop, len(t.args))}
		for _, a := range t.args {
			sc.Properties[a.name] = prop{Type: "string", Description: a.desc}
			if a.required {
				sc.Required = append(sc.Required, a.name)
			}
		}
		out = append(out, toolInfo{Name: t.name, Description: t.desc, InputSchema: sc})
	}
	return out
}
