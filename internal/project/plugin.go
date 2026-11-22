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

// InboxFile is slot's inbox record file under the checkout at root: <root>/.obor/state/inbox/<slot>, its "/" a "."
// (L-03/drive → L-03.drive), mode 0600 (it holds the session's messaging token). Keyed by the project and the slot,
// never by an agent name: two projects with one prefix keep apart, and a rename leaves it where it is. InboxFiles are
// the checkouts a record may be in.
func InboxFile(root, slot string) string {
	return filepath.Join(StateDir(root), "inbox", strings.ReplaceAll(slot, "/", "."))
}

// InboxFiles are the files slot's inbox record may be in, in cfg's project: InboxFile of the main checkout, then of the
// worktree the slot's session runs in (its lane; a role worktree) when that is another. A sandboxed agent writes only
// its own workspace, its temp dirs and caches (agy's auto mode: docs/research/agy-probes.md P9), so a command it runs
// records its inbox in its worktree's gitignored state dir when the main checkout refuses the write (WriteInbox).
func InboxFiles(cfg *config.Config, slot string) []string {
	files := []string{InboxFile(cfg.Root, slot)}
	if dir := slotDir(cfg, slot); dir != "" && filepath.Clean(dir) != filepath.Clean(cfg.Root) {
		files = append(files, InboxFile(dir, slot))
	}
	return files
}

// slotDir is the worktree the session holding slot runs in: a lane-bound slot's lane (L-03, L-03/drive), else the
// role's dir (config.RoleDir); "" for a slot cfg does not know.
func slotDir(cfg *config.Config, slot string) string {
	lane, _, _ := strings.Cut(slot, "/")
	if l, ok := cfg.Lane(lane); ok {
		return l.Dir
	}
	if rc, ok := cfg.Role(slot); ok {
		return cfg.RoleDir(rc.Role)
	}
	return ""
}

// InboxAt is the file holding slot's inbox record in cfg's project: of InboxFiles, the one written last (the main
// checkout's on a tie); an error when there is none.
func InboxAt(cfg *config.Config, slot string) (string, error) {
	at, newest, first := "", time.Time{}, error(nil)
	for _, f := range InboxFiles(cfg, slot) {
		fi, err := os.Stat(f)
		switch {
		case err != nil:
			if first == nil {
				first = err
			}
		case at == "" || fi.ModTime().After(newest):
			at, newest = f, fi.ModTime()
		}
	}
	if at == "" {
		return "", first
	}
	return at, nil
}

// RemoveInbox removes slot's inbox record from every file of InboxFiles in cfg's project, best-effort.
func RemoveInbox(cfg *config.Config, slot string) {
	for _, f := range InboxFiles(cfg, slot) {
		_ = os.Remove(f)
	}
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

// Inbox is a session's inbox record: its socket (Claude's CLAUDE_CODE_MESSAGING_SOCKET; a plugin kind's, as its
// deliver call names it), token (CLAUDE_CODE_MESSAGING_TOKEN) and session id, and a plugin kind's own record as its
// JSON text (the object of its manifest's inbox_record variables), which its deliver call is given back. Sends: the
// kind's deliver sends the text itself (agentapi for agy), no socket; Refused: a send with this record was refused
// (why), so it is not live until an obor command of the session records it again.
type Inbox struct {
	Socket  string `json:"socket"`
	Token   string `json:"token,omitempty"`
	Session string `json:"session,omitempty"`
	Record  string `json:"record,omitempty"`
	Sends   bool   `json:"sends,omitempty"`
	Refused string `json:"refused,omitempty"`
}

// InboxLive is agent's inbox record in cfg's project when it is live: it names a socket that is there (a session that
// ended removed it; a crashed one's is refused at the dial), or the kind sends with it and no send with it was refused:
// the session that wrote it, ok. No slot, no record, no socket, or a refused send: not ok.
func InboxLive(cfg *config.Config, agent string) (session string, ok bool) {
	slot := AgentSlot(cfg, agent)
	if slot == "" {
		return "", false
	}
	in, err := ReadInbox(cfg, slot)
	if err != nil {
		return "", false
	}
	if in.Sends {
		return in.Session, in.Refused == ""
	}
	_, err = os.Stat(in.Socket)
	return in.Session, err == nil
}

// ReadInbox is the inbox record of slot in cfg's project (the file InboxAt names); an error when there is none or it
// names neither a socket nor the kind's own send.
func ReadInbox(cfg *config.Config, slot string) (Inbox, error) {
	f, err := InboxAt(cfg, slot)
	if err != nil {
		return Inbox{}, err
	}
	return readInboxFile(f)
}

// readInboxFile is the inbox record in file f: ReadInbox's rule on one file.
func readInboxFile(f string) (Inbox, error) {
	var in Inbox
	b, err := os.ReadFile(f)
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return in, err
	}
	if in.Socket == "" && !in.Sends {
		return in, errors.New(f + ": no socket")
	}
	return in, nil
}

// RefuseInbox marks slot's inbox record in cfg's project refused (why) in each file of InboxFiles that still holds
// the record a send was refused with (record, the kind's own; a newer one an obor command wrote since is left as it
// is): not live (InboxLive) until the session records it again.
func RefuseInbox(cfg *config.Config, slot, record, why string) error {
	var first error
	for _, f := range InboxFiles(cfg, slot) {
		in, err := readInboxFile(f)
		if err != nil || in.Record != record {
			continue
		}
		in.Refused = why
		if err := writeInboxFile(f, in); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// WriteInbox records in as slot's inbox in cfg's project: in the main checkout's file, else (a write there refused: a
// sandboxed session) the next file of InboxFiles; an error naming each refused write when none took it.
func WriteInbox(cfg *config.Config, slot string, in Inbox) error {
	var why []string
	for _, f := range InboxFiles(cfg, slot) {
		err := writeInboxFile(f, in)
		if err == nil {
			return nil
		}
		why = append(why, err.Error())
	}
	return errors.New(strings.Join(why, "; "))
}

// writeInboxFile writes in to file f, mode 0600, replacing any record at once (a reader never sees half of it).
func writeInboxFile(f string, in Inbox) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
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

// PluginMarkerFile is <root>/.obor/state/lanes/<lane>.plugin: the opencode plugin's marker (compared with the
// lane row's dispatch time), never in the lane worktree.
func PluginMarkerFile(root, lane string) string {
	return filepath.Join(StateDir(root), "lanes", lane+".plugin")
}

// PluginMarker is the marker an agent's plugin leaves when its context hook runs: it proves the plugin loaded,
// so obor status warns when it is missing (the lane then runs with no obor guard).
// Kind is the agent's kind (one whose hooks run through obor's plugin: agents.HooksPlugin), Version its version when the hook knew one (usually ""), At the epoch it ran.
type PluginMarker struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	At      int64  `json:"at"`
}

// PluginMarkerAt reads the lane's marker in root's state dir (<state dir>/lanes/<lane>.plugin): ok false when
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

// WritePluginMarker records that an agent of kind ran a hook: the marker goes to the main checkout's
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
