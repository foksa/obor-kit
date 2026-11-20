package hc

// Sent is every method this client sends to hc and, for each, the params it puts in the request: the one list of what
// obor depends on in hc's API. TestSchemaHasSent checks it against the committed copy of hc 0.1.21's `hc schema`
// (testdata/hc-0.1.21.openrpc.json), and TestSentCoversCallSites keeps it in step with the call sites in this package.
// A call that sends a new method or param adds it here in the same change. hello's method list is checked against it
// at every snapshot (a newer hc without one of them gets a Note).
var Sent = map[string][]string{
	"hello":            {"generation"},
	"server.status":    nil,
	"machine.exec":     {"argv"},
	"terminal.list":    nil,
	"terminal.get":     {"terminal"},
	"terminal.read":    {"terminal", "lines"},
	"events.subscribe": nil,
	"terminal.send":    {"terminal", "text", "force"},
	"terminal.keys":    {"terminal", "keys"},
	"terminal.label":   {"terminal", "labels"},
}

// Read is every result field the client reads, by the schema's component that holds it (its notifications are not
// in the schema: the stream's tests read hc's captured lines instead). TestSchemaHasRead checks it against the same
// copy; Record, Status, Foreground and the answers' structs decode exactly these.
var Read = map[string][]string{
	"HelloResult":     {"generation", "version", "methods"},
	"StatusResult":    {"core"},
	"CoreInfo":        {"id"},
	"ExecResult":      {"code", "stdout"},
	"ListResult":      {"terminals"},
	"SubscribeResult": {"seq", "terminals"},
	"ReadResult":      {"text"},
	"Terminal":        {"address", "state", "reason", "pid", "cwd", "labels", "reported", "status", "foreground"},
	"Status":          {"state", "kind", "msg", "app"},
	"Foreground":      {"pid", "name", "argv", "cwd"},
}
