package mcp

import (
	"context"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"obor/internal/app/wire"
)

// The tools' arguments: each schema is derived from its type (schemaOf: a field without omitempty is required, a
// jsonschema tag is its description), and the SDK checks a call's arguments against it before the handler runs.
type (
	jobNextIn struct {
		Job string `json:"job,omitempty" jsonschema:"the job id the wake names ([obor job <id>]): that job when it is open for you; omitted, your oldest open job"`
	}
	taskDoneIn struct {
		Report string `json:"report" jsonschema:"your final report: Summary, Self-check, Skipped, Open questions (Concerns optional), each a Name: label"`
	}
	acceptIn struct {
		Patch string `json:"patch" jsonschema:"the candidate's patch hash (the review job names it)"`
		Note  string `json:"note,omitempty" jsonschema:"a note for the merge log"`
	}
	sendbackIn struct {
		Why string `json:"why" jsonschema:"why, for the worker"`
	}
	rejectIn struct {
		Why string `json:"why" jsonschema:"why, for the log"`
	}
	holdIn struct {
		Why string `json:"why" jsonschema:"the decision the owner must make"`
	}
	resolvedIn struct{}
)

// register adds the role's tools to srv: the worker fetches its job and submits, the integrator fetches and gives
// verdicts, every other role gets nothing. Each tool's fill copies its arguments into the call (an empty required one
// is the tool's refusal); the verdicts key on the job job_next fetched last.
func (s *server) register(srv *sdk.Server) {
	jobNext := func(in jobNextIn, c *wire.Call) error {
		c.Job = in.Job // the wake's job id (T-602): that job when it is open, else superseded{current}
		return nil
	}
	const jobNextDesc = "Your open obor job: its id, task, attempt, step and prompt text. Call it when a wake names a " +
		"job; the same job comes back until a newer one replaces it."
	switch s.role() {
	case "worker":
		add(srv, s, wire.MethodJobNext, jobNextDesc, jobNext)
		add(srv, s, wire.MethodTaskDone, "Submit your task's work, your final report its argument (the review job quotes "+
			"it). A refusal names what is missing.", func(in taskDoneIn, c *wire.Call) error {
			c.Report = in.Report // serve resolves the lane to its task (T-439)
			return required("report", in.Report)
		})
	case "integrator":
		add(srv, s, wire.MethodJobNext, jobNextDesc, jobNext)
		add(srv, s, wire.MethodAccept, "The integrator's verdict: the candidate whose patch hash you name may be published.",
			func(in acceptIn, c *wire.Call) error {
				c.Task, c.Job = s.fetched()
				c.Patch, c.Note = in.Patch, in.Note
				return required("patch", in.Patch)
			})
		add(srv, s, wire.MethodSendback, "The integrator's verdict: back to the worker with your notes.",
			func(in sendbackIn, c *wire.Call) error { return s.why(in.Why, c) })
		add(srv, s, wire.MethodReject, "The integrator's verdict: drop the candidate.",
			func(in rejectIn, c *wire.Call) error { return s.why(in.Why, c) })
		add(srv, s, wire.MethodResolved, "The integrator's verdict: the conflict is merged in the scratch, gate it again.",
			func(_ resolvedIn, c *wire.Call) error {
				c.Task, c.Job = s.fetched()
				return nil
			})
		add(srv, s, wire.MethodHold, "The integrator's verdict: the owner must decide; the job stays open.",
			func(in holdIn, c *wire.Call) error { return s.why(in.Why, c) })
	}
}

// add registers one tool, its name its app socket method (jobs_contract.go, verdict.go): the app's reply (or refusal)
// is the tool's text (or error).
func add[In any](srv *sdk.Server, s *server, name, desc string, fill func(In, *wire.Call) error) {
	sdk.AddTool(srv, &sdk.Tool{Name: name, Description: desc, InputSchema: schemaOf[In]()},
		func(_ context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
			text, err := s.call(name, func(c *wire.Call) error { return fill(in, c) })
			if err != nil {
				return nil, nil, err
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil, nil
		})
}

// schemaOf is In's schema, kept as before the SDK (T-877): open, so an argument the tool does not know is ignored (a
// newer client's), and with its properties even when it has none ({}).
func schemaOf[In any]() *jsonschema.Schema {
	sc, err := jsonschema.For[In](nil)
	if err != nil {
		panic(err) // the argument types above: structs of strings
	}
	sc.AdditionalProperties = nil
	if sc.Properties == nil {
		sc.Properties = map[string]*jsonschema.Schema{}
	}
	return sc
}

// why is a sendback, reject or hold: the fetched job, the why its note.
func (s *server) why(why string, c *wire.Call) error {
	c.Task, c.Job = s.fetched()
	c.Note = why
	return required("why", why)
}

// required refuses an empty required argument (the schema lets "" through).
func required(name, v string) error {
	if v == "" {
		return fmt.Errorf("the %q argument is required", name)
	}
	return nil
}
