package herdr

// Sent is every request method this client sends to herdr and, for each, the params it puts in the request. It is the
// one list of what obor depends on in herdr's API: the conformance suite (tests/conformance, H-S1) checks it against
// `herdr api schema --json`, and TestSentCoversCallSites keeps it in step with the call sites in this package. A
// call that sends a new method or param adds it here in the same change.
var Sent = map[string][]string{
	"ping":              nil,
	"session.snapshot":  nil,
	"agent.list":        nil,
	"agent.get":         {"target"},
	"agent.prompt":      {"target", "text"},
	"agent.send_keys":   {"target", "keys"},
	"agent.rename":      {"target", "name"},
	"agent.start":       {"name", "kind", "pane_id", "args"},
	"pane.list":         nil,
	"pane.get":          {"pane_id"},
	"pane.send_keys":    {"pane_id", "keys"},
	"pane.send_input":   {"pane_id", "text", "keys"},
	"pane.read":         {"pane_id", "source", "lines", "strip_ansi"},
	"pane.rename":       {"pane_id", "label"},
	"pane.layout":       {"pane_id"},
	"pane.split":        {"target_pane_id", "direction", "cwd", "env"},
	"pane.close":        {"pane_id"},
	"pane.process_info": {"pane_id"},
	"tab.create":        {"workspace_id", "label", "cwd", "focus", "env"},
	"workspace.create":  {"label", "cwd", "focus"},
	"plugin.list":       nil,
	"plugin.link":       {"path"},
	"plugin.unlink":     {"plugin_id"},
	"notification.show": {"title", "body"},
	"events.subscribe":  {"subscriptions"},
}

// SentSubscriptions is every subscription type events.subscribe carries ("type" in each entry) and the params obor
// puts in that entry besides "type". H-S1 checks them against the schema's Subscription; pane.agent_status_changed
// must keep requiring pane_id.
var SentSubscriptions = map[string][]string{
	"pane.created":              nil,
	"pane.closed":               nil,
	"pane.exited":               nil,
	"pane.updated":              nil,
	"pane.agent_detected":       nil,
	"pane.moved":                nil,
	"workspace.created":         nil,
	"workspace.renamed":         nil,
	"pane.output_matched":       {"pane_id", "source", "match"},
	"pane.agent_status_changed": {"pane_id"},
}
