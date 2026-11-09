package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"obor/internal/agents"
)

// The setup edit (T-753, T-765): the owner changes one setup value from obor web's Setup page (the lead with obor
// nightly on|off, T-781), and serve, the one writer, writes .obor/config.json and commits it. Exactly these keys are
// editable (shape 2's names, T-1126):
//
//	agents.difficulty.<level>   the level's ordered list of model refs (<model>[:<effort>])
//	agents.roles.<role>         a configured role's model ref
//	lanes.count                 how many lanes (L-01 … L-NN; a count replaces lanes.list, T-1031)
//	agents.kinds.<kind>.args    the args appended to every start of a kind
//	nightly.at                  the nightly's switch: a local time "HH:MM" (on, every night then) or null (off)
//
// Any other key stays obor setup's or a hand edit's. A key names one member of shape 2, and so does the write: a file
// of shape 1 is written at the member shape 1 had (change's was), so the one-release window's file keeps its shape.
//
// A key .obor/local.json overrides (the key itself, a member of it, or an ancestor local.json replaces) is refused:
// writing it to config.json would report success and change nothing that runs (T-754).

// Change is one setup edit applied to a config.json: its new bytes (in the file's own layout, Relayout) and the key's
// value before and after, each one line of JSON ("" for a value the config leaves unset).
type Change struct {
	Doc      []byte
	Old, New string
	// Dropped are the lanes a lower lanes count removes: serve refuses the edit while one of them has a task.
	Dropped []string
}

// Rev is a config.json's revision as the Setup page reads it (T-760): the sha256 of its bytes, its first 16 hex
// characters. A setup edit names the revision its page read, and serve refuses it when the file is another one now:
// another tab, the lead or a hand edit changed it since (an edit built from a stale page never undoes theirs).
func Rev(doc []byte) string {
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:8])
}

// EditKeys are the editable keys as patterns, for errors.
const EditKeys = "agents.difficulty.<level>, agents.roles.<role>, lanes.count, agents.kinds.<kind>.args, nightly.at"

// Edit is the change of the setup value at key to val (JSON) in doc, the bytes of c's config.json, and c's local.json
// as it is, an error names why the key is not editable, val not a value it takes (an unknown level, role or kind, a
// model ref naming no model or an effort its kind does not take) or the key is one .obor/local.json overrides (edit it
// there: the committed value would not count). Edit does not validate the whole config: the caller loads the result
// (LoadWith) and runs Validate.
func (c *Config) Edit(doc []byte, key string, val json.RawMessage) (Change, error) {
	parts := strings.Split(key, ".")
	switch {
	case len(parts) == 3 && parts[0] == "agents" && parts[1] == "difficulty":
		return c.editLevel(doc, parts[2], val)
	case len(parts) == 3 && parts[0] == "agents" && parts[1] == "roles":
		return c.editRole(doc, parts[2], val)
	case key == "lanes.count":
		return c.editLanes(doc, val)
	case len(parts) == 4 && parts[0] == "agents" && parts[1] == "kinds" && parts[3] == "args":
		return c.editKindArgs(doc, parts[2], val)
	case key == "nightly.at":
		return c.editNightlyAt(doc, val)
	}
	return Change{}, fmt.Errorf("%s is not an editable key (%s)", Compact(key), EditKeys)
}

// editLevel sets agents.difficulty.<level> to a list of model refs, each naming a model of c.
func (c *Config) editLevel(doc []byte, level string, val json.RawMessage) (Change, error) {
	if _, ok := DifficultyDefault[level]; !ok {
		return Change{}, fmt.Errorf("%s is not a level: hard, medium or simple", Compact(level))
	}
	var refs []string
	if err := json.Unmarshal(val, &refs); err != nil || refs == nil {
		return Change{}, fmt.Errorf("agents.difficulty.%s takes a list of model refs", level)
	}
	for _, ref := range refs {
		if err := c.CheckModelRef(ref); err != nil {
			return Change{}, fmt.Errorf("agents.difficulty.%s: %v", level, err)
		}
	}
	old := Compact(c.DifficultyList(level))
	return c.change("agents.difficulty."+level, doc, []string{"agents", "difficulty", level},
		[]string{"difficulty", level}, nil, refs, old, Compact(refs))
}

// editLanes sets lanes.count (a whole number, 0 or more; it replaces lanes.list, T-1031): the lanes it no longer
// has are in Change.Dropped.
func (c *Config) editLanes(doc []byte, val json.RawMessage) (Change, error) {
	var n int
	if err := json.Unmarshal(val, &n); err != nil || n < 0 {
		return Change{}, fmt.Errorf("lanes.count takes a whole number (L-01 … L-NN)")
	}
	if at := c.localShadow([]string{"lanes", "list"}); at != "" { // a list wins over the count
		return Change{}, localRefusal("lanes.count", at)
	}
	old := Compact(c.Data.Lanes.Count)
	if c.Data.Lanes.List != nil {
		old = Compact(c.Data.Lanes.List)
	}
	ch, err := c.change("lanes.count", doc, []string{"lanes", "count"}, []string{"lanes"},
		[]string{"lanes", "list"}, n, old, Compact(n))
	kept := Lanes{Count: n}.entries()
	for _, e := range c.Data.Lanes.entries() {
		if !slices.ContainsFunc(kept, func(k LaneEntry) bool { return k.Name == e.Name }) {
			ch.Dropped = append(ch.Dropped, e.Name)
		}
	}
	return ch, err
}

// editNightlyAt switches the nightly (T-781): a local time "HH:MM" sets nightly.at (on, every night at it), null
// deletes it (off); anything else is refused.
func (c *Config) editNightlyAt(doc []byte, val json.RawMessage) (Change, error) {
	var at *string
	ok := json.Unmarshal(val, &at) == nil
	if ok && at != nil {
		_, _, ok = ParseClock(*at)
	}
	if !ok {
		return Change{}, fmt.Errorf("nightly.at takes a local time HH:MM (00:00 to 23:59), or null (off)")
	}
	var v any
	newLine := ""
	if at != nil {
		v, newLine = *at, Compact(*at)
	}
	return c.change("nightly.at", doc, []string{"nightly", "at"}, []string{"merge", "nightly_at"}, nil,
		v, unsetOr(c.Data.Nightly.At), newLine)
}

// editKindArgs sets agents.kinds.<kind>.args: a list of args with no bare -- (obor adds the first prompt itself); an empty
// list drops the key.
func (c *Config) editKindArgs(doc []byte, kind string, val json.RawMessage) (Change, error) {
	if _, ok := agents.Lookup(kind); !ok {
		return Change{}, fmt.Errorf("%s is not a kind (kinds: %s)", Compact(kind), strings.Join(agents.Names(), ", "))
	}
	var args []string
	if err := json.Unmarshal(val, &args); err != nil {
		return Change{}, fmt.Errorf("agents.kinds.%s.args takes a list of strings", kind)
	}
	if slices.Contains(args, "--") {
		return Change{}, fmt.Errorf("agents.kinds.%s.args: `--` is not allowed: obor adds the first prompt itself", kind)
	}
	var v any = args
	if len(args) == 0 {
		v = nil
	}
	old := ""
	if cur := c.Data.Agents.Kinds.M[kind].Args; len(cur) > 0 {
		old = Compact(cur)
	}
	newLine := ""
	if len(args) > 0 {
		newLine = Compact(args)
	}
	return c.change("agents.kinds."+kind+".args", doc, []string{"agents", "kinds", kind, "args"},
		[]string{"kinds", kind, "args"}, nil, v, old, newLine)
}

// editRole sets a configured role's model ref; a role always names its model (T-769), so "" and null are refused. A
// custom role's object row keeps its write list: its model is agents.roles.<role>.model (T-801).
func (c *Config) editRole(doc []byte, role string, val json.RawMessage) (Change, error) {
	rc, ok := c.Role(role)
	if !ok || role == "worker" || rc.Role.Optional && !rc.Configured {
		names := []string{}
		for _, rc := range c.RoleAgents() {
			names = append(names, rc.Role.Name)
		}
		return Change{}, fmt.Errorf("%s is not a configured role (roles: %s; a worker's model is its dispatch's)",
			Compact(role), strings.Join(names, ", "))
	}
	var ref string
	if err := json.Unmarshal(val, &ref); err != nil || ref == "" {
		return Change{}, fmt.Errorf("agents.roles.%s takes a model ref (<model>[:<effort>])", role)
	}
	if err := c.CheckModelRef(ref); err != nil {
		return Change{}, fmt.Errorf("agents.roles.%s: %v", role, err)
	}
	key, at := "agents.roles."+role, []string{"agents", "roles", role}
	was := []string{"roles", role}
	if c.Data.Agents.Roles.M[role].Object {
		key, at, was = key+".model", append(at, "model"), append(was, "model")
	}
	return c.change(key, doc, at, was, nil, ref, unsetOr(ModelRef(rc.Type, rc.Effort)), Compact(ref))
}

// change is c's edit of key: doc with the member at path (shape 2's) set to v (nil deletes it) and, when drop is not
// nil, the member at drop deleted too (lanes.count replacing lanes.list, T-1031), laid out as doc is, with the value's
// old and new lines. A doc of shape 1 keeps its shape: there the member is was, the key's shape-1 path, and a write
// replaces what shape 1 had (drop is shape 2's alone). It refuses a member .obor/local.json overrides (localRefusal)
// and one the merged config does not take up: either way the file would be written and what runs would not change.
func (c *Config) change(key string, doc []byte, path, was, drop []string, v any, old, new string) (Change, error) {
	if at := c.localShadow(path); at != "" {
		return Change{}, localRefusal(key, at)
	}
	at := path
	if ShapeOf(doc) < ShapeVersion {
		at, drop = was, nil
	}
	out, err := EditJSON(doc, at, v)
	if err != nil {
		return Change{}, err
	}
	if drop != nil {
		if out, err = EditJSON(out, drop, nil); err != nil {
			return Change{}, err
		}
	}
	ch, err := relaid(doc, out, old, new)
	if err != nil {
		return Change{}, err
	}
	if !c.applied(ch.Doc, path, v) {
		return Change{}, localRefusal(key, strings.Join(path, "."))
	}
	return ch, nil
}

// localRefusal is why the setup edit of key is refused: .obor/local.json sets at, so the committed value would not
// count.
func localRefusal(key, at string) error {
	return fmt.Errorf("%s: set in .obor/local.json at %s: edit it there", key, at)
}

// localShadow is the dotted path c's .obor/local.json sets that keeps a value at path from counting: path itself or a
// member of it, or an ancestor local.json replaces with a value that is no object (null, a list, a scalar). "" when
// local.json leaves path alone (an object it sets on the way merges key by key, so the value at path still counts).
func (c *Config) localShadow(path []string) string {
	cur := json.RawMessage(c.local)
	for i, k := range path {
		t := bytes.TrimSpace(cur)
		if len(t) == 0 {
			return ""
		}
		ms, ok := object(t)
		if !ok {
			return strings.Join(path[:i], ".") // a null, list or scalar here replaces everything below it
		}
		cur = nil
		for _, m := range ms {
			if m.Key == k {
				cur = m.Val
			}
		}
		if cur == nil {
			return ""
		}
	}
	return strings.Join(path, ".")
}

// applied reports whether the merged config, doc (in shape 2: reshaped when it is shape 1, as the load does) with c's
// .obor/local.json, has v at path: what a write to doc would leave running. v nil is the key gone (or null).
func (c *Config) applied(doc []byte, path []string, v any) bool {
	merged := doc
	if ShapeOf(doc) < ShapeVersion {
		var err error
		if merged, err = Reshape(doc); err != nil {
			return false
		}
	}
	if len(c.local) > 0 {
		merged = merge(merged, c.local)
	}
	got, err := MemberAt(merged, path)
	if err != nil {
		return false
	}
	want := "null"
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		want = string(b)
	}
	if got == nil {
		got = []byte("null")
	}
	var b bytes.Buffer // the laid-out file's value against the one line v marshals as
	if err := json.Compact(&b, got); err != nil {
		return false
	}
	return b.String() == want
}

// relaid is the change to out, edited from doc, laid out as doc is.
func relaid(doc, out []byte, old, new string) (Change, error) {
	out, err := Relayout(doc, out)
	if err != nil {
		return Change{}, err
	}
	return Change{Doc: out, Old: old, New: new}, nil
}

// unsetOr is s as one line of JSON, "" for "" (the value is unset).
func unsetOr(s string) string {
	if s == "" {
		return ""
	}
	return Compact(s)
}

// Relayout is the JSON document out in the layout of orig, the file it was edited from: one line when orig is one,
// else indented by orig's own indent (its first indented line's leading whitespace, two spaces when it has none), with
// orig's final newline or none. Every member keeps its bytes but for the whitespace between them.
func Relayout(orig, out []byte) ([]byte, error) {
	var b bytes.Buffer
	body := bytes.TrimRight(orig, " \t\r\n")
	if !bytes.Contains(body, []byte("\n")) {
		if err := json.Compact(&b, out); err != nil {
			return nil, err
		}
	} else {
		if err := json.Indent(&b, out, "", indentOf(body)); err != nil {
			return nil, err
		}
	}
	if len(body) < len(orig) && bytes.HasSuffix(orig, []byte("\n")) {
		b.WriteByte('\n')
	}
	return b.Bytes(), nil
}

// indentOf is the leading whitespace of the first indented line of doc ("  " when none is).
func indentOf(doc []byte) string {
	for _, line := range strings.Split(string(doc), "\n")[1:] {
		if ws := line[:len(line)-len(strings.TrimLeft(line, " \t"))]; ws != "" && len(ws) < len(line) {
			return ws
		}
	}
	return "  "
}

// NewFails is the FAIL lines of after that before does not have: a change is refused for what it breaks, not for a
// problem the config already had.
func NewFails(before, after []string) []string {
	var out []string
	for _, f := range after {
		if !slices.Contains(before, f) {
			out = append(out, f)
		}
	}
	return out
}

// The setup writes (T-1121): obor setup changes .obor/config.json through these functions, never by a key path of its
// own, so the shape changes in this package (docs/research/config-shape.md §4.4, P1).

// Write is one setup write to a config document: the new document, the key it set (a dotted path) and its value, as
// the caller's "config <key> = <value> (uncommitted)" line says them.
type Write struct {
	Doc      []byte
	Key, Val string
}

// write sets path to v in doc; key is how the caller's line names it. A doc of shape 1 is reshaped first (Reshape):
// a setup write never mixes the shapes.
func write(key string, doc []byte, path []string, v any) (Write, error) {
	if ShapeOf(doc) < ShapeVersion {
		var err error
		if doc, err = Reshape(doc); err != nil {
			return Write{}, err
		}
	}
	out, err := EditJSON(doc, path, v)
	return Write{Doc: out, Key: key, Val: fmt.Sprintf("%v", v)}, err
}

// ProjectDoc is doc (the kit's config example, shape 2) as obor init writes a new project's config: name, prefix and
// lanesDir set, no lanes, project commands, lane links or protected files, the commands init detects (lanes.setup,
// merge.check, nightly.slow_check) null; the example's agents.difficulty and agents.kinds go, but for each kit kind's
// plugin (KitKinds).
func ProjectDoc(doc []byte, name, prefix, lanesDir string) ([]byte, error) {
	for _, kv := range []struct {
		path []string
		val  any
	}{
		{p("project", "name"), name}, {p("project", "agent_prefix"), prefix}, {p("lanes", "dir"), lanesDir},
		{p("lanes", "count"), 0}, {p("lanes", "list"), nil},
		{p("lanes", "setup"), json.RawMessage(`null`)}, {p("lanes", "ready"), nil},
		{p("merge", "check"), json.RawMessage(`null`)}, {p("nightly", "slow_check"), json.RawMessage(`null`)},
		{p("commands"), nil},
		{p("lanes", "links"), []string{}}, {p("rules", "protected_files"), []string{}},
		{p("agents", "difficulty"), nil}, {p("agents", "kinds"), nil},
	} {
		var err error
		if doc, err = EditJSON(doc, kv.path, kv.val); err != nil {
			return nil, err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(KitKinds)) {
		var err error
		if doc, err = EditJSON(doc, p("agents", "kinds", name, "plugin"), KitKinds[name]); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

// commandPath is where the command run by name lives: its group for one of FixedCommands, else commands.<name>.
func commandPath(name string) []string {
	if at, ok := FixedCommands[name]; ok {
		return at
	}
	return p("commands", name)
}

// SetCommand returns doc with the command run by name = cmd (commandPath).
func SetCommand(doc []byte, name, cmd string) (Write, error) {
	at := commandPath(name)
	return write(strings.Join(at, "."), doc, at, cmd)
}

// SetRoleModel returns doc with agents.roles.<role> = ref.
func SetRoleModel(doc []byte, role, ref string) (Write, error) {
	return write("agents.roles."+role, doc, p("agents", "roles", role), ref)
}

// SetDifficulty returns doc with agents.difficulty = levels.
func SetDifficulty(doc []byte, levels map[string][]string) (Write, error) {
	return write("agents.difficulty", doc, p("agents", "difficulty"), levels)
}

// SetLanes returns doc with lanes.count = n (it replaces lanes.list, T-1031).
func SetLanes(doc []byte, n int) (Write, error) {
	w, err := write("lanes.count", doc, p("lanes", "list"), nil)
	if err != nil {
		return w, err
	}
	return write("lanes.count", w.Doc, p("lanes", "count"), n)
}

// SetLaneLinks returns doc with lanes.links = links.
func SetLaneLinks(doc []byte, links []string) (Write, error) {
	return write("lanes.links", doc, p("lanes", "links"), links)
}

// SetSetupConfirmed returns doc with agents.confirmed = confirmed.
func SetSetupConfirmed(doc []byte, confirmed bool) (Write, error) {
	return write("agents.confirmed", doc, p("agents", "confirmed"), confirmed)
}

// SetKindCommand returns doc with agents.kinds.<kind>.plugin = cmd.
func SetKindCommand(doc []byte, kind, cmd string) (Write, error) {
	return write("agents.kinds."+kind+".plugin", doc, p("agents", "kinds", kind, "plugin"), cmd)
}

// RenameProject returns doc with project.name and project.agent_prefix set to the new ones: each only when the file
// has it, or always with force (config.json; a local.json changes only what it overrides). obor setup rename's step 1.
// A file of shape 1 (or the local.json beside one) keeps its shape: name and prefix there.
func RenameProject(doc []byte, name, prefix string, force bool) ([]byte, error) {
	out := doc
	for _, kv := range []struct {
		at, old []string
		val     string
	}{{p("project", "name"), p("name"), name}, {p("project", "agent_prefix"), p("prefix"), prefix}} {
		at := shapedPath(out, kv.at, kv.old)
		cur, ok := stringAt(out, at...)
		if cur == kv.val || (!ok && !force) {
			continue
		}
		var err error
		if out, err = EditJSON(out, at, kv.val); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// shapedPath is where a setup edit of doc finds and writes a key: at (shape 2's path) when doc has it, old (shape 1's)
// when doc has that, else at for a file of shape 2 and old for one of shape 1 (ShapeOf).
func shapedPath(doc []byte, at, old []string) []string {
	if v, _ := MemberAt(doc, at); v != nil {
		return at
	}
	if v, _ := MemberAt(doc, old); v != nil || ShapeOf(doc) < ShapeVersion {
		return old
	}
	return at
}

// stringAt is the string member of doc at path; ok false when there is none or it is no string.
func stringAt(doc []byte, path ...string) (string, bool) {
	raw, err := MemberAt(doc, path)
	if err != nil || raw == nil {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// pushPath is rules.may_push in doc's shape (shapedPath; guards.allow_push in shape 1).
func pushPath(doc []byte) []string {
	return shapedPath(doc, p("rules", "may_push"), p("guards", "allow_push"))
}

// PushEntries is rules.may_push in doc (nil when the file has none, or it is no string list).
func PushEntries(doc []byte) []string {
	raw, err := MemberAt(doc, pushPath(doc))
	if err != nil || raw == nil {
		return nil
	}
	var entries []string
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	return entries
}

// SetPushEntries returns doc with rules.may_push = entries.
func SetPushEntries(doc []byte, entries []string) ([]byte, error) {
	return EditJSON(doc, pushPath(doc), entries)
}
