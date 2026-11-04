package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"obor/internal/config"
	"obor/internal/roles"
)

// InboxFile is where the SessionStart hook records the Claude session inbox of the session holding slot in the
// project at root (T-485, DD-consolidation I4): <root>/.obor/state/inbox/<slot>, its "/" a "." (L-03/drive →
// L-03.drive), mode 0600 (it holds the session's messaging token). Keyed by the project and the slot, never by an agent
// name: two projects with one prefix keep apart, and a rename leaves it where it is. obor serve posts there.
func InboxFile(root, slot string) string {
	return filepath.Join(StateDir(root), "inbox", strings.ReplaceAll(slot, "/", "."))
}

// InboxSlot is the slot a session of role holds, its inbox record's key: the role (lead, reviewer …), a worker's lane
// (L-03), the owner's drive agent's lane/drive (L-03/drive); "" for none (no role, a role cfg does not know, a
// lane-bound role without its lane).
func InboxSlot(cfg *config.Config, role Role) string {
	switch {
	case role.Role == roles.Driver:
		if role.Lane == "" {
			return ""
		}
		return role.Lane + "/drive"
	case roles.LaneBound(role.Role):
		return role.Lane
	}
	if cfg == nil {
		return ""
	}
	if _, ok := cfg.Role(role.Role); !ok {
		return ""
	}
	return role.Role
}

// AgentSlot is the slot whose derived agent name (config.Self) under cfg is agent: InboxSlot of that role and lane;
// "" when no slot's is.
func AgentSlot(cfg *config.Config, agent string) string {
	if cfg == nil || agent == "" {
		return ""
	}
	for _, l := range cfg.Lanes() {
		for _, role := range []string{"worker", roles.Driver} {
			if cfg.Self(role, l.Name) == agent {
				return InboxSlot(cfg, Role{Role: role, Lane: l.Name})
			}
		}
	}
	for _, rc := range cfg.Roles() {
		if !roles.LaneBound(rc.Role.Name) && cfg.Self(rc.Role.Name, "") == agent {
			return rc.Role.Name
		}
	}
	return ""
}

// Inbox is a Claude session's inbox record: its socket (CLAUDE_CODE_MESSAGING_SOCKET), token
// (CLAUDE_CODE_MESSAGING_TOKEN) and session id.
type Inbox struct {
	Socket  string `json:"socket"`
	Token   string `json:"token,omitempty"`
	Session string `json:"session,omitempty"`
}

// ReadInbox is the inbox record of slot in root's project; an error when there is none or it names no socket.
func ReadInbox(root, slot string) (Inbox, error) {
	var in Inbox
	f := InboxFile(root, slot)
	b, err := os.ReadFile(f)
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, err
	}
	if in.Socket == "" {
		return in, errors.New(f + ": no socket")
	}
	return in, nil
}

// WriteInbox records in as slot's inbox in root's project, mode 0600, replacing any record at once (a reader never
// sees half of it).
func WriteInbox(root, slot string, in Inbox) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	f := InboxFile(root, slot)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), "."+filepath.Base(f)+".*")
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
