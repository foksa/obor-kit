package rex

// Sent is every method this client calls and, for each, the params it puts in the call: the one list of what obor
// depends on in Rex's API. TestSchemaHasSent checks it against the committed copy of `rex api describe --json` for
// these methods (testdata/rex-api-18.describe.json, API revision 18), and TestSentCoversCallSites keeps it in step with
// the call sites in this package. A block method (Terminal + ".process") takes its block's session_id and block_id and
// its own params under args (blockParams). A call that sends a new method or param adds it here in the same change.
var Sent = map[string][]string{
	"server.version":             nil,
	"server.status":              nil,
	"session.list":               nil,
	"session.list_blocks":        {"session_id"},
	Terminal + ".process":        {"session_id", "block_id", "args"},
	Terminal + ".program_status": {"session_id", "block_id", "args"},
}

// Read is every result field the client reads, by method and the path to the object that holds it ("" the result
// itself; "records[]" each element of records). TestSchemaHasRead checks it against the same copy; Session, Block,
// Process, Processes, Record and the answers' structs decode exactly these.
var Read = map[string]map[string][]string{
	"server.version":      {"": {"rex_version", "api_revision"}},
	"server.status":       {"": {"instance_id"}},
	"session.list":        {"": {"sessions"}, "sessions[]": {"session_id", "label"}},
	"session.list_blocks": {"": {"blocks"}, "blocks[]": {"block_id", "label", "creator_name"}},
	Terminal + ".process": {"": {"child", "foreground", "ancestors"}, "child": {"pid", "name", "path"},
		"foreground": {"pid", "name", "path", "cwd"}, "ancestors[]": {"pid", "name", "path"}},
	Terminal + ".program_status": {"": {"records"}, "records[]": {"id", "state", "kind", "msg", "app", "owner"},
		"records[].owner": {"pid"}},
}

// The stream's event types: no schema has them (`rex api describe` and help.all describe methods and block types
// only), so they are named here from the probe's recorded stream (docs/research/rex-probes.md R1 and R2;
// testdata/events.jsonl). A top-level line is one of the first three or a client line; a session_event wraps one of
// the session's events, a block_event one block's named event.
const (
	EventSessionCreated   = "session_created"
	EventSessionDestroyed = "session_destroyed"
	EventSession          = "session_event"

	EventBlock        = "block_event"
	EventBlockCreated = "block_created"
	EventBlockClosed  = "block_closed"
	EventViewChanged  = "session_view_changed" // a block's relabel sends only this
	EventLabelChanged = "session_label_changed"

	EventStatusChanged  = "program_status_changed" // payload {id, record, reason}; the root's id is ""
	EventStatusRemoved  = "program_status_removed" // payload {ids, reason}; the root is ""
	EventProcessChanged = "process_changed"        // payload {generation, foreground, ancestors}
	EventChildExited    = "child_exited"           // payload {exit_code}

	EventClientJoined = "client_joined"
)

// clientEvents are the client lines the stream drops before anything reads them: under --all every CLI call (each of
// this client's own) adds a client_joined and a client_left, and one that attaches to a session a client_connected and
// a client_disconnected in it.
var clientEvents = []string{EventClientJoined, "client_left", "client_connected", "client_disconnected", "client_connection_changed"}
