package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"obor/internal/agents"
	"obor/internal/roles"
)

// The setup edit (T-753): the owner changes one setup value from obor web's Setup page, and serve, the one writer,
// writes .obor/config.json and commits it. Exactly these keys are editable:
//
//	difficulty.<level>         the level's ordered list of type references, each naming a lane type
//	lane_types.<type>.model    the model its args name (per kind: agents.ModelArgs)
//	lane_types.<type>.effort   the effort its args set (per kind: agents.EffortArgs)
//	lane_types.<type>.max      its cap (0 or null: none)
//	lanes.<LANE>               an existing lane's type ("": pooled)
//	roles.<role>.type          a configured role's type reference ("": the role's default)
//
// Adding or removing lanes or lane types, other args and any other key stay obor setup's or a hand edit's.
//
// A key .obor/local.json overrides (the key itself, a member of it, or an ancestor local.json replaces) is refused:
// writing it to config.json would report success and change nothing that runs, and an args rewrite would copy this
// machine's args into the committed file (T-754).

// Change is one setup edit applied to a config.json: its new bytes (in the file's own layout, Relayout) and the key's
// value before and after, each one line of JSON ("" for a value the config leaves unset).
type Change struct {
	Doc      []byte
	Old, New string
	// Lane is the lane a lanes.<LANE> edit changes ("": another key): serve refuses it while the lane has a task.
	Lane string
}

// Rev is a config.json's revision as the Setup page reads it (T-760): the sha256 of its bytes, its first 16 hex
// characters. A setup edit names the revision its page read, and serve refuses it when the file is another one now:
// another tab, the lead or a hand edit changed it since (an edit built from a stale page never undoes theirs).
func Rev(doc []byte) string {
	sum := sha256.Sum256(doc)
	return hex.EncodeToString(sum[:8])
}

// EditKeys are the editable keys as patterns, for errors.
const EditKeys = "difficulty.<level>, lane_types.<type>.model|effort|max, lanes.<LANE>, roles.<role>.type"

// Edit is the change of the setup value at key to val (JSON) in doc, the bytes of c's config.json, and c's local.json
// as it is, an error names why the key is not editable, val not a value it takes (an unknown level, lane, lane type or
// role, a type reference naming no lane type, an effort the kind does not take) or the key is one .obor/local.json
// overrides (edit it there: the committed value would not count). Edit does not validate the whole config: the caller
// loads the result (LoadWith) and runs Validate.
func (c *Config) Edit(doc []byte, key string, val json.RawMessage) (Change, error) {
	parts := strings.Split(key, ".")
	switch {
	case len(parts) == 2 && parts[0] == "difficulty":
		return c.editLevel(doc, parts[1], val)
	case len(parts) == 3 && parts[0] == "lane_types":
		return c.editLaneType(doc, parts[1], parts[2], val)
	case len(parts) == 2 && parts[0] == "lanes":
		return c.editLane(doc, parts[1], val)
	case len(parts) == 3 && parts[0] == "roles" && parts[2] == "type":
		return c.editRole(doc, parts[1], val)
	}
	return Change{}, fmt.Errorf("%s is not an editable key (%s)", Compact(key), EditKeys)
}

// editLevel sets difficulty.<level> to a list of type references, each naming a lane type of c.
func (c *Config) editLevel(doc []byte, level string, val json.RawMessage) (Change, error) {
	if _, ok := DifficultyDefault[level]; !ok {
		return Change{}, fmt.Errorf("%s is not a level: hard, medium or simple", Compact(level))
	}
	var refs []string
	if err := json.Unmarshal(val, &refs); err != nil || refs == nil {
		return Change{}, fmt.Errorf("difficulty.%s takes a list of type references", level)
	}
	for _, ref := range refs {
		if err := c.CheckTypeRef(ref); err != nil {
			return Change{}, fmt.Errorf("difficulty.%s: %v", level, err)
		}
	}
	old := Compact(c.DifficultyList(level))
	return c.change("difficulty."+level, doc, []string{"difficulty", level}, refs, old, Compact(refs))
}

// editLaneType sets a lane type's model or effort (rewritten inside its args, per kind) or its max.
func (c *Config) editLaneType(doc []byte, name, field string, val json.RawMessage) (Change, error) {
	lt, ok := c.LaneType(name)
	if !ok {
		return Change{}, fmt.Errorf("%s is not a lane type of this project (lane types: %s)", Compact(name),
			strings.Join(c.LaneTypeNames(), ", "))
	}
	if own, err := MemberAt(doc, []string{"lane_types", name}); err != nil || own == nil {
		return Change{}, fmt.Errorf("lane type %s is not in .obor/config.json (local.json sets it): edit it there", name)
	}
	key := "lane_types." + name + "." + field
	switch field {
	case "model", "effort":
		var s string
		if err := json.Unmarshal(val, &s); err != nil {
			return Change{}, fmt.Errorf("%s takes a string", key)
		}
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "-") || strings.ContainsAny(s, " \t\n") {
			return Change{}, fmt.Errorf("%s: %s is no %s", key, Compact(s), field)
		}
		base, err := committedArgs(doc, name) // never lt.Args: the merged args may be this machine's (T-754)
		if err != nil {
			return Change{}, err
		}
		var args []string
		committed := LaneType{Args: base}
		old := committed.Model()
		if field == "model" {
			args, err = agents.ModelArgs(lt.Kind, base, s)
		} else {
			old = committed.Effort()
			args, err = agents.EffortArgs(lt.Kind, base, s)
		}
		if err != nil {
			return Change{}, fmt.Errorf("%s: %v", key, err)
		}
		var v any = args
		if len(args) == 0 {
			v = nil // no args left: the key goes, as a type without args is written
		}
		return c.change(key, doc, []string{"lane_types", name, "args"}, v, unsetOr(old), unsetOr(s))
	case "max":
		n := 0
		if t := bytes.TrimSpace(val); len(t) > 0 && string(t) != "null" && string(t) != `""` {
			if err := json.Unmarshal(t, &n); err != nil || n < 0 {
				return Change{}, fmt.Errorf("%s takes a whole number, 0 or empty for no cap", key)
			}
		}
		var v any = n
		if n == 0 {
			v = nil
		}
		return c.change(key, doc, []string{"lane_types", name, "max"}, v, Compact(lt.Max), Compact(n))
	}
	return Change{}, fmt.Errorf("%s is not an editable key (%s)", Compact(key), EditKeys)
}

// editLane sets an existing lane's type: "" (pooled) or a lane type of c.
func (c *Config) editLane(doc []byte, lane string, val json.RawMessage) (Change, error) {
	if !c.HasLane(lane) {
		return Change{}, fmt.Errorf("%s is not a lane of this project: obor setup adds lanes", Compact(lane))
	}
	var typ string
	if err := json.Unmarshal(val, &typ); err != nil {
		return Change{}, fmt.Errorf("lanes.%s takes a lane type (\"\": pooled)", lane)
	}
	if _, ok := c.LaneType(typ); typ != "" && !ok {
		return Change{}, fmt.Errorf("lanes.%s: %s is not a lane type of this project (lane types: %s)", lane, Compact(typ),
			strings.Join(c.LaneTypeNames(), ", "))
	}
	own, err := MemberAt(doc, []string{"lanes", lane})
	if err != nil || own == nil {
		return Change{}, fmt.Errorf("lane %s is not in .obor/config.json (local.json sets it): edit it there", lane)
	}
	path := []string{"lanes", lane}
	if _, isObject := object(own); isObject { // {type, agent}: only its type
		path = append(path, "type")
	}
	ch, err := c.change("lanes."+lane, doc, path, typ, Compact(c.Data.Lanes.M[lane].Type), Compact(typ))
	ch.Lane = lane
	return ch, err
}

// editRole sets a configured role's type reference ("": the key goes, the role's default type).
func (c *Config) editRole(doc []byte, role string, val json.RawMessage) (Change, error) {
	rc, ok := c.Role(role)
	if !ok || role == "worker" || rc.Role.Optional && !rc.Configured {
		names := []string{}
		for _, r := range roles.All() {
			if rc, _ := c.Role(r.Name); r.Name != "worker" && (!r.Optional || rc.Configured) {
				names = append(names, r.Name)
			}
		}
		return Change{}, fmt.Errorf("%s is not a configured role (roles: %s; a worker's type is its lane's)", Compact(role),
			strings.Join(names, ", "))
	}
	var ref string
	if err := json.Unmarshal(val, &ref); err != nil {
		return Change{}, fmt.Errorf("roles.%s.type takes a type reference (<type>[:<effort>])", role)
	}
	if ref != "" {
		if err := c.CheckTypeRef(ref); err != nil {
			return Change{}, fmt.Errorf("roles.%s.type: %v", role, err)
		}
	}
	old := unsetOr(TypeRef(rc.Type, rc.Effort))
	path := []string{"roles", role, "type"}
	if ref != "" {
		return c.change("roles."+role+".type", doc, path, ref, old, Compact(ref))
	}
	// "": the key goes; an entry left empty stays as {} (an optional role is configured by its entry)
	ch, err := c.change("roles."+role+".type", doc, path, nil, old, "")
	if err != nil {
		return Change{}, err
	}
	if entry, _ := MemberAt(ch.Doc, []string{"roles", role}); entry == nil {
		out, err := EditJSON(ch.Doc, []string{"roles", role}, json.RawMessage("{}"))
		if err != nil {
			return Change{}, err
		}
		return relaid(doc, out, old, "")
	}
	return ch, nil
}

// change is c's edit of key: doc with path (the member the key writes) set to v (nil deletes it), laid out as doc is,
// with the value's old and new lines. It refuses a member .obor/local.json overrides (localRefusal) and one the merged
// config does not take up: either way the file would be written and what runs would not change.
func (c *Config) change(key string, doc []byte, path []string, v any, old, new string) (Change, error) {
	if at := c.localShadow(path); at != "" {
		return Change{}, localRefusal(key, at)
	}
	out, err := EditJSON(doc, path, v)
	if err != nil {
		return Change{}, err
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

// applied reports whether the merged config, doc with c's .obor/local.json, has v at path: what a write to doc would
// leave running. v nil is the key gone (or null).
func (c *Config) applied(doc []byte, path []string, v any) bool {
	merged := doc
	if len(c.local) > 0 {
		merged = merge(doc, c.local)
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

// committedArgs is the args doc sets for lane type name (nil when it sets none): the base an args rewrite starts from,
// never the merged type's args, which may be this machine's own (T-754).
func committedArgs(doc []byte, name string) ([]string, error) {
	raw, err := MemberAt(doc, []string{"lane_types", name, "args"})
	if err != nil || raw == nil {
		return nil, err
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("lane_types.%s.args is not a list of strings", name)
	}
	return args, nil
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
