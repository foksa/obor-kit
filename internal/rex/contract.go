package rex

// Sent is every method this client calls and, for each, the params it puts in the call: the one list of what obor
// depends on in Rex's API. TestSchemaHasSent checks it against the committed copy of `rex api describe --json` for
// these methods (testdata/rex-api-18.describe.json, API revision 18), and TestSentCoversCallSites keeps it in step with
// the call sites in this package. A block method (Terminal + ".process") takes its block's session_id and block_id and
// its own args (Args) under args (blockParams). A call that sends a new method or param adds it here in the same
// change.
var Sent = map[string][]string{
	"server.version":             nil,
	"server.status":              nil,
	"session.list":               nil,
	"session.list_blocks":        {"session_id"},
	"session.view":               {"session_id"},
	"session.create":             {"label"},
	"session.new_window":         {"session_id", "window_label", "focus", "layout"},
	"session.new_split":          {"session_id", "anchor_id", "window_id", "direction", "side", "focus", "layout"},
	"session.new_block":          {"session_id", "flavor", "options"},
	"session.set_block_label":    {"session_id", "block_id", "label"},
	"session.set_window_label":   {"session_id", "window_id", "label"},
	"session.set_label":          {"session_id", "label"},
	"session.destroy":            {"session_id"},
	"block.close":                {"session_id", "block_id"},
	"block.event":                {"session_id", "block_id", "event"},
	"target.resolve":             {"kinds", "queries"},
	Terminal + ".process":        {"session_id", "block_id", "args"},
	Terminal + ".program_status": {"session_id", "block_id", "args"},
	Terminal + ".format":         {"session_id", "block_id", "args"},
}

// Args is each block method's own args obor sends (a method not here gets {}); Options the shell flavor's options
// obor gives a block it makes (shellOptions: session.new_block's options, a layout's block options), checked against
// the flavor's options_schema in the same copy (block_creator.list); Event the fields of block.event's event
// (inputEvent: a paste).
var (
	Args    = map[string][]string{Terminal + ".format": {"format", "trim", "unwrap"}}
	Options = []string{"shell", "command", "cwd", "exit", "exit.on_completion"}
	Event   = []string{"type", "payload"}
)

// CLI is every rex command line obor runs besides `rex api call <method> '<json>'`, after the global options (-S,
// --autostart=false): the stream (stream.go) and the keys (control.go: `rex send-key` presses keys by Rex's key
// binding names, which block.event's key events would need as physical key codes). No schema has them; the live tier
// runs both.
var CLI = []string{"events --all --json", "send-key -s <session> -b <block> <key>..."}

// Read is every result field the client reads, by method and the path to the object that holds it ("" the result
// itself; "records[]" each element of records). TestSchemaHasRead checks it against the same copy; Session, Block,
// Process, Processes, Record and the answers' structs decode exactly these.
var Read = map[string]map[string][]string{
	"server.version":      {"": {"rex_version", "api_revision"}},
	"server.status":       {"": {"instance_id"}},
	"session.list":        {"": {"sessions"}, "sessions[]": {"session_id", "label"}},
	"session.list_blocks": {"": {"blocks"}, "blocks[]": {"block_id", "label", "creator_name"}},
	"session.view": {"": {"label", "windows"}, "windows[]": {"window_id", "label", "layers"},
		"windows[].layers[]": {"kind", "blocks"}, "windows[].layers[].blocks[]": {"block_id", "label", "creator_name"}},
	"session.create":     {"": {"session_id"}},
	"session.new_window": {"": {"block_ids"}},
	"session.new_split":  {"": {"block_ids"}},
	"session.new_block":  {"": {"block_id"}},
	"target.resolve": {"": {"results"}, "results[]": {"status", "target"},
		"results[].target": {"session_id", "window_id", "label"}},
	Terminal + ".process": {"": {"child", "foreground", "ancestors", "last_exit"}, "child": {"pid", "name", "path"},
		"foreground": {"pid", "name", "path", "argv0", "cwd"}, "ancestors[]": {"pid", "name", "path"},
		"last_exit": {"exit_code"}},
	Terminal + ".program_status": {"": {"records"}, "records[]": {"id", "state", "kind", "msg", "app", "owner"},
		"records[].owner": {"pid"}},
	Terminal + ".format": {"": {"content"}},
}

// The stream's delivery. Rex numbers a connection's events with a seq that has no gaps and says it closes a connection
// that falls 8 MiB of events behind rather than skip one (`rex api describe session.attach_all`, session.bootstrap);
// `rex events --json` prints each event without its seq, so obor cannot see a gap itself and relies on that guarantee.
// Measured on Rex 0.1.0-preview.21 (API revision 18) through the CLI: a burst of 6000 status reports (12 MB) arrived
// whole and in order, and so did the same burst to a reader that read nothing until it had ended (the backlog was held,
// no event dropped, the connection not closed); the API answered meanwhile. A connection Rex closes ends `rex events`,
// which ends the stream as a refresh (stream.go: a new snapshot, a new subscription). The live tier checks it on each
// run (tests/conformance, REX-R4: a burst of reports, `rex events` read only after it ended, arrives whole and in
// order).

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
