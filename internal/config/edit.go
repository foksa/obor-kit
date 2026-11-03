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

// The setup edit (T-753, T-765): the owner changes one setup value from obor web's Setup page, and serve, the one
// writer, writes .obor/config.json and commits it. Exactly these keys are editable:
//
//	difficulty.<level>   the level's ordered list of model refs (<model>[:<effort>])
//	roles.<role>         a configured role's model ref
//	lanes                how many lanes (L-01 … L-NN)
//	kinds.<kind>.args    the args appended to every start of a kind
//
// Any other key stays obor setup's or a hand edit's.
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
const EditKeys = "difficulty.<level>, roles.<role>, lanes, kinds.<kind>.args"

// Edit is the change of the setup value at key to val (JSON) in doc, the bytes of c's config.json, and c's local.json
// as it is, an error names why the key is not editable, val not a value it takes (an unknown level, role or kind, a
// model ref naming no model or an effort its kind does not take) or the key is one .obor/local.json overrides (edit it
// there: the committed value would not count). Edit does not validate the whole config: the caller loads the result
// (LoadWith) and runs Validate.
func (c *Config) Edit(doc []byte, key string, val json.RawMessage) (Change, error) {
	parts := strings.Split(key, ".")
	switch {
	case len(parts) == 2 && parts[0] == "difficulty":
		return c.editLevel(doc, parts[1], val)
	case len(parts) == 2 && parts[0] == "roles",
		len(parts) == 3 && parts[0] == "roles" && parts[2] == "type": // roles.<role>.type: the Setup page's key until its redesign
		return c.editRole(doc, parts[1], val)
	case key == "lanes":
		return c.editLanes(doc, val)
	case len(parts) == 3 && parts[0] == "kinds" && parts[2] == "args":
		return c.editKindArgs(doc, parts[1], val)
	}
	return Change{}, fmt.Errorf("%s is not an editable key (%s)", Compact(key), EditKeys)
}

// editLevel sets difficulty.<level> to a list of model refs, each naming a model of c.
func (c *Config) editLevel(doc []byte, level string, val json.RawMessage) (Change, error) {
	if _, ok := DifficultyDefault[level]; !ok {
		return Change{}, fmt.Errorf("%s is not a level: hard, medium or simple", Compact(level))
	}
	var refs []string
	if err := json.Unmarshal(val, &refs); err != nil || refs == nil {
		return Change{}, fmt.Errorf("difficulty.%s takes a list of model refs", level)
	}
	for _, ref := range refs {
		if err := c.CheckModelRef(ref); err != nil {
			return Change{}, fmt.Errorf("difficulty.%s: %v", level, err)
		}
	}
	old := Compact(c.DifficultyList(level))
	return c.change("difficulty."+level, doc, []string{"difficulty", level}, refs, old, Compact(refs))
}

// editLanes sets the lanes count (a whole number, 0 or more): a lower count's dropped lanes are in Change.Dropped.
func (c *Config) editLanes(doc []byte, val json.RawMessage) (Change, error) {
	var n int
	if err := json.Unmarshal(val, &n); err != nil || n < 0 {
		return Change{}, fmt.Errorf("lanes takes a whole number (L-01 … L-NN)")
	}
	ch, err := c.change("lanes", doc, []string{"lanes"}, n, Compact(c.Data.Lanes), Compact(n))
	for i := n + 1; i <= c.Data.Lanes; i++ {
		ch.Dropped = append(ch.Dropped, LaneName(i))
	}
	return ch, err
}

// editKindArgs sets kinds.<kind>.args: a list of args with no bare -- (obor adds the first prompt itself); an empty
// list drops the key.
func (c *Config) editKindArgs(doc []byte, kind string, val json.RawMessage) (Change, error) {
	if _, ok := agents.Lookup(kind); !ok {
		return Change{}, fmt.Errorf("%s is not a kind (kinds: %s)", Compact(kind), strings.Join(agents.Names(), ", "))
	}
	var args []string
	if err := json.Unmarshal(val, &args); err != nil {
		return Change{}, fmt.Errorf("kinds.%s.args takes a list of strings", kind)
	}
	if slices.Contains(args, "--") {
		return Change{}, fmt.Errorf("kinds.%s.args: `--` is not allowed: obor adds the first prompt itself", kind)
	}
	var v any = args
	if len(args) == 0 {
		v = nil
	}
	old := ""
	if cur := c.Data.Kinds.M[kind].Args; len(cur) > 0 {
		old = Compact(cur)
	}
	newLine := ""
	if len(args) > 0 {
		newLine = Compact(args)
	}
	return c.change("kinds."+kind+".args", doc, []string{"kinds", kind, "args"}, v, old, newLine)
}

// editRole sets a configured role's model ref; "" deletes the member, so a non-optional role runs its default model
// again (an optional role refuses it: deleting its member would disable the role).
func (c *Config) editRole(doc []byte, role string, val json.RawMessage) (Change, error) {
	rc, ok := c.Role(role)
	if !ok || role == "worker" || rc.Role.Optional && !rc.Configured {
		names := []string{}
		for _, r := range roles.All() {
			if rc, _ := c.Role(r.Name); r.Name != "worker" && (!r.Optional || rc.Configured) {
				names = append(names, r.Name)
			}
		}
		return Change{}, fmt.Errorf("%s is not a configured role (roles: %s; a worker's model is its dispatch's)",
			Compact(role), strings.Join(names, ", "))
	}
	var ref string
	if err := json.Unmarshal(val, &ref); err != nil {
		return Change{}, fmt.Errorf("roles.%s takes a model ref (<model>[:<effort>])", role)
	}
	if ref == "" {
		if rc.Role.Optional {
			return Change{}, fmt.Errorf("roles.%s takes a model ref (<model>[:<effort>]): %s is an optional role, and "+
				"removing its model would disable it", role, role)
		}
		return c.change("roles."+role, doc, []string{"roles", role}, nil, unsetOr(ModelRef(rc.Type, rc.Effort)), "role's default")
	}
	if err := c.CheckModelRef(ref); err != nil {
		return Change{}, fmt.Errorf("roles.%s: %v", role, err)
	}
	return c.change("roles."+role, doc, []string{"roles", role}, ref, unsetOr(ModelRef(rc.Type, rc.Effort)), Compact(ref))
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
