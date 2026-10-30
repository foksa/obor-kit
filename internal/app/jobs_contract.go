package app

// The MCP job tools' socket methods: `obor mcp` sends each tool call as one request
// with a Call (Agent, Role, Root from the agent's environment and cwd, Lane and Pane when set, a CallID per tool
// call; Task for task_done). The app answers job_next with the JSON of a JobReply as the reply line, task_done with
// "submitted" or the refusal.
const (
	MethodJobNext  = "job_next"  // the caller's open job, "none", or superseded by a newer one
	MethodTaskDone = "task_done" // the worker's report submits its task (row 16)
)

// JobReply is job_next's answer. State: "job" (the fields are set), "none", or "superseded" (Current names the
// caller's newer job id).
type JobReply struct {
	State   string `json:"state"`
	Job     string `json:"job,omitempty"`
	Task    string `json:"task,omitempty"`
	Attempt int    `json:"attempt,omitempty"`
	Step    string `json:"step,omitempty"`
	Text    string `json:"text,omitempty"`
	Current string `json:"current,omitempty"`
}
