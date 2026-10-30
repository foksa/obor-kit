package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// SidFile is where the SessionStart hook records an agent's session id: ${OBOR_STATE_HOME:-$HOME/.obor}/agents/<agent>.sid
// (obor restart resumes it).
func SidFile(agent string, env Env) string {
	return filepath.Join(StateHome(env), "agents", agent+".sid")
}

// InboxFile is where the SessionStart hook records an agent's Claude session inbox (T-485): beside its SidFile,
// <agent>.inbox, mode 0600 (it holds the session's messaging token). obor serve posts the lead's lines there.
func InboxFile(agent string, env Env) string {
	return filepath.Join(StateHome(env), "agents", agent+".inbox")
}

// Inbox is a Claude session's inbox record: its socket (CLAUDE_CODE_MESSAGING_SOCKET), token
// (CLAUDE_CODE_MESSAGING_TOKEN) and session id.
type Inbox struct {
	Socket  string `json:"socket"`
	Token   string `json:"token,omitempty"`
	Session string `json:"session,omitempty"`
}

// ReadInbox is agent's inbox record; an error when there is none or it names no socket.
func ReadInbox(agent string, env Env) (Inbox, error) {
	var in Inbox
	b, err := os.ReadFile(InboxFile(agent, env))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, err
	}
	if in.Socket == "" {
		return in, errors.New(InboxFile(agent, env) + ": no socket")
	}
	return in, nil
}

// WriteInbox records in as agent's inbox, mode 0600, replacing any record at once (a reader never sees half of it).
func WriteInbox(agent string, in Inbox, env Env) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	f := InboxFile(agent, env)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), "."+agent+".inbox.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}

// PluginMarkerFile is <root>/.obor/state/lanes/<lane>.plugin (T-310): the opencode plugin's marker (compared with the
// lane row's dispatch time), never in the lane worktree.
func PluginMarkerFile(root, lane string) string {
	return filepath.Join(StateDir(root), "lanes", lane+".plugin")
}

// PluginMarker is the marker an agent's plugin leaves when its context hook runs (T-307): it proves the plugin loaded,
// so obor status warns when it is missing (the lane then runs with no obor guard).
// Kind is the agent's kind (one whose hooks run through obor's plugin: agents.HooksPlugin), Version its version when the hook knew one (usually ""), At the epoch it ran.
type PluginMarker struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	At      int64  `json:"at"`
}

// PluginMarkerAt reads the lane's marker in root's state dir (T-310: <state dir>/lanes/<lane>.plugin): ok false when
// there is none or it is not a marker.
func PluginMarkerAt(root, lane string) (PluginMarker, bool) {
	b, err := os.ReadFile(PluginMarkerFile(root, lane))
	if err != nil {
		return PluginMarker{}, false
	}
	var m PluginMarker
	if json.Unmarshal(b, &m) != nil {
		return PluginMarker{}, false
	}
	return m, true
}

// WritePluginMarker records that an agent of kind ran a hook (T-307, T-310): the marker goes to the main checkout's
// state dir, never the lane worktree, which must stay clean. Best-effort, like the other
// state-dir records: a state dir the session cannot write says nothing.
func WritePluginMarker(root, lane, kind, version string) {
	b, err := json.Marshal(PluginMarker{Kind: kind, Version: version, At: time.Now().Unix()})
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(PluginMarkerFile(root, lane)), 0o755) != nil {
		return
	}
	_ = os.WriteFile(PluginMarkerFile(root, lane), append(b, '\n'), 0o644)
}
